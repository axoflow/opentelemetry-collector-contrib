// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package etwreceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/etwreceiver"

import (
	"go.opentelemetry.io/collector/component"
)

// createDefaultConfig creates a config with type and version
func createDefaultConfig() component.Config {
	return &WindowsEtwConfig{}
}

// WindowsEtwConfig defines configuration for the etw receiver
type WindowsEtwConfig struct {
	Providers []string `mapstructure:"providers"`
}
