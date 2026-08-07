// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package crowdstrikereceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/crowdstrikereceiver"

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/receiver/receivertest"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/crowdstrikereceiver/internal/metadata"
)

// An access token carries no cloud, so gofalcon cannot autodiscover one and
// the receiver must fail construction rather than start a poller that will
// only ever error.
func TestCreateLogsWithoutCloud(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	cfg.AccessToken = "a-token"

	_, err := NewFactory().CreateLogs(t.Context(), receivertest.NewNopSettings(metadata.Type), cfg, consumertest.NewNop())
	require.Error(t, err)
}
