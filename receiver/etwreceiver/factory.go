// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package etwreceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/etwreceiver"

import (
	"go.opentelemetry.io/collector/receiver"
)

// NewFactory creates a factory for etw receiver
func NewFactory() receiver.Factory {
	return newFactoryAdapter()
}
