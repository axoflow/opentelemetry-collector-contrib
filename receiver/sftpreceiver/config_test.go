// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package sftpreceiver

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConfigValidate(t *testing.T) {
	cfg := NewFactory().CreateDefaultConfig().(*Config)
	require.Error(t, cfg.Validate())

	cfg.Endpoint, cfg.Username, cfg.Password = "localhost:22", "otel", "secret"
	cfg.KnownHosts, cfg.Include = "/etc/ssh/ssh_known_hosts", []string{"/logs/*.log"}
	require.NoError(t, cfg.Validate())

	cfg.Include = []string{"/logs/[.log"}
	require.Error(t, cfg.Validate())
}
