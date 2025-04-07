// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package etwreceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/etwreceiver"

import (
	"fmt"
	"strings"

	"go.opentelemetry.io/collector/component"
)

type TraceLevel int

const (
	TraceLevelCritical TraceLevel = iota + 1
	TraceLevelError
	TraceLevelWarning
	TraceLevelInformation
	TraceLevelVerbose
)

func TraceLevelFromString(level string) (TraceLevel, error) {
	switch strings.ToLower(level) {
	case "critical":
		return TraceLevelCritical, nil
	case "error":
		return TraceLevelError, nil
	case "warning":
		return TraceLevelWarning, nil
	case "information":
		return TraceLevelInformation, nil
	case "verbose":
		return TraceLevelVerbose, nil
	default:
		return 0, fmt.Errorf("unknown trace level: %s", level)
	}
}

// createDefaultConfig creates a config with type and version
func createDefaultConfig() component.Config {
	return &WindowsEtwConfig{}
}

// WindowsEtwConfig defines configuration for the etw receiver
type WindowsEtwConfig struct {
	Provider string `mapstructure:"provider"`
	// Set the trace level for the provider.
	// Higher levels include lower levels.
	Level string `mapstructure:"level"`
}
