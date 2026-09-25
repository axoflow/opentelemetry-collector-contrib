// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package crowdstrikefdrreceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/crowdstrikefdrreceiver"

import (
	"context"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/receiver"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/crowdstrikefdrreceiver/internal/metadata"
)

func NewFactory() receiver.Factory {
	return receiver.NewFactory(
		metadata.Type,
		createDefaultConfig,
		receiver.WithLogs(createLogs, metadata.LogsStability),
	)
}

func createDefaultConfig() component.Config {
	return &Config{
		// Same defaults as CrowdStrike's own SQS consumer.
		VisibilityTimeout:   5 * time.Minute,
		MaxNumberOfMessages: 10,
	}
}

func createLogs(_ context.Context, settings receiver.Settings, cfg component.Config, logs consumer.Logs) (receiver.Logs, error) {
	return newReceiver(cfg.(*Config), settings, logs)
}
