// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package etwreceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/etwreceiver"

func (c *WindowsEtwConfig) Validate() error {
	if _, err := c.extractProviderGUID(); err != nil {
		return err
	}
	return nil
}
