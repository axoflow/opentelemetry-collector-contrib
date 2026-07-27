// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package idirareceiver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/receiver/receivertest"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/idirareceiver/internal/metadata"
)

func TestCreateDefaultConfig(t *testing.T) {
	cfg := createDefaultConfig()
	require.NotNil(t, cfg)
	assert.NoError(t, componenttest.CheckConfigStruct(cfg))
}

func TestCreateLogsReceiver(t *testing.T) {
	r, err := NewFactory().CreateLogs(t.Context(), receivertest.NewNopSettings(metadata.Type),
		testConfig("https://audit.example.cloud"), consumertest.NewNop())
	require.NoError(t, err)
	assert.NotNil(t, r)
}
