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
	switch strings.ToLower(strings.TrimSpace(level)) {
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

func (t TraceLevel) String() string {
	switch t {
	case TraceLevelCritical:
		return "critical"
	case TraceLevelError:
		return "error"
	case TraceLevelWarning:
		return "warning"
	case TraceLevelInformation:
		return "information"
	case TraceLevelVerbose:
		return "verbose"
	default:
		return "unknown trace level"
	}
}

// createDefaultConfig creates a config with type and version
func createDefaultConfig() component.Config {
	return &WindowsEtwConfig{
		Level: TraceLevelVerbose.String(),
	}
}

// WindowsEtwConfig defines configuration for the etw receiver
type WindowsEtwConfig struct {
	Provider string `mapstructure:"provider"`
	// Set the trace level for the provider.
	// Higher levels include lower levels.
	// Default is `verbose`.
	Level string `mapstructure:"level"`
	// 64-bit bitmask of keywords that determine the categories of events that you want the provider to write.
	// The provider typically writes an event if the event's keyword bits match any of the bits set in this value.
	MatchAnyKeywords uint64 `mapstructure:"match_any_keywords"`
	// 64-bit bitmask of keywords that restricts the events that you want the provider to write.
	// The provider typically writes an event if the event's keyword bits match all of the bits set in this value.
	MatchAllKeywords      uint64 `mapstructure:"match_all_keywords"`
	IgnoreMissingProvider bool   `mapstructure:"ignore_missing_provider"`
	// The following fields control ETW tracing session buffering
	// See https://learn.microsoft.com/en-us/windows/win32/api/evntrace/ns-evntrace-event_trace_properties
	// Kilobytes of memory allocated for each event tracing session buffer.
	// The minimum buffer size is 4 (4KB). The maximum buffer size is 16384 (16MB).
	BufferSize uint32 `mapstructure:"buffer_size"`
	// Minimum number of buffers reserved for the tracing session's buffer pool.
	MinimumBuffers uint32 `mapstructure:"minimum_buffers"`
	// Maximum number of buffers to be allocated for the tracing session's buffer pool.
	MaximumBuffers uint32 `mapstructure:"maximum_buffers"`
	// How often, in seconds, any non-empty trace buffers are flushed.
	// The minimum flush time is 1 second.
	// For real-time sessions: Setting FlushTimer to 0 will enable a default timeout of 1 second.
	// Real-time sessions should set the flush timer based on how quickly the data needs to be received.
	FlushTimerSeconds uint32 `mapstructure:"flush_timer"`

	// Number of ETW events the ETW receiver stores in memory for processing.
	EventBufferSize uint `mapstructure:"event_buffer_size"`
	// Number of worker goroutines that process ETW events.
	NumWorkers uint `mapstructure:"num_workers"`
}
