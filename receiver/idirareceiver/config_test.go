// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package idirareceiver

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/confmap/confmaptest"
	"go.opentelemetry.io/collector/confmap/xconfmap"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/idirareceiver/internal/metadata"
)

func TestLoadConfig(t *testing.T) {
	cm, err := confmaptest.LoadConf(filepath.Join("testdata", "config.yaml"))
	require.NoError(t, err)

	fileStorage := component.MustNewID("file_storage")

	tests := []struct {
		id          component.ID
		expected    *Config
		expectedErr []string
	}{
		{
			id: component.NewID(metadata.Type),
			expected: func() *Config {
				cfg := createDefaultConfig().(*Config)
				cfg.Endpoint = "https://audit.example.cloud"
				cfg.APIKey = "an-api-key"
				cfg.TokenURL = "https://identity.example.cloud/OAuth2/Token/web_app_id"
				cfg.ClientID = "siem-integration"
				cfg.ClientSecret = "a-secret"
				cfg.PollInterval = 5 * time.Minute
				cfg.InitialLookback = time.Hour
				cfg.PageSize = 100
				cfg.ApplicationCodes = []string{"DPA"}
				cfg.StorageID = &fileStorage
				return cfg
			}(),
		},
		{
			id: component.NewIDWithName(metadata.Type, "defaults"),
			expected: func() *Config {
				cfg := createDefaultConfig().(*Config)
				cfg.Endpoint = "https://audit.example.cloud"
				cfg.APIKey = "an-api-key"
				cfg.TokenURL = "https://identity.example.cloud/OAuth2/Token/web_app_id"
				cfg.ClientID = "siem-integration"
				cfg.ClientSecret = "a-secret"
				return cfg
			}(),
		},
		{
			id: component.NewIDWithName(metadata.Type, "invalid"),
			expectedErr: []string{
				"endpoint must be specified",
				"api_key must be specified",
				"token_url must be specified",
				"client_id must be specified",
				"client_secret must be specified",
				"poll_interval must be at least 1m, got 30s",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.id.String(), func(t *testing.T) {
			cfg := createDefaultConfig()
			sub, err := cm.Sub(tt.id.String())
			require.NoError(t, err)
			require.NoError(t, sub.Unmarshal(cfg))

			err = xconfmap.Validate(cfg)
			if len(tt.expectedErr) > 0 {
				for _, msg := range tt.expectedErr {
					assert.ErrorContains(t, err, msg)
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expected, cfg)
		})
	}
}

func TestValidateBounds(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	cfg.Endpoint = "https://audit.example.cloud"
	cfg.APIKey = "k"
	cfg.TokenURL = "https://identity.example.cloud/OAuth2/Token/app"
	cfg.ClientID = "id"
	cfg.ClientSecret = "secret"
	require.NoError(t, cfg.Validate())

	cfg.PageSize = 0
	cfg.InitialLookback = 0
	err := cfg.Validate()
	assert.ErrorContains(t, err, "page_size must be positive, got 0")
	assert.ErrorContains(t, err, "initial_lookback must be positive, got 0s")
}
