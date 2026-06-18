// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearchlogsreceiver

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/confmap/confmaptest"
)

func TestValidate(t *testing.T) {
	tests := []struct {
		desc        string
		mutate      func(*Config)
		expectedErr error
	}{
		{
			desc:   "valid default-ish config",
			mutate: func(_ *Config) {},
		},
		{
			desc:        "empty endpoint",
			mutate:      func(c *Config) { c.Endpoint = "" },
			expectedErr: errEmptyEndpoint,
		},
		{
			desc:        "bad endpoint scheme",
			mutate:      func(c *Config) { c.Endpoint = "ftp://localhost:9200" },
			expectedErr: errEndpointBadScheme,
		},
		{
			desc:        "password without username",
			mutate:      func(c *Config) { c.Username = ""; c.Password = "secret" },
			expectedErr: errUsernameNotSpecified,
		},
		{
			desc:        "username without password",
			mutate:      func(c *Config) { c.Username = "user"; c.Password = "" },
			expectedErr: errPasswordNotSpecified,
		},
		{
			desc:        "basic auth and api key together",
			mutate:      func(c *Config) { c.Username = "user"; c.Password = "secret"; c.APIKey = "abc" },
			expectedErr: errBasicAndAPIKey,
		},
		{
			desc:        "no indices",
			mutate:      func(c *Config) { c.Indices = nil },
			expectedErr: errNoIndices,
		},
		{
			desc:        "no timestamp field",
			mutate:      func(c *Config) { c.TimestampField = "" },
			expectedErr: errNoTimestampField,
		},
		{
			desc:   "single sort field is allowed",
			mutate: func(c *Config) { c.Sort = []map[string]string{{"@timestamp": "asc"}} },
		},
		{
			desc:        "empty sort",
			mutate:      func(c *Config) { c.Sort = nil },
			expectedErr: errSortEmpty,
		},
		{
			desc:        "sort with bad order",
			mutate:      func(c *Config) { c.Sort = []map[string]string{{"@timestamp": "asc"}, {"id": "sideways"}} },
			expectedErr: errSortBadOrder,
		},
		{
			desc:        "non positive page size",
			mutate:      func(c *Config) { c.PageSize = 0 },
			expectedErr: errBadPageSize,
		},
		{
			desc:        "negative batch limit",
			mutate:      func(c *Config) { c.BatchLimit = -1 },
			expectedErr: errBadBatchLimit,
		},
		{
			desc:   "zero batch limit is allowed",
			mutate: func(c *Config) { c.BatchLimit = 0 },
		},
		{
			desc:        "non positive poll interval",
			mutate:      func(c *Config) { c.PollInterval = 0 },
			expectedErr: errBadPollInterval,
		},
		{
			desc:        "bad start_at",
			mutate:      func(c *Config) { c.StartAt = "somewhere" },
			expectedErr: errBadStartAt,
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			cfg := validConfig()
			tc.mutate(cfg)
			err := cfg.Validate()
			if tc.expectedErr == nil {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, tc.expectedErr)
		})
	}
}

func validConfig() *Config {
	cfg := createDefaultConfig().(*Config)
	cfg.Indices = []string{"logs-*"}
	cfg.Sort = []map[string]string{{"@timestamp": "asc"}, {"event.id": "asc"}}
	return cfg
}

func TestLoadConfig(t *testing.T) {
	cm, err := confmaptest.LoadConf(filepath.Join("testdata", "config.yaml"))
	require.NoError(t, err)

	sub, err := cm.Sub(component.NewID(component.MustNewType("elasticsearchlogs")).String())
	require.NoError(t, err)

	cfg := createDefaultConfig()
	require.NoError(t, sub.Unmarshal(cfg))

	expected := createDefaultConfig().(*Config)
	expected.Endpoint = "http://localhost:9200"
	expected.Username = "otel"
	expected.Password = "otelpw"
	expected.Indices = []string{"logs-*"}
	expected.Query = map[string]any{
		"bool": map[string]any{
			"must": []any{
				map[string]any{"term": map[string]any{"service.name": "checkout"}},
			},
		},
	}
	expected.TimestampField = "@timestamp"
	expected.Sort = []map[string]string{{"@timestamp": "asc"}, {"event.id": "asc"}}
	expected.PageSize = 500
	expected.BatchLimit = 2000
	expected.PollInterval = 15 * time.Second
	expected.InitialDelay = time.Second
	expected.StartAt = startAtEnd
	expected.InitialLookback = time.Hour
	storageID := component.MustNewID("file_storage")
	expected.StorageID = &storageID

	assert.Equal(t, expected, cfg)
}
