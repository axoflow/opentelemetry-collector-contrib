// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package crowdstrikereceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/crowdstrikereceiver"

import (
	"context"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/crowdstrikereceiver/internal/metadata"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/receiver"
)

// NewFactory creates a factory for etw receiver
func NewFactory() receiver.Factory {
	return newFactoryAdapter()
}

func newFactoryAdapter() receiver.Factory {
	return receiver.NewFactory(
		metadata.Type,
		createDefaultConfig,
		receiver.WithLogs(createLogsReceiver, metadata.LogsStability),
	)
}

func createDefaultConfig() component.Config {
	return &CrowdstrikeReceiverConfig{}
}

func createLogsReceiver(ctx context.Context, settings receiver.Settings, cc component.Config, consumer consumer.Logs) (receiver.Logs, error) {
	return newCrowdstrikeReceiver(ctx, cc.(*CrowdstrikeReceiverConfig), consumer, settings)
}

func newCrowdstrikeReceiver(_ context.Context, cfg *CrowdstrikeReceiverConfig, consumer consumer.Logs, settings receiver.Settings) (receiver.Logs, error) {
	// client, err := falcon.NewClient(&falcon.ApiConfig{})
	// if err != nil {
	//  return nil, err
	// }
	return &crowdstrikeReceiver{
		logger:       settings.Logger,
		nextConsumer: consumer,
		config:       cfg,
		mockClient:   &mockAlertsClient{},
	}, nil
}
