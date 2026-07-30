// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package tenableauditlogreceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/tenableauditlogreceiver"

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/extension/xextension/storage"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/receiver"
	"go.uber.org/zap"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/tenableauditlogreceiver/internal/metadata"
)

const (
	auditLogPath  = "/audit-log/v1/events"
	checkpointKey = "checkpoint"
	// maxPagesPerPoll guards against a pagination token that never terminates.
	maxPagesPerPoll = 100
)

// auditLogResponse is the subset of the audit log API response the receiver relies on.
// Events are kept as generic maps so that every field Tenable returns ends up in the log body.
type auditLogResponse struct {
	Events     []map[string]any `json:"events"`
	Pagination struct {
		Next string `json:"next"`
	} `json:"pagination"`
}

// checkpoint records how far the receiver has consumed the audit log.
type checkpoint struct {
	LastEventTime time.Time `json:"last_event_time"`
	// SeenIDs holds the ids of the events sharing LastEventTime. The query filter is inclusive of
	// LastEventTime, so that an event indexed after the poll that read the same instant is not
	// missed; the events already emitted at that instant are deduplicated by id instead.
	SeenIDs []string `json:"seen_ids"`
}

func (c *checkpoint) seen(id string) bool {
	return slices.Contains(c.SeenIDs, id)
}

func (c *checkpoint) advance(ts time.Time, id string) {
	switch {
	case ts.After(c.LastEventTime):
		c.LastEventTime = ts
		c.SeenIDs = nil
	case !ts.Equal(c.LastEventTime):
		return
	}
	if id != "" {
		c.SeenIDs = append(c.SeenIDs, id)
	}
}

type tenableReceiver struct {
	cfg      *Config
	settings receiver.Settings
	consumer consumer.Logs
	client   *http.Client
	storage  storage.Client
	cp       checkpoint
	// rateLimitedUntil holds the Retry-After deadline reported by the last rate limited request.
	// Only accessed from the polling goroutine.
	rateLimitedUntil time.Time
	cancel           context.CancelFunc
	wg               sync.WaitGroup
}

func newReceiver(cfg *Config, settings receiver.Settings, logs consumer.Logs) *tenableReceiver {
	return &tenableReceiver{cfg: cfg, settings: settings, consumer: logs}
}

func (r *tenableReceiver) Start(ctx context.Context, host component.Host) error {
	client, err := r.cfg.ToClient(ctx, host.GetExtensions(), r.settings.TelemetrySettings)
	if err != nil {
		return err
	}
	r.client = client

	if r.storage, err = getStorageClient(ctx, host, r.cfg.StorageID, r.settings.ID); err != nil {
		return err
	}
	r.loadCheckpoint(ctx)

	// The polling loop outlives Start's context.
	pollCtx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.wg.Add(1)
	go r.pollLoop(pollCtx)
	return nil
}

func (r *tenableReceiver) Shutdown(ctx context.Context) error {
	if r.cancel != nil {
		r.cancel()
	}
	r.wg.Wait()
	if r.storage != nil {
		return r.storage.Close(ctx)
	}
	return nil
}

func (r *tenableReceiver) pollLoop(ctx context.Context) {
	defer r.wg.Done()

	ticker := time.NewTicker(r.cfg.PollInterval)
	defer ticker.Stop()
	for {
		if err := r.poll(ctx); err != nil && ctx.Err() == nil {
			// A failed poll leaves the checkpoint untouched, so the next tick retries the
			// same window instead of skipping events.
			r.settings.Logger.Error("failed to poll audit log", zap.Error(err))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *tenableReceiver) poll(ctx context.Context) error {
	if wait := time.Until(r.rateLimitedUntil); wait > 0 {
		r.settings.Logger.Debug("skipping poll while rate limited", zap.Duration("retry_after", wait))
		return nil
	}

	since := r.cp.LastEventTime
	if since.IsZero() {
		since = time.Now().UTC().Add(-r.cfg.InitialLookback)
	}

	events, err := r.fetch(ctx, since)
	if err != nil {
		return err
	}

	logs := plog.NewLogs()
	scopeLogs := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty()
	scopeLogs.Scope().SetName(metadata.ScopeName)
	next := r.cp
	for _, event := range events {
		ts, hasTS := eventTime(event)
		id, _ := event["id"].(string)
		if hasTS && (ts.Before(r.cp.LastEventTime) || (ts.Equal(r.cp.LastEventTime) && r.cp.seen(id))) {
			continue
		}
		r.appendLogRecord(scopeLogs.LogRecords().AppendEmpty(), event, ts, hasTS)
		if hasTS {
			next.advance(ts, id)
		}
	}

	if logs.LogRecordCount() == 0 {
		return nil
	}
	if err := r.consumer.ConsumeLogs(ctx, logs); err != nil {
		return err
	}
	r.cp = next
	r.saveCheckpoint(ctx)
	return nil
}

func (r *tenableReceiver) fetch(ctx context.Context, since time.Time) ([]map[string]any, error) {
	var events []map[string]any
	// Cursor pagination starts at "0"; without the parameter the API falls back to offset
	// pagination and never reports a next cursor.
	nextToken := "0"
	for range maxPagesPerPoll {
		limit := min(r.cfg.PageSize, r.cfg.MaxRecordsPerPoll-len(events))
		if limit <= 0 {
			break
		}
		resp, err := r.request(ctx, since, limit, nextToken)
		if err != nil {
			return nil, err
		}
		if len(resp.Events) == 0 {
			break
		}
		events = append(events, resp.Events...)
		if nextToken = resp.Pagination.Next; nextToken == "" {
			break
		}
	}
	return events, nil
}

func (r *tenableReceiver) request(ctx context.Context, since time.Time, limit int, nextToken string) (*auditLogResponse, error) {
	query := url.Values{}
	// `received` carries milliseconds, and the date filter accepts them, so the checkpoint is sent
	// at full precision. Truncating to seconds would only widen the window with events the
	// checkpoint discards again.
	query.Set("f", "date.gte:"+since.UTC().Format("2006-01-02T15:04:05.000Z07:00"))
	// Events are not returned in time order by default, so a poll truncated by
	// max_records_per_poll would advance the checkpoint past events it never read.
	query.Set("sort", "received:asc")
	query.Set("limit", strconv.Itoa(limit))
	query.Set("next", nextToken)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.cfg.Endpoint+auditLogPath+"?"+query.Encode(), http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-ApiKeys", fmt.Sprintf("accessKey=%s;secretKey=%s", string(r.cfg.AccessKey), string(r.cfg.SecretKey)))
	req.Header.Set("Accept", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Tenable answers requests over the rate or concurrency limit with 429 and a Retry-After
	// header holding the number of seconds to wait.
	if resp.StatusCode == http.StatusTooManyRequests {
		delay := r.cfg.PollInterval
		if seconds, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && seconds > 0 {
			delay = time.Duration(seconds) * time.Second
		}
		r.rateLimitedUntil = time.Now().Add(delay)
		return nil, fmt.Errorf("audit log request rate limited, waiting %s before retrying", delay)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("audit log request failed with status %q: %s", resp.Status, body)
	}

	var out auditLogResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("failed to decode audit log response: %w", err)
	}
	return &out, nil
}

func (r *tenableReceiver) appendLogRecord(record plog.LogRecord, event map[string]any, ts time.Time, hasTS bool) {
	record.SetObservedTimestamp(pcommon.NewTimestampFromTime(time.Now()))
	if hasTS {
		record.SetTimestamp(pcommon.NewTimestampFromTime(ts))
	}
	if err := record.Body().SetEmptyMap().FromRaw(event); err != nil {
		r.settings.Logger.Warn("failed to set log body from audit event", zap.Error(err))
	}
}

// eventTime reports when Tenable recorded the event. Events without a usable timestamp are still
// emitted, but cannot move the checkpoint, so they may be re-emitted while inside the query window.
func eventTime(event map[string]any) (time.Time, bool) {
	received, ok := event["received"].(string)
	if !ok {
		return time.Time{}, false
	}
	ts, err := time.Parse(time.RFC3339, received)
	if err != nil {
		return time.Time{}, false
	}
	return ts, true
}

func (r *tenableReceiver) loadCheckpoint(ctx context.Context) {
	data, err := r.storage.Get(ctx, checkpointKey)
	if err != nil {
		r.settings.Logger.Warn("failed to load checkpoint, starting from initial_lookback", zap.Error(err))
		return
	}
	if len(data) == 0 {
		return
	}
	var cp checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		r.settings.Logger.Warn("failed to parse checkpoint, starting from initial_lookback", zap.Error(err))
		return
	}
	r.cp = cp
}

func (r *tenableReceiver) saveCheckpoint(ctx context.Context) {
	data, err := json.Marshal(r.cp)
	if err != nil {
		r.settings.Logger.Warn("failed to marshal checkpoint", zap.Error(err))
		return
	}
	if err := r.storage.Set(ctx, checkpointKey, data); err != nil {
		r.settings.Logger.Warn("failed to persist checkpoint", zap.Error(err))
	}
}

func getStorageClient(ctx context.Context, host component.Host, storageID *component.ID, componentID component.ID) (storage.Client, error) {
	if storageID == nil {
		return storage.NewNopClient(), nil
	}
	ext, ok := host.GetExtensions()[*storageID]
	if !ok {
		return nil, fmt.Errorf("storage extension '%s' not found", storageID)
	}
	storageExt, ok := ext.(storage.Extension)
	if !ok {
		return nil, fmt.Errorf("non-storage extension '%s' found", storageID)
	}
	return storageExt.GetClient(ctx, component.KindReceiver, componentID, "")
}
