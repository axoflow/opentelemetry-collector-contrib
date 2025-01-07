// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

//go:generate mdatagen metadata.yaml

package bytesconnector // import import "github.com/open-telemetry/opentelemetry-collector-contrib/connector/bytesconnector"

import (
	"context"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/connector"
	"go.opentelemetry.io/collector/consumer"

	"github.com/open-telemetry/opentelemetry-collector-contrib/connector/bytesconnector/internal/metadata"
	"github.com/open-telemetry/opentelemetry-collector-contrib/pkg/ottl/contexts/ottllog"
)

// NewFactory returns a ConnectorFactory.
func NewFactory() connector.Factory {
	return connector.NewFactory(
		metadata.Type,
		createDefaultConfig,
		connector.WithLogsToMetrics(createLogsToMetrics, metadata.LogsToMetricsStability),
	)
}

// createDefaultConfig creates the default configuration.
func createDefaultConfig() component.Config {
	return &Config{}
}

// createLogsToMetrics creates a logs to metrics connector based on provided config.
func createLogsToMetrics(
	_ context.Context,
	set connector.Settings,
	cfg component.Config,
	nextConsumer consumer.Metrics,
) (connector.Logs, error) {
	c := cfg.(*Config)

	metricDefs := make(map[string]metricDef[ottllog.TransformContext], len(c.Logs))
	for name, info := range c.Logs {
		md := metricDef[ottllog.TransformContext]{
			desc:  info.Description,
			attrs: info.Attributes,
		}
		metricDefs[name] = md
	}

	return &count{
		metricsConsumer: nextConsumer,
		logsMetricDefs:  metricDefs,
	}, nil
}

type metricDef[K any] struct {
	desc  string
	attrs []AttributeConfig
}
