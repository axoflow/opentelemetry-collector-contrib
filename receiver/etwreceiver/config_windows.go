// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package etwreceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/etwreceiver"

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
	return nil
}
