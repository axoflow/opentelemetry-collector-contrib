// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package etwreceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/etwreceiver"

const (
	ETWReceiverEventBufferSize = uint(1000)
	ETWReceiverNumberOfWorkers = uint(1)

	ETWReceiverMinimumBufferSize = uint32(4)
	ETWReceiverMaximumBufferSize = uint32(16384)
)
