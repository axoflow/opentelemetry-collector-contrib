// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package etwreceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/etwreceiver"

import "fmt"

func (c *WindowsEtwConfig) Validate() error {
	if _, err := c.extractProviderGUID(); err != nil {
		if !c.IgnoreMissingProvider {
			return err
		}
		return nil
	}
	if _, err := TraceLevelFromString(c.Level); err != nil {
		return err
	}
	if c.BufferSize < ETWReceiverMinimumBufferSize {
		return fmt.Errorf("buffer_size must be at least %v (in KB)", ETWReceiverMinimumBufferSize)
	}
	if c.BufferSize > ETWReceiverMaximumBufferSize {
		return fmt.Errorf("buffer_size must be at most %v (in KB)", ETWReceiverMaximumBufferSize)
	}
	return nil
}
