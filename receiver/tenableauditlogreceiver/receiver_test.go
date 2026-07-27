// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package tenableauditlogreceiver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/extension/xextension/storage"
	"go.opentelemetry.io/collector/receiver/receivertest"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/tenableauditlogreceiver/internal/metadata"
)

var testEvents = []map[string]any{
	{"id": "1", "action": "user.login", "received": "2026-07-01T10:00:00Z"},
	{"id": "2", "action": "user.logout", "received": "2026-07-01T11:00:00Z"},
	{"id": "3", "action": "scan.create", "received": "2026-07-01T11:00:00Z"},
}

// newTestServer serves testEvents two at a time, ignoring the date filter the same way the
// audit log API does when an event is indexed into a second the receiver has already read.
func newTestServer(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		assert.Equal(t, "accessKey=key;secretKey=secret", req.Header.Get("X-ApiKeys"))
		assert.Equal(t, auditLogPath, req.URL.Path)
		assert.Contains(t, req.URL.Query().Get("f"), "date.gte:")
		assert.Equal(t, "received:asc", req.URL.Query().Get("sort"))

		resp := auditLogResponse{}
		// The first request of a poll must seed the cursor with "0" to get a next cursor back.
		if next := req.URL.Query().Get("next"); next == "0" {
			resp.Events = testEvents[:2]
			resp.Pagination.Next = "page2"
		} else {
			assert.Equal(t, "page2", next)
			resp.Events = testEvents[2:]
		}
		w.Header().Set("Content-Type", "application/json")
		assert.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
}

func newTestReceiver(t *testing.T, endpoint string, sink *consumertest.LogsSink) *tenableReceiver {
	cfg := createDefaultConfig().(*Config)
	cfg.Endpoint = endpoint
	cfg.AccessKey = "key"
	cfg.SecretKey = "secret"
	cfg.PageSize = 2
	require.NoError(t, cfg.Validate())

	r := newReceiver(cfg, receivertest.NewNopSettings(metadata.Type), sink)
	r.client = http.DefaultClient
	r.storage = storage.NewNopClient()
	return r
}

func TestPollPaginatesAndDeduplicates(t *testing.T) {
	server := newTestServer(t)
	defer server.Close()

	sink := new(consumertest.LogsSink)
	r := newTestReceiver(t, server.URL, sink)

	require.NoError(t, r.poll(t.Context()))
	require.Equal(t, 3, sink.LogRecordCount())

	records := sink.AllLogs()[0].ResourceLogs().At(0).ScopeLogs().At(0).LogRecords()
	assert.Equal(t, "2026-07-01T10:00:00Z", records.At(0).Timestamp().AsTime().Format(time.RFC3339))
	assert.Equal(t, map[string]any{"id": "1", "action": "user.login", "received": "2026-07-01T10:00:00Z"},
		records.At(0).Body().Map().AsRaw())
	assert.NotZero(t, records.At(0).ObservedTimestamp())

	// The newest event time is shared by two events, so both ids must be remembered.
	assert.Equal(t, "2026-07-01T11:00:00Z", r.cp.LastEventTime.Format(time.RFC3339))
	assert.Equal(t, []string{"2", "3"}, r.cp.SeenIDs)

	// A second poll re-reads the same events and must emit nothing.
	require.NoError(t, r.poll(t.Context()))
	assert.Equal(t, 3, sink.LogRecordCount())
}

// An event can be indexed after the poll that already read its second. The inclusive date.gte
// filter returns it again, and only the ids already emitted at that second are skipped.
func TestPollEmitsLateEventSharingCheckpointSecond(t *testing.T) {
	late := map[string]any{"id": "4", "action": "scan.delete", "received": "2026-07-01T11:00:00Z"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		assert.Contains(t, req.URL.Query().Get("f"), "date.gte:2026-07-01T11:00:00Z")
		assert.NoError(t, json.NewEncoder(w).Encode(auditLogResponse{
			Events: []map[string]any{testEvents[1], testEvents[2], late},
		}))
	}))
	defer server.Close()

	sink := new(consumertest.LogsSink)
	r := newTestReceiver(t, server.URL, sink)
	r.cp = checkpoint{LastEventTime: time.Date(2026, 7, 1, 11, 0, 0, 0, time.UTC), SeenIDs: []string{"2", "3"}}

	require.NoError(t, r.poll(t.Context()))
	require.Equal(t, 1, sink.LogRecordCount())
	records := sink.AllLogs()[0].ResourceLogs().At(0).ScopeLogs().At(0).LogRecords()
	assert.Equal(t, late, records.At(0).Body().Map().AsRaw())
	assert.Equal(t, []string{"2", "3", "4"}, r.cp.SeenIDs)
}

func TestPollEmitsEventsWithoutTimestamp(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		assert.NoError(t, json.NewEncoder(w).Encode(auditLogResponse{
			Events: []map[string]any{{"id": "1"}, {"id": "2", "received": "not-a-timestamp"}},
		}))
	}))
	defer server.Close()

	sink := new(consumertest.LogsSink)
	r := newTestReceiver(t, server.URL, sink)

	require.NoError(t, r.poll(t.Context()))
	require.Equal(t, 2, sink.LogRecordCount())
	records := sink.AllLogs()[0].ResourceLogs().At(0).ScopeLogs().At(0).LogRecords()
	assert.Zero(t, records.At(0).Timestamp())
	assert.True(t, r.cp.LastEventTime.IsZero())
}

func TestPollFailureKeepsCheckpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	sink := new(consumertest.LogsSink)
	r := newTestReceiver(t, server.URL, sink)

	require.ErrorContains(t, r.poll(t.Context()), "500")
	assert.Equal(t, 0, sink.LogRecordCount())
	assert.True(t, r.cp.LastEventTime.IsZero())
}

func TestPollHonorsRetryAfter(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	sink := new(consumertest.LogsSink)
	r := newTestReceiver(t, server.URL, sink)

	require.ErrorContains(t, r.poll(t.Context()), "rate limited")
	assert.WithinDuration(t, time.Now().Add(30*time.Second), r.rateLimitedUntil, time.Second)

	// The next poll must not touch the API until the Retry-After deadline passes.
	require.NoError(t, r.poll(t.Context()))
	assert.Equal(t, 1, requests)

	r.rateLimitedUntil = time.Now().Add(-time.Second)
	require.Error(t, r.poll(t.Context()))
	assert.Equal(t, 2, requests)
}

func TestPollRateLimitedWithoutRetryAfter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	sink := new(consumertest.LogsSink)
	r := newTestReceiver(t, server.URL, sink)

	require.ErrorContains(t, r.poll(t.Context()), "rate limited")
	assert.WithinDuration(t, time.Now().Add(r.cfg.PollInterval), r.rateLimitedUntil, time.Second)
}

func TestCheckpointPersistence(t *testing.T) {
	sink := new(consumertest.LogsSink)
	r := newTestReceiver(t, "https://cloud.tenable.example", sink)
	client := &memoryStorage{data: map[string][]byte{}}
	r.storage = client

	r.cp = checkpoint{LastEventTime: time.Date(2026, 7, 1, 11, 0, 0, 0, time.UTC), SeenIDs: []string{"2"}}
	r.saveCheckpoint(t.Context())

	restored := newTestReceiver(t, "https://cloud.tenable.example", sink)
	restored.storage = client
	restored.loadCheckpoint(t.Context())
	assert.Equal(t, r.cp, restored.cp)
}

func TestStartShutdown(t *testing.T) {
	server := newTestServer(t)
	defer server.Close()

	sink := new(consumertest.LogsSink)
	cfg := createDefaultConfig().(*Config)
	cfg.Endpoint = server.URL
	cfg.AccessKey = "key"
	cfg.SecretKey = "secret"

	r := newReceiver(cfg, receivertest.NewNopSettings(metadata.Type), sink)
	require.NoError(t, r.Start(t.Context(), componenttest.NewNopHost()))
	assert.Eventually(t, func() bool { return sink.LogRecordCount() == 3 }, time.Second, 10*time.Millisecond)
	require.NoError(t, r.Shutdown(t.Context()))
}

type memoryStorage struct {
	data map[string][]byte
}

func (m *memoryStorage) Get(_ context.Context, key string) ([]byte, error) {
	return m.data[key], nil
}

func (m *memoryStorage) Set(_ context.Context, key string, value []byte) error {
	m.data[key] = value
	return nil
}

func (m *memoryStorage) Delete(_ context.Context, key string) error {
	delete(m.data, key)
	return nil
}

func (*memoryStorage) Batch(_ context.Context, _ ...*storage.Operation) error {
	return nil
}

func (*memoryStorage) Close(_ context.Context) error {
	return nil
}
