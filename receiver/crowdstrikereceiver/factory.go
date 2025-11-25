// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package crowdstrikereceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/crowdstrikereceiver"

import (
	"context"
	"time"

	"github.com/crowdstrike/gofalcon/falcon"
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

func newCrowdstrikeReceiver(ctx context.Context, cfg *CrowdstrikeReceiverConfig, consumer consumer.Logs, settings receiver.Settings) (receiver.Logs, error) {
	var cloudType falcon.CloudType = falcon.CloudAutoDiscover
	if cfg.Cloud != "" {
		c, err := falcon.CloudValidate(cfg.Cloud)
		if err != nil {
			return nil, err
		}
		cloudType = c
	}
	client, err := falcon.NewClient(&falcon.ApiConfig{
		AccessToken:      cfg.AccessToken,
		ClientId:         cfg.ClientID,
		ClientSecret:     cfg.ClientSecret,
		Cloud:            cloudType,
		Context:          ctx,
		MemberCID:        cfg.MemberCID,
		HostOverride:     cfg.HostOverride,
		BasePathOverride: cfg.BasePathOverride,
	})
	if err != nil {
		return nil, err
	}
	pollInterval := time.Duration(1) * time.Second
	if cfg.PollInterval != nil {
		pollInterval = *cfg.PollInterval
	}
	return &crowdstrikeReceiver{
		logger:       settings.Logger,
		nextConsumer: consumer,
		config:       cfg,
		client:       client,
		pollInterval: pollInterval,
	}, nil
}
