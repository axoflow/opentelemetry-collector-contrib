// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package idirareceiver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/extension/xextension/storage"
	"go.opentelemetry.io/collector/receiver/receivertest"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/idirareceiver/internal/metadata"
)

// fakeAPI serves the token, createQuery and results endpoints, handing out one page of events
// per createQuery call followed by an empty page.
type fakeAPI struct {
	mu      sync.Mutex
	queries []createQueryRequest
	apiKeys []string
	tokens  []string
	// pages maps a cursor to the response served for it.
	pages map[string]resultsResponse
}

func newFakeAPI() *fakeAPI {
	return &fakeAPI{pages: map[string]resultsResponse{}}
}

func (f *fakeAPI) server(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"access_token":"a-token","token_type":"Bearer","expires_in":3600}`))
		assert.NoError(t, err)
	})
	mux.HandleFunc(createQueryPath, func(w http.ResponseWriter, r *http.Request) {
		var req createQueryRequest
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&req)) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		f.mu.Lock()
		f.queries = append(f.queries, req)
		f.apiKeys = append(f.apiKeys, r.Header.Get("x-api-key"))
		f.tokens = append(f.tokens, r.Header.Get("Authorization"))
		cursor := "cursor-" + req.Query.FilterModel.Date.DateFrom
		page := resultsResponse{Data: []map[string]any{
			{"uuid": "1", "timestamp": float64(1745573887741), "message": "first"},
			{"uuid": "2", "message": "no timestamp"},
		}}
		page.Paging.Cursor.CursorRef = cursor + "-next"
		f.pages[cursor] = page
		f.pages[cursor+"-next"] = resultsResponse{}
		f.mu.Unlock()

		writeJSON(t, w, createQueryResponse{CursorRef: cursor})
	})
	mux.HandleFunc(resultsPath, func(w http.ResponseWriter, r *http.Request) {
		var req resultsRequest
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&req)) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		f.mu.Lock()
		page, ok := f.pages[req.CursorRef]
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		writeJSON(t, w, page)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	assert.NoError(t, json.NewEncoder(w).Encode(v))
}

// fakeClock pins nowFunc and returns a function to advance it.
func fakeClock(t *testing.T) func(time.Duration) {
	now := time.Date(2026, 5, 2, 10, 30, 0, 0, time.UTC)
	nowFunc = func() time.Time { return now }
	t.Cleanup(func() { nowFunc = time.Now })
	return func(d time.Duration) { now = now.Add(d) }
}

func testConfig(endpoint string) *Config {
	cfg := createDefaultConfig().(*Config)
	cfg.Endpoint = endpoint
	cfg.APIKey = "an-api-key"
	cfg.TokenURL = endpoint + "/token"
	cfg.ClientID = "siem-integration"
	cfg.ClientSecret = "a-secret"
	cfg.InitialLookback = time.Hour
	cfg.ApplicationCodes = []string{"DPA"}
	return cfg
}

// TestPollDrainsPagesAndCheckpoints covers the two-step flow: one createQuery per poll, results
// pages drained until empty, and the next poll resuming where the previous one ended.
func TestPollDrainsPagesAndCheckpoints(t *testing.T) {
	api := newFakeAPI()
	srv := api.server(t)
	cfg := testConfig(srv.URL)
	advance := fakeClock(t)

	sink := new(consumertest.LogsSink)
	r := newReceiver(cfg, receivertest.NewNopSettings(metadata.Type), sink)
	r.client = &client{httpClient: srv.Client(), endpoint: cfg.Endpoint, apiKey: string(cfg.APIKey)}
	r.storage = storage.NewNopClient()

	require.NoError(t, r.poll(t.Context()))
	firstEnd := r.lastEnd
	require.False(t, firstEnd.IsZero())
	advance(time.Minute)
	require.NoError(t, r.poll(t.Context()))

	require.Len(t, api.queries, 2)
	first, second := api.queries[0].Query, api.queries[1].Query

	assert.Equal(t, 500, first.PageSize)
	assert.Equal(t, selectedFields, first.SelectedFields)
	assert.Equal(t, []filterEntry{{Op: "include", Params: []string{"DPA"}}}, first.FilterModel.ApplicationCode)
	assert.Equal(t, firstEnd.Add(-time.Hour).Format(dateLayout), first.FilterModel.Date.DateFrom)
	assert.Equal(t, firstEnd.Format(dateLayout), first.FilterModel.Date.DateTo)
	// The end of the first window is reused as the start of the second.
	assert.Equal(t, firstEnd.Format(dateLayout), second.FilterModel.Date.DateFrom)
	assert.Equal(t, r.lastEnd.Format(dateLayout), second.FilterModel.Date.DateTo)

	require.Equal(t, 4, sink.LogRecordCount())
	records := sink.AllLogs()[0].ResourceLogs().At(0).ScopeLogs().At(0).LogRecords()
	assert.Equal(t, map[string]any{"uuid": "1", "timestamp": float64(1745573887741), "message": "first"},
		records.At(0).Body().Map().AsRaw())
	assert.Equal(t, time.UnixMilli(1745573887741).UTC(), records.At(0).Timestamp().AsTime().UTC())
	assert.Zero(t, records.At(1).Timestamp())
	assert.NotZero(t, records.At(1).ObservedTimestamp())
}

func TestPollPersistsCheckpoint(t *testing.T) {
	api := newFakeAPI()
	srv := api.server(t)
	cfg := testConfig(srv.URL)
	advance := fakeClock(t)

	apiClient := &client{httpClient: srv.Client(), endpoint: cfg.Endpoint, apiKey: string(cfg.APIKey)}
	persisted := &fakeStorage{data: map[string][]byte{}}
	r := newReceiver(cfg, receivertest.NewNopSettings(metadata.Type), new(consumertest.LogsSink))
	r.client = apiClient
	r.storage = persisted

	require.NoError(t, r.poll(t.Context()))
	stored, err := time.Parse(time.RFC3339, string(persisted.data[checkpointKey]))
	require.NoError(t, err)
	assert.Equal(t, r.lastEnd, stored)

	// A restart reads the persisted checkpoint instead of falling back to initial_lookback.
	restarted := newReceiver(cfg, receivertest.NewNopSettings(metadata.Type), new(consumertest.LogsSink))
	restarted.client = apiClient
	restarted.storage = persisted
	advance(time.Minute)
	require.NoError(t, restarted.poll(t.Context()))
	require.Len(t, api.queries, 2)
	assert.Equal(t, stored.Format(dateLayout), api.queries[1].Query.FilterModel.Date.DateFrom)
}

func TestPollReportsAPIErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("invalid api key"))
	}))
	defer srv.Close()

	cfg := testConfig(srv.URL)
	r := newReceiver(cfg, receivertest.NewNopSettings(metadata.Type), new(consumertest.LogsSink))
	r.client = &client{httpClient: srv.Client(), endpoint: cfg.Endpoint, apiKey: string(cfg.APIKey)}
	r.storage = storage.NewNopClient()

	err := r.poll(t.Context())
	assert.ErrorContains(t, err, "403")
	assert.ErrorContains(t, err, "invalid api key")
	assert.True(t, r.lastEnd.IsZero())
}

// TestStartAuthenticates checks that the receiver obtains a token and sends it with the API key.
func TestStartAuthenticates(t *testing.T) {
	api := newFakeAPI()
	srv := api.server(t)

	cfg := testConfig(srv.URL)
	cfg.PollInterval = time.Minute

	sink := new(consumertest.LogsSink)
	r := newReceiver(cfg, receivertest.NewNopSettings(metadata.Type), sink)
	require.NoError(t, r.Start(t.Context(), componenttest.NewNopHost()))
	require.Eventually(t, func() bool { return sink.LogRecordCount() == 2 }, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, r.Shutdown(t.Context()))

	require.Len(t, api.tokens, 1)
	assert.Equal(t, "Bearer a-token", api.tokens[0])
	assert.Equal(t, "an-api-key", api.apiKeys[0])
}

func TestStartMissingStorageExtension(t *testing.T) {
	cfg := testConfig("https://audit.example.cloud")
	id := component.MustNewID("file_storage")
	cfg.StorageID = &id

	r := newReceiver(cfg, receivertest.NewNopSettings(metadata.Type), new(consumertest.LogsSink))
	err := r.Start(t.Context(), componenttest.NewNopHost())
	assert.ErrorContains(t, err, `storage extension "file_storage" not found`)
}

type fakeStorage struct {
	data map[string][]byte
}

func (f *fakeStorage) Get(_ context.Context, key string) ([]byte, error) { return f.data[key], nil }

func (f *fakeStorage) Set(_ context.Context, key string, value []byte) error {
	f.data[key] = value
	return nil
}

func (f *fakeStorage) Delete(_ context.Context, key string) error {
	delete(f.data, key)
	return nil
}

func (*fakeStorage) Batch(_ context.Context, _ ...*storage.Operation) error { return nil }
func (*fakeStorage) Close(_ context.Context) error                          { return nil }
