// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package tenableauditlogreceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/tenableauditlogreceiver"

import (
	"context"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/config/confighttp"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/receiver"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/tenableauditlogreceiver/internal/metadata"
)

// NewFactory returns the component factory for the tenableauditlogreceiver.
func NewFactory() receiver.Factory {
	return receiver.NewFactory(
		metadata.Type,
		createDefaultConfig,
		receiver.WithLogs(createLogsReceiver, metadata.LogsStability),
	)
}

func createDefaultConfig() component.Config {
	clientCfg := confighttp.NewDefaultClientConfig()
	clientCfg.Endpoint = "https://cloud.tenable.com"
	return &Config{
		ClientConfig:      clientCfg,
		PollInterval:      5 * time.Minute,
		PageSize:          1000,
		MaxRecordsPerPoll: 5000,
		InitialLookback:   24 * time.Hour,
	}
}

func createLogsReceiver(
	_ context.Context,
	settings receiver.Settings,
	rConf component.Config,
	logs consumer.Logs,
) (receiver.Logs, error) {
	return newReceiver(rConf.(*Config), settings, logs), nil
}
