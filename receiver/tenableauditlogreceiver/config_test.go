// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package tenableauditlogreceiver

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/confmap/confmaptest"
	"go.opentelemetry.io/collector/confmap/xconfmap"
)

func TestLoadConfig(t *testing.T) {
	cm, err := confmaptest.LoadConf(filepath.Join("testdata", "config.yaml"))
	require.NoError(t, err)
	sub, err := cm.Sub("tenable_audit_log")
	require.NoError(t, err)

	cfg := createDefaultConfig().(*Config)
	require.NoError(t, sub.Unmarshal(cfg))
	require.NoError(t, xconfmap.Validate(cfg))

	assert.Equal(t, "https://cloud.tenable.example", cfg.Endpoint)
	assert.Equal(t, "testaccesskey", string(cfg.AccessKey))
	assert.Equal(t, "testsecretkey", string(cfg.SecretKey))
	assert.Equal(t, time.Minute, cfg.PollInterval)
	assert.Equal(t, 500, cfg.PageSize)
	assert.Equal(t, 2000, cfg.MaxRecordsPerPoll)
	assert.Equal(t, 6*time.Hour, cfg.InitialLookback)
	assert.Equal(t, component.MustNewID("file_storage"), *cfg.StorageID)
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(*Config)
		expectedErr string
	}{
		{
			name:   "default config with credentials",
			mutate: func(*Config) {},
		},
		{
			name:        "missing endpoint",
			mutate:      func(c *Config) { c.Endpoint = "" },
			expectedErr: "endpoint must be specified",
		},
		{
			name:        "missing access key",
			mutate:      func(c *Config) { c.AccessKey = "" },
			expectedErr: "access_key must be specified",
		},
		{
			name:        "missing secret key",
			mutate:      func(c *Config) { c.SecretKey = "" },
			expectedErr: "secret_key must be specified",
		},
		{
			name:        "non positive poll interval",
			mutate:      func(c *Config) { c.PollInterval = 0 },
			expectedErr: "poll_interval must be positive",
		},
		{
			name:        "page size above API limit",
			mutate:      func(c *Config) { c.PageSize = maxPageSize + 1 },
			expectedErr: "page_size must be between 1 and 5000",
		},
		{
			name:        "non positive max records",
			mutate:      func(c *Config) { c.MaxRecordsPerPoll = -1 },
			expectedErr: "max_records_per_poll must be positive",
		},
		{
			name:        "non positive initial lookback",
			mutate:      func(c *Config) { c.InitialLookback = 0 },
			expectedErr: "initial_lookback must be positive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := createDefaultConfig().(*Config)
			cfg.AccessKey = "key"
			cfg.SecretKey = "secret"
			tt.mutate(cfg)

			err := cfg.Validate()
			if tt.expectedErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.expectedErr)
		})
	}
}
