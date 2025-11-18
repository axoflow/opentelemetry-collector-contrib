// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package crowdstrikereceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/crowdstrikereceiver"

import (
	"context"
	"encoding/json"
	"time"

	"github.com/crowdstrike/gofalcon/falcon/client"
	"github.com/crowdstrike/gofalcon/falcon/client/alerts"
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
	mockClient   *mockAlertsClient
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

	interval := 1 * time.Second

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.logger.Info("CrowdStrike receiver tick")
				// TODO: switch to real client
				// alerts, err := r.client.Alerts.GetV2(alerts.NewGetV2Params())
				alerts, err := r.mockClient.GetV2(alerts.NewGetV2Params())
				if err != nil {
					r.logger.Error("Error fetching alerts from CrowdStrike", zap.Error(err))
					continue
				}
				logs, err := convertAlertToPlogLogs(alerts)
				if err != nil {
					r.logger.Error("Error converting alerts to plog.Logs", zap.Error(err))
					continue
				}
				if err = r.nextConsumer.ConsumeLogs(ctx, *logs); err != nil {
					r.logger.Error("Error consuming logs", zap.Error(err))
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
