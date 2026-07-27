// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package idirareceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/idirareceiver"

import (
	"context"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/config/confighttp"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/receiver"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/idirareceiver/internal/metadata"
)

// NewFactory creates a factory for the Idira receiver.
func NewFactory() receiver.Factory {
	return receiver.NewFactory(
		metadata.Type,
		createDefaultConfig,
		receiver.WithLogs(createLogsReceiver, metadata.LogsStability),
	)
}

func createDefaultConfig() component.Config {
	clientConfig := confighttp.NewDefaultClientConfig()
	// confighttp leaves requests unbounded by default; a stuck API call must not stall a poll.
	clientConfig.Timeout = 30 * time.Second

	return &Config{
		ClientConfig:    clientConfig,
		Scopes:          []string{"isp.audit.events:read"},
		PollInterval:    time.Minute,
		InitialLookback: 5 * time.Minute,
		PageSize:        500,
	}
}

func createLogsReceiver(_ context.Context, settings receiver.Settings, rConf component.Config, consumer consumer.Logs) (receiver.Logs, error) {
	return newReceiver(rConf.(*Config), settings, consumer), nil
}
