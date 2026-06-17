// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearchlogsreceiver

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/extension/xextension/storage"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/receiver/receivertest"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/elasticsearchlogsreceiver/internal/metadata"
)

// fakeClient returns canned pages of hits, one per Search call.
type fakeClient struct {
	pages [][]searchHit
	calls []searchRequest
	idx   int
	err   error
}

func (f *fakeClient) Search(_ context.Context, req searchRequest) (*searchResponse, error) {
	f.calls = append(f.calls, req)
	if f.err != nil {
		return nil, f.err
	}
	resp := &searchResponse{}
	if f.idx < len(f.pages) {
		resp.Hits.Hits = f.pages[f.idx]
		f.idx++
	}
	return resp, nil
}

// memStorage is an in-memory storage.Client for tests.
type memStorage struct {
	data map[string][]byte
}

func newMemStorage() *memStorage { return &memStorage{data: map[string][]byte{}} }

func (m *memStorage) Get(_ context.Context, key string) ([]byte, error) { return m.data[key], nil }
func (m *memStorage) Set(_ context.Context, key string, value []byte) error {
	m.data[key] = value
	return nil
}
func (m *memStorage) Delete(_ context.Context, key string) error           { delete(m.data, key); return nil }
func (*memStorage) Batch(_ context.Context, _ ...*storage.Operation) error { return nil }
func (*memStorage) Close(_ context.Context) error                          { return nil }

func newTestReceiver(t *testing.T, client esLogsClient, consumer consumer.Logs, store storage.Client) *logsReceiver {
	t.Helper()
	cfg := validConfig()
	cfg.PageSize = 2
	r := newLogsReceiver(receivertest.NewNopSettings(metadata.Type), cfg, consumer)
	r.client = client
	r.persister = newCursorPersister(store)
	return r
}

func hit(index, id, ts string) searchHit {
	return searchHit{
		Index:  index,
		ID:     id,
		Source: map[string]any{"@timestamp": ts, "message": "m-" + id},
		Sort:   []any{ts, id},
	}
}

func TestPollOncePaginatesAndCheckpoints(t *testing.T) {
	client := &fakeClient{pages: [][]searchHit{
		{hit("logs-1", "a", "2026-06-17T10:00:00.000Z"), hit("logs-1", "b", "2026-06-17T10:00:01.000Z")},
		{hit("logs-1", "c", "2026-06-17T10:00:02.000Z")},
	}}
	sink := new(consumertest.LogsSink)
	store := newMemStorage()
	r := newTestReceiver(t, client, sink, store)

	r.pollOnce(context.Background())

	// 3 records consumed across two pages; third page is short so polling stops.
	assert.Equal(t, 3, sink.LogRecordCount())

	// cursor advanced to the last document and was persisted.
	assert.Equal(t, []any{"2026-06-17T10:00:02.000Z", "c"}, r.cursor)
	persisted, err := newCursorPersister(store).Load(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []any{"2026-06-17T10:00:02.000Z", "c"}, persisted)

	// second request must carry search_after from the first page's last hit.
	require.Len(t, client.calls, 2)
	assert.Nil(t, client.calls[0].SearchAfter)
	assert.Equal(t, []any{"2026-06-17T10:00:01.000Z", "b"}, client.calls[1].SearchAfter)
}

func TestPollOnceEmpty(t *testing.T) {
	client := &fakeClient{pages: [][]searchHit{{}}}
	sink := new(consumertest.LogsSink)
	r := newTestReceiver(t, client, sink, newMemStorage())

	r.pollOnce(context.Background())
	assert.Equal(t, 0, sink.LogRecordCount())
	assert.Nil(t, r.cursor)
}

func TestPollOnceConsumerError(t *testing.T) {
	client := &fakeClient{pages: [][]searchHit{
		{hit("logs-1", "a", "2026-06-17T10:00:00.000Z"), hit("logs-1", "b", "2026-06-17T10:00:01.000Z")},
	}}
	sink := consumertest.NewErr(errors.New("downstream boom"))
	store := newMemStorage()
	r := newTestReceiver(t, client, sink, store)

	r.pollOnce(context.Background())

	// cursor must NOT advance when the consumer rejects the batch.
	assert.Nil(t, r.cursor)
	_, ok := store.data[cursorStorageKey]
	assert.False(t, ok)
}

func TestStartResumesFromPersistedCursor(t *testing.T) {
	store := newMemStorage()
	// Pre-seed a persisted cursor.
	require.NoError(t, newCursorPersister(store).Save(context.Background(), []any{"2026-06-17T09:59:59.000Z", "z"}))

	client := &fakeClient{pages: [][]searchHit{{}}}
	sink := new(consumertest.LogsSink)
	cfg := validConfig()
	cfg.InitialDelay = 0
	cfg.PollInterval = time.Hour // avoid a second tick during the test
	r := newLogsReceiver(receivertest.NewNopSettings(metadata.Type), cfg, sink)
	r.client = client
	r.persister = newCursorPersister(store)

	// Mimic the cursor-load portion of Start without a real host/extension.
	cursor, err := r.persister.Load(context.Background())
	require.NoError(t, err)
	r.cursor = cursor

	r.pollOnce(context.Background())
	require.Len(t, client.calls, 1)
	assert.Equal(t, []any{"2026-06-17T09:59:59.000Z", "z"}, client.calls[0].SearchAfter)
}

func TestConvertHits(t *testing.T) {
	cfg := validConfig()
	r := newLogsReceiver(receivertest.NewNopSettings(metadata.Type), cfg, consumertest.NewNop())

	logs, lastSort := r.convertHits([]searchHit{
		hit("logs-1", "a", "2026-06-17T10:00:00.000Z"),
		hit("logs-1", "b", "2026-06-17T10:00:01.000Z"),
	})

	require.Equal(t, 2, logs.LogRecordCount())
	rl := logs.ResourceLogs().At(0)
	lr := rl.ScopeLogs().At(0).LogRecords().At(0)
	assert.Equal(t, "m-a", lr.Body().Map().AsRaw()["message"])
	idAttr, ok := lr.Attributes().Get("elasticsearch.id")
	require.True(t, ok)
	assert.Equal(t, "a", idAttr.Str())
	expectedTS, _ := time.Parse(time.RFC3339Nano, "2026-06-17T10:00:00.000Z")
	assert.Equal(t, pcommon.NewTimestampFromTime(expectedTS), lr.Timestamp())

	assert.Equal(t, []any{"2026-06-17T10:00:01.000Z", "b"}, lastSort)
}

func TestParseTimestamp(t *testing.T) {
	cases := []struct {
		name  string
		in    any
		valid bool
	}{
		{"rfc3339 millis Z", "2026-06-17T10:15:23.123Z", true},
		{"rfc3339 offset", "2026-06-17T10:15:23+02:00", true},
		{"epoch millis", float64(1_750_000_000_000), true},
		{"garbage", "not-a-time", false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := parseTimestamp(tc.in)
			assert.Equal(t, tc.valid, ok)
		})
	}
}
