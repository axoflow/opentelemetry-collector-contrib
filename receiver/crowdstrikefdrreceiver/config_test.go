// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package crowdstrikefdrreceiver

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestConfigValidate(t *testing.T) {
	valid := func() *Config {
		cfg := createDefaultConfig().(*Config)
		cfg.QueueURL = "https://sqs.us-west-1.amazonaws.com/123/queue"
		cfg.Region = "us-west-1"
		return cfg
	}
	require.NoError(t, valid().Validate())

	for name, tc := range map[string]struct {
		mutate func(*Config)
		want   string
	}{
		"missing queue":       {func(c *Config) { c.QueueURL = "" }, "queue_url is required"},
		"missing region":      {func(c *Config) { c.Region = "" }, "region is required"},
		"half credentials":    {func(c *Config) { c.AccessKeyID = "AKIA" }, "must be set together"},
		"visibility too long": {func(c *Config) { c.VisibilityTimeout = 13 * time.Hour }, "visibility_timeout"},
		"too many messages":   {func(c *Config) { c.MaxNumberOfMessages = 11 }, "max_number_of_messages"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := valid()
			tc.mutate(cfg)
			require.ErrorContains(t, cfg.Validate(), tc.want)
		})
	}
}
