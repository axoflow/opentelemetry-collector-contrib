// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package crowdstrikereceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/crowdstrikereceiver"

import (
	"context"
	"encoding/json"
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

type crowdstrikeReceiver struct {
	cancel       context.CancelFunc
	logger       *zap.Logger
	nextConsumer consumer.Logs
	config       *CrowdstrikeReceiverConfig
	client       *client.CrowdStrikeAPISpecification
	pollInterval time.Duration
}

func (r *crowdstrikeReceiver) Shutdown(_ context.Context) error {
	if r.cancel != nil {
		r.cancel()
	}
	return nil
}

func (r *crowdstrikeReceiver) Start(ctx context.Context, _ component.Host) error {
	ctx = context.Background()
	ctx, r.cancel = context.WithCancel(ctx)

	go func() {
		ticker := time.NewTicker(r.pollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.logger.Debug("CrowdStrike receiver tick")
				queries, err := r.client.Alerts.QueryV2(alerts.NewQueryV2Params())
				if err != nil {
					r.logger.Error("Error querying alerts from CrowdStrike", zap.Error(err))
					continue
				}
				paramsCompositeIDs := models.DetectsapiPostEntitiesAlertsV2Request{
					CompositeIds: queries.GetPayload().Resources,
				}

				alerts, err := r.client.Alerts.GetV2(alerts.NewGetV2Params().WithBody(&paramsCompositeIDs))
				if err != nil {
					r.logger.Error("Error fetching alerts from CrowdStrike", zap.Error(err))
					continue
				}

				// Log rate limit information
				r.logger.Debug("CrowdStrike API rate limits",
					zap.String("trace_id", alerts.XCSTRACEID),
					zap.Int64("rate_limit", alerts.XRateLimitLimit),
					zap.Int64("rate_limit_remaining", alerts.XRateLimitRemaining),
				)

				// Check rate limit and adjust polling if needed
				if alerts.XRateLimitRemaining < alerts.XRateLimitLimit/10 { // Less than 10% remaining
					r.logger.Warn("CrowdStrike API rate limit nearly exhausted, backing off",
						zap.Int64("remaining", alerts.XRateLimitRemaining),
						zap.Int64("limit", alerts.XRateLimitLimit),
					)
					// Temporarily slow down polling by waiting extra time
					backoffDuration := r.pollInterval * 2
					select {
					case <-ctx.Done():
						return
					case <-time.After(backoffDuration):
						// Continue after backoff
					}
				}

				// Check if we have alerts in the payload
				if alerts.Payload == nil {
					r.logger.Debug("No alerts returned from CrowdStrike")
					continue
				}

				logs, err := convertAlertToPlogLogs(alerts)
				if err != nil {
					r.logger.Error("Error converting alerts to plog.Logs",
						zap.Error(err),
						zap.String("trace_id", alerts.XCSTRACEID),
					)
					continue
				}

				if err = r.nextConsumer.ConsumeLogs(ctx, *logs); err != nil {
					r.logger.Error("Error consuming logs",
						zap.Error(err),
						zap.String("trace_id", alerts.XCSTRACEID),
					)
				}
			}
		}
	}()
	return nil
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
