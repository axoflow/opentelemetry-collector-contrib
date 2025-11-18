// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package crowdstrikereceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/crowdstrikereceiver"

import (
	"context"

	"go.opentelemetry.io/collector/component"
)

type crowdstrikeReceiver struct {
	cancel context.CancelFunc
}

func (r *crowdstrikeReceiver) Shutdown(_ context.Context) error {
	if r.cancel != nil {
		r.cancel()
	}
	return nil
}

func (r *crowdstrikeReceiver) Start(ctx context.Context, _ component.Host) error {
	ctx = context.Background()
	ctx, r.cancel = context.WithCancel(ctx)
	return nil
}
