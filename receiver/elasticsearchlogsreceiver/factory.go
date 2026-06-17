// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearchlogsreceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/elasticsearchlogsreceiver"

import (
	"context"
	"errors"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/config/confighttp"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/receiver"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/elasticsearchlogsreceiver/internal/metadata"
)

const (
	defaultEndpoint          = "http://localhost:9200"
	defaultHTTPClientTimeout = 10 * time.Second
	defaultPollInterval      = 30 * time.Second
	defaultInitialDelay      = time.Second
	defaultPageSize          = 1000
	defaultTimestampField    = "@timestamp"
)

var errConfigNotESLogs = errors.New("config was not an elasticsearchlogs receiver config")

// NewFactory creates a factory for the Elasticsearch logs receiver.
func NewFactory() receiver.Factory {
	return receiver.NewFactory(
		metadata.Type,
		createDefaultConfig,
		receiver.WithLogs(createLogsReceiver, metadata.LogsStability))
}

func createDefaultConfig() component.Config {
	clientConfig := confighttp.NewDefaultClientConfig()
	clientConfig.Endpoint = defaultEndpoint
	clientConfig.Timeout = defaultHTTPClientTimeout

	return &Config{
		ClientConfig:    clientConfig,
		TimestampField:  defaultTimestampField,
		PageSize:        defaultPageSize,
		PollInterval:    defaultPollInterval,
		InitialDelay:    defaultInitialDelay,
		StartAt:         startAtEnd,
		InitialLookback: 0,
	}
}

func createLogsReceiver(
	_ context.Context,
	params receiver.Settings,
	rConf component.Config,
	consumer consumer.Logs,
) (receiver.Logs, error) {
	cfg, ok := rConf.(*Config)
	if !ok {
		return nil, errConfigNotESLogs
	}
	return newLogsReceiver(params, cfg, consumer), nil
}
