// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package sftpreceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/sftpreceiver"

import (
	"context"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/receiver"
	"go.opentelemetry.io/collector/receiver/receiverhelper"
)

const transport = "sftp"

type sftpReceiver struct {
	cfg     *Config
	set     receiver.Settings
	next    consumer.Logs
	obsrecv *receiverhelper.ObsReport
}

func newReceiver(cfg *Config, set receiver.Settings, next consumer.Logs) (*sftpReceiver, error) {
	obsrecv, err := receiverhelper.NewObsReport(receiverhelper.ObsReportSettings{
		ReceiverID:             set.ID,
		Transport:              transport,
		ReceiverCreateSettings: set,
	})
	if err != nil {
		return nil, err
	}
	return &sftpReceiver{cfg: cfg, set: set, next: next, obsrecv: obsrecv}, nil
}

func (*sftpReceiver) Start(context.Context, component.Host) error {
	return nil
}

func (*sftpReceiver) Shutdown(context.Context) error {
	return nil
}
