// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package idirareceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/idirareceiver"

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/extension/xextension/storage"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/receiver"
	"go.uber.org/zap"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/idirareceiver/internal/metadata"
)

// maxPagesPerPoll bounds a single poll so a misbehaving API cannot page forever.
const maxPagesPerPoll = 10000

const checkpointKey = "last_query_end"

// nowFunc is overridden in tests.
var nowFunc = time.Now

type idiraReceiver struct {
	cfg      *Config
	settings receiver.Settings
	consumer consumer.Logs

	client  *client
	storage storage.Client
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	// lastEnd is the end of the last consumed window, owned by the polling goroutine. The
	// storage extension, when configured, only carries it across restarts.
	lastEnd time.Time
}

func newReceiver(cfg *Config, settings receiver.Settings, consumer consumer.Logs) *idiraReceiver {
	return &idiraReceiver{cfg: cfg, settings: settings, consumer: consumer}
}

func (r *idiraReceiver) Start(ctx context.Context, host component.Host) error {
	httpClient, err := r.cfg.ToClient(ctx, host.GetExtensions(), r.settings.TelemetrySettings)
	if err != nil {
		return fmt.Errorf("failed to create HTTP client: %w", err)
	}

	pollCtx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel

	// The token source refreshes on its own schedule, so it cannot hold Start's context. It gets
	// the polling context instead, so that shutdown also aborts an in-flight token request. It
	// must also fetch tokens with its own client: sharing the authenticated one would make every
	// token request try to authenticate itself.
	tokenClient := &http.Client{Transport: httpClient.Transport, Timeout: httpClient.Timeout}
	tokenCtx := context.WithValue(pollCtx, oauth2.HTTPClient, tokenClient)
	tokenSource := (&clientcredentials.Config{
		ClientID:     r.cfg.ClientID,
		ClientSecret: string(r.cfg.ClientSecret),
		TokenURL:     r.cfg.TokenURL,
		Scopes:       r.cfg.Scopes,
	}).TokenSource(tokenCtx)
	httpClient.Transport = &oauth2.Transport{Source: tokenSource, Base: tokenClient.Transport}

	r.client = &client{httpClient: httpClient, endpoint: r.cfg.Endpoint, apiKey: string(r.cfg.APIKey)}

	if r.storage, err = getStorageClient(ctx, host, r.cfg.StorageID, r.settings.ID); err != nil {
		cancel()
		return err
	}

	r.wg.Add(1)
	go r.startPolling(pollCtx)
	return nil
}

func (r *idiraReceiver) Shutdown(ctx context.Context) error {
	if r.cancel != nil {
		r.cancel()
	}
	r.wg.Wait()
	if r.storage != nil {
		return r.storage.Close(ctx)
	}
	return nil
}

func (r *idiraReceiver) startPolling(ctx context.Context) {
	defer r.wg.Done()

	ticker := time.NewTicker(r.cfg.PollInterval)
	defer ticker.Stop()
	for {
		if err := r.poll(ctx); err != nil && ctx.Err() == nil {
			r.settings.Logger.Error("failed to poll for audit events", zap.Error(err))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// poll opens one query covering everything since the last successful poll and drains its pages.
func (r *idiraReceiver) poll(ctx context.Context) error {
	to := nowFunc().UTC().Truncate(time.Second)
	from, err := r.checkpoint(ctx, to)
	if err != nil {
		return err
	}
	if !to.After(from) {
		return nil
	}

	cursor, err := r.client.createQuery(ctx, from, to, r.cfg.PageSize, r.cfg.ApplicationCodes)
	if err != nil {
		return err
	}

	for range maxPagesPerPoll {
		resp, err := r.client.results(ctx, cursor)
		if err != nil {
			return err
		}
		if len(resp.Data) == 0 {
			// The query end is only checkpointed once its pages are consumed, so a failure
			// mid-drain re-queries the same window on the next poll.
			return r.setCheckpoint(ctx, to)
		}
		if err := r.consumer.ConsumeLogs(ctx, r.toLogs(resp.Data)); err != nil {
			return err
		}
		next := resp.Paging.Cursor.CursorRef
		if next == "" || next == cursor {
			return r.setCheckpoint(ctx, to)
		}
		cursor = next
	}

	r.settings.Logger.Warn("stopped draining pages at the per-poll limit; the window will be re-queried",
		zap.Int("max_pages", maxPagesPerPoll))
	return nil
}

func (r *idiraReceiver) toLogs(events []map[string]any) plog.Logs {
	logs := plog.NewLogs()
	sl := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty()
	sl.Scope().SetName(metadata.ScopeName)
	observed := pcommon.NewTimestampFromTime(time.Now())

	for _, event := range events {
		lr := sl.LogRecords().AppendEmpty()
		lr.SetObservedTimestamp(observed)
		// timestamp is Unix milliseconds; absent or malformed leaves the record timestamp unset.
		if ms, ok := event["timestamp"].(float64); ok {
			lr.SetTimestamp(pcommon.NewTimestampFromTime(time.UnixMilli(int64(ms))))
		}
		if err := lr.Body().SetEmptyMap().FromRaw(event); err != nil {
			r.settings.Logger.Warn("failed to convert audit event to a log body", zap.Error(err))
		}
	}
	return logs
}

// checkpoint returns the start of the window to query, defaulting to now minus initial_lookback.
func (r *idiraReceiver) checkpoint(ctx context.Context, now time.Time) (time.Time, error) {
	if !r.lastEnd.IsZero() {
		return r.lastEnd, nil
	}

	data, err := r.storage.Get(ctx, checkpointKey)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to read checkpoint: %w", err)
	}
	if len(data) == 0 {
		return now.Add(-r.cfg.InitialLookback), nil
	}
	from, err := time.Parse(time.RFC3339, string(data))
	if err != nil {
		r.settings.Logger.Warn("ignoring unparsable checkpoint", zap.String("checkpoint", string(data)), zap.Error(err))
		return now.Add(-r.cfg.InitialLookback), nil
	}
	return from, nil
}

// setCheckpoint stores the end of the window just consumed. It is reused verbatim as the next
// window's start: the date filter has second granularity, so re-reading that second is the only
// way not to drop sub-second events, at the cost of duplicating the events on the boundary.
func (r *idiraReceiver) setCheckpoint(ctx context.Context, to time.Time) error {
	r.lastEnd = to
	return r.storage.Set(ctx, checkpointKey, []byte(to.Format(time.RFC3339)))
}

func getStorageClient(ctx context.Context, host component.Host, storageID *component.ID, componentID component.ID) (storage.Client, error) {
	if storageID == nil {
		return storage.NewNopClient(), nil
	}
	ext, ok := host.GetExtensions()[*storageID]
	if !ok {
		return nil, fmt.Errorf("storage extension %q not found", storageID)
	}
	storageExt, ok := ext.(storage.Extension)
	if !ok {
		return nil, fmt.Errorf("non-storage extension %q found", storageID)
	}
	return storageExt.GetClient(ctx, component.KindReceiver, componentID, "")
}
