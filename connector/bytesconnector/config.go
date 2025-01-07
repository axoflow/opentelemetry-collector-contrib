// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package bytesconnector // import import "github.com/open-telemetry/opentelemetry-collector-contrib/connector/bytesconnector"

import (
	"fmt"

	"go.opentelemetry.io/collector/confmap"
)

// Default metrics are emitted
const (
	defaultMetricNameLogs = "log.record.bytes"
	defaultMetricDescLogs = "The size of log records observed (in bytes)."
)

// Config for the connector
type Config struct {
	Logs map[string]MetricInfo `mapstructure:"logs"`
}

// MetricInfo for a data type
type MetricInfo struct {
	Description string            `mapstructure:"description"`
	Attributes  []AttributeConfig `mapstructure:"attributes"`
}

type AttributeConfig struct {
	Key          string `mapstructure:"key"`
	DefaultValue any    `mapstructure:"default_value"`
}

func (c *Config) Validate() error {
	for name, info := range c.Logs {
		if name == "" {
			return fmt.Errorf("logs: metric name missing")
		}
		if err := info.validateAttributes(); err != nil {
			return fmt.Errorf("logs attributes: metric %q: %w", name, err)
		}
	}
	return nil
}

func (i *MetricInfo) validateAttributes() error {
	for _, attr := range i.Attributes {
		if attr.Key == "" {
			return fmt.Errorf("attribute key missing")
		}
	}
	return nil
}

var _ confmap.Unmarshaler = (*Config)(nil)

// Unmarshal with custom logic to set default values.
// This is necessary to ensure that default metrics are
// not configured if the user has specified any custom metrics.
func (c *Config) Unmarshal(componentParser *confmap.Conf) error {
	if componentParser == nil {
		// Nothing to do if there is no config given.
		return nil
	}
	if err := componentParser.Unmarshal(c, confmap.WithIgnoreUnused()); err != nil {
		return err
	}
	if !componentParser.IsSet("logs") {
		c.Logs = defaultLogsConfig()
	}
	return nil
}

func defaultLogsConfig() map[string]MetricInfo {
	return map[string]MetricInfo{
		defaultMetricNameLogs: {
			Description: defaultMetricDescLogs,
		},
	}
}
