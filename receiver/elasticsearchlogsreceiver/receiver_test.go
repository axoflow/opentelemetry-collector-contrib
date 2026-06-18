// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearchlogsreceiver

import (
	"context"
	"errors"
	"reflect"
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

type searchCall struct {
	index string
	req   searchRequest
}

// fakeClient returns canned pages of hits per index, one page per Search call for that index.
type fakeClient struct {
	pages map[string][][]searchHit
	idx   map[string]int
	calls []searchCall
	err   error
}

func newFakeClient(pages map[string][][]searchHit) *fakeClient {
	return &fakeClient{pages: pages, idx: map[string]int{}}
}

func (f *fakeClient) Search(_ context.Context, index string, req searchRequest) (*searchResponse, error) {
	f.calls = append(f.calls, searchCall{index: index, req: req})
	if f.err != nil {
		return nil, f.err
	}
	resp := &searchResponse{}
	pages := f.pages[index]
	if i := f.idx[index]; i < len(pages) {
		resp.Hits.Hits = pages[i]
		f.idx[index] = i + 1
	}
	return resp, nil
}

// callsFor returns the requests issued against a given index, in order.
func (f *fakeClient) callsFor(index string) []searchRequest {
	var reqs []searchRequest
	for _, c := range f.calls {
		if c.index == index {
			reqs = append(reqs, c.req)
		}
	}
	return reqs
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
	client := newFakeClient(map[string][][]searchHit{
		"logs-*": {
			{hit("logs-1", "a", "2026-06-17T10:00:00.000Z"), hit("logs-1", "b", "2026-06-17T10:00:01.000Z")},
			{hit("logs-1", "c", "2026-06-17T10:00:02.000Z")},
		},
	})
	sink := new(consumertest.LogsSink)
	store := newMemStorage()
	r := newTestReceiver(t, client, sink, store)

	r.pollOnce(context.Background())

	// 3 records consumed across two pages; third page is short so polling stops.
	assert.Equal(t, 3, sink.LogRecordCount())

	// cursor advanced to the last document and was persisted under the index key.
	assert.Equal(t, []any{"2026-06-17T10:00:02.000Z", "c"}, r.cursors["logs-*"])
	persisted, err := newCursorPersister(store).Load(context.Background(), "logs-*")
	require.NoError(t, err)
	assert.Equal(t, []any{"2026-06-17T10:00:02.000Z", "c"}, persisted)

	// second request must carry search_after from the first page's last hit.
	calls := client.callsFor("logs-*")
	require.Len(t, calls, 2)
	assert.Nil(t, calls[0].SearchAfter)
	assert.Equal(t, []any{"2026-06-17T10:00:01.000Z", "b"}, calls[1].SearchAfter)
}

func TestPollOnceEmpty(t *testing.T) {
	client := newFakeClient(map[string][][]searchHit{"logs-*": {{}}})
	sink := new(consumertest.LogsSink)
	r := newTestReceiver(t, client, sink, newMemStorage())

	r.pollOnce(context.Background())
	assert.Equal(t, 0, sink.LogRecordCount())
	assert.Empty(t, r.cursors)
}

func TestPollOnceConsumerError(t *testing.T) {
	client := newFakeClient(map[string][][]searchHit{
		"logs-*": {{hit("logs-1", "a", "2026-06-17T10:00:00.000Z"), hit("logs-1", "b", "2026-06-17T10:00:01.000Z")}},
	})
	sink := consumertest.NewErr(errors.New("downstream boom"))
	store := newMemStorage()
	r := newTestReceiver(t, client, sink, store)

	r.pollOnce(context.Background())

	// cursor must NOT advance when the consumer rejects the batch.
	assert.Empty(t, r.cursors)
	_, ok := store.data[cursorKey("logs-*")]
	assert.False(t, ok)
}

func TestStartResumesFromPersistedCursor(t *testing.T) {
	store := newMemStorage()
	// Pre-seed a persisted cursor for the configured index.
	require.NoError(t, newCursorPersister(store).Save(context.Background(), "logs-*", []any{"2026-06-17T09:59:59.000Z", "z"}))

	client := newFakeClient(map[string][][]searchHit{"logs-*": {{}}})
	sink := new(consumertest.LogsSink)
	cfg := validConfig()
	cfg.InitialDelay = 0
	cfg.PollInterval = time.Hour // avoid a second tick during the test
	r := newLogsReceiver(receivertest.NewNopSettings(metadata.Type), cfg, sink)
	r.client = client
	r.persister = newCursorPersister(store)

	// Mimic the cursor-load portion of Start without a real host/extension.
	cursor, err := r.persister.Load(context.Background(), "logs-*")
	require.NoError(t, err)
	r.cursors["logs-*"] = cursor

	r.pollOnce(context.Background())
	calls := client.callsFor("logs-*")
	require.Len(t, calls, 1)
	assert.Equal(t, []any{"2026-06-17T09:59:59.000Z", "z"}, calls[0].SearchAfter)
}

// listClient models Elasticsearch: it serves up to req.Size hits after the search_after cursor from a
// flat ordered list, so it honors the requested page size (unlike fakeClient's fixed pages).
type listClient struct {
	docs  []searchHit
	calls []searchCall
}

func (c *listClient) Search(_ context.Context, index string, req searchRequest) (*searchResponse, error) {
	c.calls = append(c.calls, searchCall{index: index, req: req})
	start := 0
	if req.SearchAfter != nil {
		for i, d := range c.docs {
			if reflect.DeepEqual(d.Sort, req.SearchAfter) {
				start = i + 1
				break
			}
		}
	}
	end := start + req.Size
	if end > len(c.docs) {
		end = len(c.docs)
	}
	resp := &searchResponse{}
	resp.Hits.Hits = c.docs[start:end]
	return resp, nil
}

func TestPollOnceBatchLimit(t *testing.T) {
	docs := make([]searchHit, 5)
	for i := range docs {
		ts := "2026-06-17T10:00:0" + string(rune('0'+i)) + ".000Z"
		docs[i] = hit("logs-1", string(rune('a'+i)), ts)
	}
	client := &listClient{docs: docs}
	sink := new(consumertest.LogsSink)
	store := newMemStorage()
	cfg := validConfig()
	cfg.Indices = []string{"logs-1"}
	cfg.PageSize = 2
	cfg.BatchLimit = 3
	r := newLogsReceiver(receivertest.NewNopSettings(metadata.Type), cfg, sink)
	r.client = client
	r.persister = newCursorPersister(store)

	// First cycle: fetch at most batch_limit (3) documents, never overshooting.
	r.pollOnce(context.Background())
	assert.Equal(t, 3, sink.LogRecordCount())
	for _, c := range client.calls {
		assert.LessOrEqual(t, c.req.Size, 2, "request size must never exceed page_size")
	}
	// Requested sizes were capped by remaining budget: 2 then 1.
	require.Len(t, client.calls, 2)
	assert.Equal(t, 2, client.calls[0].req.Size)
	assert.Equal(t, 1, client.calls[1].req.Size)
	assert.Equal(t, docs[2].Sort, r.cursors["logs-1"])

	// Second cycle: picks up where it left off and drains the remaining 2.
	r.pollOnce(context.Background())
	assert.Equal(t, 5, sink.LogRecordCount())
	assert.Equal(t, docs[4].Sort, r.cursors["logs-1"])
}

func TestPollOncePerIndexCursorsAreIndependent(t *testing.T) {
	client := newFakeClient(map[string][][]searchHit{
		"logs-a": {{hit("logs-a", "a1", "2026-06-17T10:00:00.000Z")}},
		"logs-b": {{hit("logs-b", "b1", "2026-06-17T11:00:00.000Z"), hit("logs-b", "b2", "2026-06-17T11:00:01.000Z")}},
	})
	sink := new(consumertest.LogsSink)
	store := newMemStorage()
	cfg := validConfig()
	cfg.PageSize = 10
	cfg.Indices = []string{"logs-a", "logs-b"}
	r := newLogsReceiver(receivertest.NewNopSettings(metadata.Type), cfg, sink)
	r.client = client
	r.persister = newCursorPersister(store)

	r.pollOnce(context.Background())

	// Each index advanced to its own last document, checkpointed under its own key.
	assert.Equal(t, []any{"2026-06-17T10:00:00.000Z", "a1"}, r.cursors["logs-a"])
	assert.Equal(t, []any{"2026-06-17T11:00:01.000Z", "b2"}, r.cursors["logs-b"])

	a, err := newCursorPersister(store).Load(context.Background(), "logs-a")
	require.NoError(t, err)
	assert.Equal(t, []any{"2026-06-17T10:00:00.000Z", "a1"}, a)
	b, err := newCursorPersister(store).Load(context.Background(), "logs-b")
	require.NoError(t, err)
	assert.Equal(t, []any{"2026-06-17T11:00:01.000Z", "b2"}, b)

	assert.Equal(t, 3, sink.LogRecordCount())
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

func TestHasDescendingSort(t *testing.T) {
	cases := []struct {
		name string
		sort []map[string]string
		want bool
	}{
		{"all asc", []map[string]string{{"@timestamp": "asc"}, {"seq": "asc"}}, false},
		{"tiebreaker desc", []map[string]string{{"@timestamp": "asc"}, {"seq": "desc"}}, true},
		{"primary desc", []map[string]string{{"@timestamp": "desc"}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Sort = tc.sort
			r := newLogsReceiver(receivertest.NewNopSettings(metadata.Type), cfg, consumertest.NewNop())
			assert.Equal(t, tc.want, r.hasDescendingSort())
		})
	}
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
