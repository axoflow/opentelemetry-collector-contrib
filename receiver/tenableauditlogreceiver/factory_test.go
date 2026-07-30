// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package tenableauditlogreceiver

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/receiver/receivertest"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/tenableauditlogreceiver/internal/metadata"
)

func TestCreateDefaultConfig(t *testing.T) {
	cfg := createDefaultConfig().(*Config)

	assert.Equal(t, "https://cloud.tenable.com", cfg.Endpoint)
	assert.Equal(t, 5*time.Minute, cfg.PollInterval)
	assert.Equal(t, 1000, cfg.PageSize)
	assert.Equal(t, 5000, cfg.MaxRecordsPerPoll)
	assert.Equal(t, 24*time.Hour, cfg.InitialLookback)
	assert.Nil(t, cfg.StorageID)
}

func TestCreateLogsReceiver(t *testing.T) {
	factory := NewFactory()
	assert.Equal(t, metadata.Type, factory.Type())

	rcvr, err := factory.CreateLogs(t.Context(), receivertest.NewNopSettings(metadata.Type),
		createDefaultConfig(), consumertest.NewNop())
	require.NoError(t, err)
	assert.NotNil(t, rcvr)
}
