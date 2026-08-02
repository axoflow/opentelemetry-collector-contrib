// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package crowdstrikereceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/crowdstrikereceiver"

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/confmap/confmaptest"
	"go.opentelemetry.io/collector/confmap/xconfmap"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/crowdstrikereceiver/internal/metadata"
)

func TestLoadConfig(t *testing.T) {
	cm, err := confmaptest.LoadConf(filepath.Join("testdata", "config.yaml"))
	require.NoError(t, err)

	cases := []struct {
		name     string
		expected *Config
	}{
		{
			name: "default",
			expected: &Config{
				ClientID:     "an-api-client",
				ClientSecret: "an-api-secret",
				PollInterval: defaultPollInterval,
				NGSIEMSearch: NGSIEMSearchConfig{QueryString: defaultSearchQuery},
			},
		},
		{
			name: "full",
			expected: &Config{
				AccessToken:      "a-token",
				MemberCID:        "a-member-cid",
				Cloud:            "eu-1",
				HostOverride:     "api.example.invalid",
				BasePathOverride: "/custom",
				PollInterval:     5 * time.Minute,
				InitialLookback:  24 * time.Hour,
				DisableAlerts:    true,
				Debug:            true,
				NGSIEMSearch: NGSIEMSearchConfig{
					Repository:  "search-all",
					QueryString: "#type = falcon",
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := NewFactory().CreateDefaultConfig()
			loaded, err := cm.Sub(component.NewIDWithName(metadata.Type, tc.name).String())
			require.NoError(t, err)
			require.NoError(t, loaded.Unmarshal(cfg))
			require.Equal(t, tc.expected, cfg)
			require.NoError(t, xconfmap.Validate(cfg))
		})
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name        string
		mutate      func(*Config)
		expectedErr error
	}{
		{
			// Client credentials do autodiscover the cloud, so neither is
			// needed here.
			name:   "client credentials without a cloud or a host override",
			mutate: func(*Config) {},
		},
		{
			name: "access token with a cloud",
			mutate: func(c *Config) {
				c.ClientID = ""
				c.ClientSecret = ""
				c.AccessToken = "a-token"
				c.Cloud = "eu-1"
			},
		},
		{
			name: "access token with a host override",
			mutate: func(c *Config) {
				c.ClientID = ""
				c.ClientSecret = ""
				c.AccessToken = "a-token"
				c.HostOverride = "api.example.invalid"
			},
		},
		{
			// The SDK cannot autodiscover a cloud from a token, so this only
			// ever fails at startup.
			name: "access token without a cloud or a host override",
			mutate: func(c *Config) {
				c.ClientID = ""
				c.ClientSecret = ""
				c.AccessToken = "a-token"
			},
			expectedErr: errNoTokenHost,
		},
		{
			name: "no credentials",
			mutate: func(c *Config) {
				c.ClientSecret = ""
			},
			expectedErr: errNoCredentials,
		},
		{
			name: "zero poll interval",
			mutate: func(c *Config) {
				c.PollInterval = 0
			},
			expectedErr: errNoPollInterval,
		},
		{
			name: "negative lookback",
			mutate: func(c *Config) {
				c.InitialLookback = -time.Second
			},
			expectedErr: errNegativeLookback,
		},
		{
			name: "alerts disabled without a repository",
			mutate: func(c *Config) {
				c.DisableAlerts = true
			},
			expectedErr: errNoSource,
		},
		{
			name: "alerts disabled with a repository",
			mutate: func(c *Config) {
				c.DisableAlerts = true
				c.NGSIEMSearch.Repository = "search-all"
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := createDefaultConfig().(*Config)
			cfg.ClientID = "an-api-client"
			cfg.ClientSecret = "an-api-secret"
			tc.mutate(cfg)

			err := cfg.Validate()
			if tc.expectedErr == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tc.expectedErr)
		})
	}
}
