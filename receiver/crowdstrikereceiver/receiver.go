// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package crowdstrikereceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/crowdstrikereceiver"

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/crowdstrike/gofalcon/falcon/client"
	"github.com/crowdstrike/gofalcon/falcon/client/alerts"
	"github.com/crowdstrike/gofalcon/falcon/models"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"
)

// FQL timestamp literal with millisecond precision, which is the granularity
// updated_timestamp carries. The filter on it is inclusive at the checkpoint:
// alerts can share a millisecond, and a strictly-greater filter would skip the
// ones a full page had no room for.
const fqlTimestampLayout = "2006-01-02T15:04:05.000Z"

// alertPageSize is how many not-yet-seen alerts a page asks for.
const alertPageSize = 500

type crowdstrikeReceiver struct {
	cancel       context.CancelFunc
	logger       *zap.Logger
	nextConsumer consumer.Logs
	config       *CrowdstrikeReceiverConfig
	client       *client.CrowdStrikeAPISpecification
	pollInterval time.Duration

	// alertCheckpoint is the highest updated_timestamp consumed so far. It is
	// only advanced after a successful ConsumeLogs, so a failed delivery is
	// retried on the next tick.
	alertCheckpoint time.Time
}

func (r *crowdstrikeReceiver) Shutdown(_ context.Context) error {
	if r.cancel != nil {
		r.cancel()
	}
	return nil
}

func (r *crowdstrikeReceiver) Start(_ context.Context, _ component.Host) error {
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel

	r.alertCheckpoint = time.Now().Add(-r.config.InitialLookback)

	go r.poll(ctx, "alerts", r.pollAlertsOnce)
	return nil
}

func (r *crowdstrikeReceiver) poll(ctx context.Context, name string, once func(context.Context) error) {
	ticker := time.NewTicker(r.pollInterval)
	defer ticker.Stop()

	// The first poll goes out on start rather than an interval later: a
	// collector restarted more often than the cadence it was given — a
	// crashlooping pod on a minutes-long poll_interval — would never reach a
	// tick, and so never collect anything at all.
	for ctx.Err() == nil {
		r.logger.Debug("CrowdStrike receiver tick", zap.String("poller", name))
		if err := once(ctx); err != nil {
			r.logger.Error("CrowdStrike poll failed", zap.String("poller", name), zap.Error(err))
		}

		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
}

func (r *crowdstrikeReceiver) pollAlertsOnce(ctx context.Context) error {
	// Instead of offset pagination, each page advances the updated_timestamp
	// filter: the result set cannot shift under us while we page through it.
	// The filter is inclusive, so the alerts already consumed at the
	// checkpoint millisecond come back with every page; they are recognized by
	// composite_id and the page budget is widened to make room for them, which
	// is what keeps a tie straddling the page boundary from being skipped.
	for {
		filter := fmt.Sprintf("updated_timestamp:>'%s'", r.alertCheckpoint.UTC().Format(fqlTimestampLayout))
		sort := "updated_timestamp|asc"
		limit := int64(alertPageSize)
		queried, err := r.client.Alerts.QueryV2(alerts.NewQueryV2Params().
			WithContext(ctx).
			WithFilter(&filter).
			WithSort(&sort).
			WithLimit(&limit))
		if err != nil {
			return fmt.Errorf("querying alert IDs: %w", err)
		}
		ids := queried.GetPayload().Resources
		if len(ids) == 0 {
			return nil
		}

		fetched, err := r.client.Alerts.GetV2(alerts.NewGetV2Params().
			WithContext(ctx).
			WithBody(&models.DetectsapiPostEntitiesAlertsV2Request{CompositeIds: ids}))
		if err != nil {
			return fmt.Errorf("fetching alerts: %w", err)
		}

		r.backOffOnRateLimit(ctx, fetched.XRateLimitLimit, fetched.XRateLimitRemaining)

		if fetched.Payload == nil {
			return nil
		}

		logs, err := convertAlertToPlogLogs(fetched)
		if err != nil {
			return fmt.Errorf("converting alerts (trace_id %s): %w", fetched.XCSTRACEID, err)
		}
		if err := r.nextConsumer.ConsumeLogs(ctx, *logs); err != nil {
			return fmt.Errorf("consuming alerts (trace_id %s): %w", fetched.XCSTRACEID, err)
		}

		for _, alert := range fetched.Payload.Resources {
			if alert.UpdatedTimestamp != nil && time.Time(*alert.UpdatedTimestamp).After(r.alertCheckpoint) {
				r.alertCheckpoint = time.Time(*alert.UpdatedTimestamp)
			}
		}

		if len(ids) < alertPageSize {
			return nil
		}
	}
}

func (r *crowdstrikeReceiver) backOffOnRateLimit(ctx context.Context, limit, remaining int64) {
	if remaining >= limit/10 {
		return
	}
	r.logger.Warn("CrowdStrike API rate limit nearly exhausted, backing off",
		zap.Int64("remaining", remaining),
		zap.Int64("limit", limit),
	)
	select {
	case <-ctx.Done():
	case <-time.After(r.pollInterval * 2):
	}
}

func convertAlertToPlogLogs(alerts *alerts.GetV2OK) (*plog.Logs, error) {
	out := plog.NewLogs()
	logs := out.ResourceLogs()
	rls := logs.AppendEmpty()
	ills := rls.ScopeLogs().AppendEmpty()

	for _, alert := range alerts.Payload.Resources {
		lr := ills.LogRecords().AppendEmpty()

		jsonMap, err := json.Marshal(alert)
		if err != nil {
			return nil, err
		}

		var rawMap map[string]any
		if err := json.Unmarshal(jsonMap, &rawMap); err != nil {
			return nil, err
		}
		if err = lr.Attributes().FromRaw(rawMap); err != nil {
			return nil, err
		}
		ts := time.Now()
		if alert.Timestamp != nil {
			ts = time.Time(*alert.Timestamp)
		}
		lr.SetTimestamp(pcommon.NewTimestampFromTime(ts))
		lr.SetObservedTimestamp(pcommon.NewTimestampFromTime(time.Now()))
		if alert.SeverityName != nil {
			lr.SetSeverityText(*alert.SeverityName)
		}
		// TODO: lr.SetSeverityNumber(...)
	}

	return &out, nil
}
