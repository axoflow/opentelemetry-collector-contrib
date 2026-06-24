// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearchlogsreceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/elasticsearchlogsreceiver"

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/receiver"
	"go.opentelemetry.io/collector/receiver/receiverhelper"
	"go.uber.org/zap"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/elasticsearchlogsreceiver/internal/metadata"
)

// dataFormat is reported in receiver observability metrics.
const dataFormat = "elasticsearch"

// timestampLayouts are the formats tried, in order, when parsing the configured timestamp field.
var timestampLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05.000Z0700",
	"2006-01-02T15:04:05Z0700",
}

type logsReceiver struct {
	cfg      *Config
	settings receiver.Settings
	consumer consumer.Logs
	logger   *zap.Logger

	client    esLogsClient
	persister *cursorPersister
	obsrecv   *receiverhelper.ObsReport

	// lowerBound is the inclusive start time applied on a fresh start; the zero value means no bound.
	// It is set once in Start and read-only afterwards.
	lowerBound time.Time

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func newLogsReceiver(settings receiver.Settings, cfg *Config, consumer consumer.Logs) *logsReceiver {
	obsrecv, err := receiverhelper.NewObsReport(receiverhelper.ObsReportSettings{
		ReceiverID:             settings.ID,
		Transport:              "http",
		ReceiverCreateSettings: settings,
	})
	if err != nil {
		settings.Logger.Warn("failed to create obsreport; receiver metrics will be unavailable", zap.Error(err))
	}
	return &logsReceiver{
		cfg:      cfg,
		settings: settings,
		consumer: consumer,
		logger:   settings.Logger,
		obsrecv:  obsrecv,
	}
}

func (r *logsReceiver) Start(ctx context.Context, host component.Host) error {
	client, err := newESLogsClient(ctx, r.settings.TelemetrySettings, r.cfg, host)
	if err != nil {
		return err
	}
	r.client = client

	storageClient, err := getStorageClient(ctx, host, r.cfg.StorageID, r.settings.ID)
	if err != nil {
		return err
	}
	r.persister = newCursorPersister(storageClient)

	// Load each index pattern's cursor up front so a storage error fails Start (fail fast). Each
	// cursor is then owned by that index's goroutine, so no shared state needs synchronization.
	cursors := make(map[string][]any, len(r.cfg.Indices))
	for _, index := range r.cfg.Indices {
		cursor, err := r.persister.Load(ctx, index)
		if err != nil {
			return err
		}
		cursors[index] = cursor
		if cursor != nil {
			r.logger.Info("resuming from persisted cursor",
				zap.String("index", index), zap.Any("search_after", cursor))
		}
	}

	if r.cfg.StartAt == startAtEnd {
		// On a fresh start, indexes without a cursor only read documents at or after
		// now - initial_lookback. buildQuery applies this per index.
		r.lowerBound = time.Now().Add(-r.cfg.InitialLookback)
		if r.cfg.InitialLookback == 0 {
			r.logger.Warn("start_at is 'end' with initial_lookback=0; documents whose timestamp " +
				"precedes startup but that are indexed afterwards (indexing lag or client/Elasticsearch " +
				"clock skew) will be missed. Set a non-zero initial_lookback to cover that window.")
		}
	}

	// A descending sort pages from newest to oldest, so once history is drained the cursor sits at
	// the oldest document and documents ingested later (which sort higher) fall before the cursor and
	// are never retrieved. Ascending sort is required to continuously tail newly ingested logs.
	if r.hasDescendingSort() {
		r.logger.Warn("'sort' uses descending order; the receiver will not pick up documents ingested " +
			"after it catches up. Use ascending order on a monotonically increasing field (e.g. the " +
			"timestamp plus a unique tiebreaker) to continuously tail new logs.")
	}

	pollCtx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	// One goroutine per index pattern so a slow or large-backlog index does not delay the others.
	for _, index := range r.cfg.Indices {
		r.wg.Add(1)
		go r.runIndex(pollCtx, index, cursors[index])
	}
	return nil
}

func (r *logsReceiver) Shutdown(ctx context.Context) error {
	if r.cancel != nil {
		r.cancel()
	}
	r.wg.Wait()
	if r.persister != nil {
		return r.persister.Close(ctx)
	}
	return nil
}

// runIndex polls a single index pattern on its own ticker, carrying that index's search_after cursor
// in a local variable so it never shares mutable state with other indexes.
func (r *logsReceiver) runIndex(ctx context.Context, index string, cursor []any) {
	defer r.wg.Done()

	if r.cfg.InitialDelay > 0 {
		timer := time.NewTimer(r.cfg.InitialDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}

	cursor = r.pollIndex(ctx, index, cursor)

	ticker := time.NewTicker(r.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cursor = r.pollIndex(ctx, index, cursor)
		}
	}
}

// pollIndex paginates one index pattern with search_after starting from cursor until it is exhausted
// (or batch_limit is reached), checkpointing after each consumed page, and returns the advanced
// cursor. An error reading or consuming one index does not affect the others; the next poll retries
// from the returned (last persisted) cursor.
func (r *logsReceiver) pollIndex(ctx context.Context, index string, cursor []any) []any {
	emitted := 0
	for {
		if ctx.Err() != nil {
			return cursor
		}

		// Cap the requested page size so a cycle never fetches more than batch_limit documents.
		size := r.cfg.PageSize
		if r.cfg.BatchLimit > 0 {
			remaining := r.cfg.BatchLimit - emitted
			if remaining <= 0 {
				return cursor
			}
			if remaining < size {
				size = remaining
			}
		}

		req := searchRequest{
			Size:        size,
			Query:       r.buildQuery(cursor),
			Sort:        r.cfg.Sort,
			SearchAfter: cursor,
		}

		resp, err := r.search(ctx, index, req)
		if err != nil {
			r.logger.Error("failed to query Elasticsearch", zap.String("index", index), zap.Error(err))
			return cursor
		}

		hits := resp.Hits.Hits
		if len(hits) == 0 {
			return cursor
		}

		// The next cursor is the sort values of the last document of the page. If it is missing we
		// cannot paginate deterministically, so refuse to deliver this page (delivering it would
		// re-deliver the same documents on every poll, since the cursor could not advance).
		nextCursor := hits[len(hits)-1].Sort
		if len(nextCursor) == 0 {
			r.logger.Error("last document is missing sort values; cannot advance search_after, check the 'sort' configuration",
				zap.String("index", index))
			return cursor
		}

		logs := r.convertHits(hits)
		if err := r.consume(ctx, logs, len(hits)); err != nil {
			r.logger.Error("failed to consume logs; will retry from last cursor",
				zap.String("index", index), zap.Error(err))
			return cursor
		}

		cursor = nextCursor
		emitted += len(hits)
		if err := r.persister.Save(ctx, index, cursor); err != nil {
			r.logger.Warn("failed to persist cursor; progress may be lost on restart",
				zap.String("index", index), zap.Error(err))
		}

		r.logger.Debug("emitted page of logs",
			zap.String("index", index),
			zap.Int("count", len(hits)),
			zap.Any("cursor", cursor))

		// A short page (fewer hits than requested) means we have caught up; wait for the next tick.
		if len(hits) < size {
			return cursor
		}

		// Reached the per-cycle cap; resume from the persisted cursor on the next poll.
		if r.cfg.BatchLimit > 0 && emitted >= r.cfg.BatchLimit {
			r.logger.Debug("reached batch_limit; pausing index until next poll",
				zap.String("index", index), zap.Int("emitted", emitted))
			return cursor
		}
	}
}

// search issues one _search request, bounding it with the configured client timeout so a single
// hung request cannot stall an index's poll loop. The request also honors ctx cancellation, so
// Shutdown unblocks an in-flight request promptly.
func (r *logsReceiver) search(ctx context.Context, index string, req searchRequest) (*searchResponse, error) {
	if r.cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.cfg.Timeout)
		defer cancel()
	}
	return r.client.Search(ctx, index, req)
}

// consume pushes a page of logs to the next consumer, recording receiver observability metrics
// (accepted/refused log records) around the call.
func (r *logsReceiver) consume(ctx context.Context, logs plog.Logs, numRecords int) error {
	if r.obsrecv == nil {
		return r.consumer.ConsumeLogs(ctx, logs)
	}
	obsCtx := r.obsrecv.StartLogsOp(ctx)
	err := r.consumer.ConsumeLogs(obsCtx, logs)
	r.obsrecv.EndLogsOp(obsCtx, dataFormat, numRecords, err)
	return err
}

// hasDescendingSort reports whether any configured sort field uses descending order.
func (r *logsReceiver) hasDescendingSort() bool {
	for _, s := range r.cfg.Sort {
		for _, order := range s {
			if order == "desc" {
				return true
			}
		}
	}
	return false
}

// buildQuery assembles the Elasticsearch query. Once the index has a cursor, search_after carries
// the position so only the user-provided filter is sent; on a fresh start (nil cursor) the configured
// lower time bound is ANDed with the user filter.
func (r *logsReceiver) buildQuery(cursor []any) map[string]any {
	if cursor != nil || r.lowerBound.IsZero() {
		return r.cfg.Query
	}

	filters := []any{
		map[string]any{
			"range": map[string]any{
				r.cfg.TimestampField: map[string]any{
					"gte": r.lowerBound.UTC().Format(time.RFC3339Nano),
				},
			},
		},
	}
	if len(r.cfg.Query) > 0 {
		filters = append(filters, r.cfg.Query)
	}
	return map[string]any{
		"bool": map[string]any{
			"filter": filters,
		},
	}
}

func (r *logsReceiver) convertHits(hits []searchHit) plog.Logs {
	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()
	sl := rl.ScopeLogs().AppendEmpty()
	sl.Scope().SetName(metadata.ScopeName)

	now := pcommon.NewTimestampFromTime(time.Now())

	for _, h := range hits {
		lr := sl.LogRecords().AppendEmpty()
		lr.SetObservedTimestamp(now)
		if ts, ok := parseTimestamp(h.Source[r.cfg.TimestampField]); ok {
			lr.SetTimestamp(pcommon.NewTimestampFromTime(ts))
		}
		if err := lr.Body().SetEmptyMap().FromRaw(h.Source); err != nil {
			r.logger.Debug("failed to set log body from _source", zap.Error(err))
		}
		if h.Index != "" {
			lr.Attributes().PutStr("elasticsearch.index", h.Index)
		}
		if h.ID != "" {
			lr.Attributes().PutStr("elasticsearch.id", h.ID)
		}
	}

	return logs
}

func parseTimestamp(v any) (time.Time, bool) {
	switch t := v.(type) {
	case string:
		for _, layout := range timestampLayouts {
			if parsed, err := time.Parse(layout, t); err == nil {
				return parsed, true
			}
		}
	case float64:
		// Elasticsearch commonly emits date sort/source values as epoch milliseconds.
		sec := int64(t) / 1000
		nsec := (int64(t) % 1000) * int64(time.Millisecond)
		return time.Unix(sec, nsec).UTC(), true
	}
	return time.Time{}, false
}
