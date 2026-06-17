// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearchlogsreceiver

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/receiver/receivertest"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/elasticsearchlogsreceiver/internal/metadata"
)

func TestCreateDefaultConfig(t *testing.T) {
	cfg := createDefaultConfig()
	require.NotNil(t, cfg)

	c, ok := cfg.(*Config)
	require.True(t, ok)
	assert.Equal(t, defaultEndpoint, c.Endpoint)
	assert.Equal(t, defaultTimestampField, c.TimestampField)
	assert.Equal(t, defaultPageSize, c.PageSize)
	assert.Equal(t, defaultPollInterval, c.PollInterval)
	assert.Equal(t, startAtEnd, c.StartAt)
}

func TestCreateLogsReceiver(t *testing.T) {
	cfg := validConfig()

	r, err := createLogsReceiver(
		context.Background(),
		receivertest.NewNopSettings(metadata.Type),
		cfg,
		consumertest.NewNop(),
	)
	require.NoError(t, err)
	require.NotNil(t, r)
}

func TestCreateLogsReceiverWrongConfig(t *testing.T) {
	_, err := createLogsReceiver(
		context.Background(),
		receivertest.NewNopSettings(metadata.Type),
		nil,
		consumertest.NewNop(),
	)
	assert.ErrorIs(t, err, errConfigNotESLogs)
}
