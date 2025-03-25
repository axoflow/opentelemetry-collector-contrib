// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package etwreceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/etwreceiver"

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/0xrawsec/golang-etw/etw"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/receiver"
	"go.opentelemetry.io/collector/receiver/receiverhelper"
	"go.uber.org/zap"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/etwreceiver/internal/metadata"
)

const (
	sessionNamePrefix = "ETWReceiverSession"
	reportFormat      = "etw"
)

func newFactoryAdapter() receiver.Factory {
	return receiver.NewFactory(
		metadata.Type,
		createDefaultConfig,
		receiver.WithLogs(createLogsReceiver, metadata.LogsStability))
}

func createLogsReceiver(ctx context.Context, settings receiver.Settings, cc component.Config, consumer consumer.Logs) (receiver.Logs, error) {
	return newEtwReceiver(ctx, cc.(*WindowsEtwConfig), consumer, settings)
}

type etwReceiver struct {
	providers       []etw.Provider
	session         *etw.RealTimeSession
	etwReader       *etw.Consumer
	cancel          context.CancelFunc
	logger          *zap.Logger
	obsrecv         *receiverhelper.ObsReport
	logsConsumer    consumer.Logs
	logsUnmarshaler plog.JSONUnmarshaler
	wg              sync.WaitGroup
}

func newEtwReceiver(_ context.Context, cfg *WindowsEtwConfig, consumer consumer.Logs, settings receiver.Settings) (*etwReceiver, error) {
	obsrecv, err := receiverhelper.NewObsReport(receiverhelper.ObsReportSettings{
		ReceiverID:             settings.ID,
		Transport:              "etw",
		ReceiverCreateSettings: settings,
	})
	if err != nil {
		return nil, err
	}

	providers := make([]etw.Provider, len(cfg.Providers))
	for idx, prov := range cfg.Providers {
		provider, err := etw.ParseProvider(prov)
		if err != nil {
			return nil, err
		}
		providers[idx] = provider
	}

	sessionName := strings.Join([]string{sessionNamePrefix, settings.ID.String()}, "-")

	return &etwReceiver{
		obsrecv:         obsrecv,
		providers:       providers,
		logsConsumer:    consumer,
		logger:          settings.Logger,
		logsUnmarshaler: plog.JSONUnmarshaler{},
		session:         etw.NewRealTimeSession(sessionName),
	}, nil
}

func (r *etwReceiver) Shutdown(_ context.Context) error {
	r.logger.Info("Shutting down ETW receiver")
	if r.etwReader != nil {
		if err := r.etwReader.Stop(); err != nil {
			return err
		}
	}

	if r.session.IsStarted() {
		if err := r.session.Stop(); err != nil {
			return err
		}
	}

	if r.cancel != nil {
		r.cancel()
	}

	r.wg.Wait()
	return nil
}

func (r *etwReceiver) Start(ctx context.Context, _ component.Host) error {
	r.logger.Debug("Starting ETW receiver", zap.Any("providers", r.providers))
	var cancelCtx context.Context
	cancelCtx, r.cancel = context.WithCancel(ctx)

	for _, prov := range r.providers {
		r.logger.Info("Enabling provider", zap.Any("provider", prov))
		if err := r.session.EnableProvider(prov); err != nil {
			r.logger.Error("Failed to enable provider", zap.Error(err))
			return err
		}
	}

	r.logger.Debug("Creating etwReader")
	r.etwReader = etw.NewRealTimeConsumer(cancelCtx)
	r.logger.Debug("Enabling sessions")
	r.etwReader.FromSessions(r.session)

	var err error
	r.wg.Add(1)
	if err = r.etwReader.Start(); err != nil {
		r.logger.Error("Failed to start ETW receiver's etwReader", zap.Error(err))
	}
	go func() {
		defer r.wg.Done()
		r.logger.Info("Reading ETW traces")
		for event := range r.etwReader.Events {
			logs, conversionError := r.convertEventToPlogLogs(event)
			if conversionError != nil {
				r.logger.Error("Failed to convert ETW event to OTLP log", zap.Error(conversionError))
				err = conversionError
			}
			r.logger.Debug("Consuming logs", zap.Any("logs", logs))
			r.obsrecv.StartLogsOp(cancelCtx)
			count := logs.LogRecordCount()
			err = r.logsConsumer.ConsumeLogs(cancelCtx, *logs)
			r.obsrecv.EndLogsOp(cancelCtx, reportFormat, count, err)
			if err != nil {
				r.logger.Error("Failed to consume logs", zap.Error(err))
			}
		}
	}()

	return err
}

func etwLevelToSeverityNumber(levelValue uint8) plog.SeverityNumber {
	// https://learn.microsoft.com/en-us/windows/win32/wes/eventmanifestschema-leveltype-complextype
	switch levelValue {
	case 1:
		return plog.SeverityNumberFatal
	case 2:
		return plog.SeverityNumberError
	case 3:
		return plog.SeverityNumberWarn
	case 4:
		return plog.SeverityNumberInfo
	case 5:
		return plog.SeverityNumberDebug
	}
	return plog.SeverityNumberUnspecified
}

func (r *etwReceiver) convertEventToPlogLogs(event *etw.Event) (*plog.Logs, error) {
	buff, err := json.Marshal(event)
	if err != nil {
		r.logger.Error("Failed to marshal ETW event", zap.Error(err))
		return nil, err
	}

	var rawMap map[string]any
	if err = json.Unmarshal(buff, &rawMap); err != nil {
		r.logger.Error("Failed to unmarhsal ETW event", zap.Error(err))
		return nil, err
	}

	out := plog.NewLogs()
	logs := out.ResourceLogs()
	rls := logs.AppendEmpty()

	ills := rls.ScopeLogs().AppendEmpty()
	lr := ills.LogRecords().AppendEmpty()

	err = lr.Attributes().FromRaw(rawMap)
	if err != nil {
		return nil, err
	}

	lr.SetTimestamp(pcommon.NewTimestampFromTime(event.System.TimeCreated.SystemTime))
	lr.SetObservedTimestamp(pcommon.NewTimestampFromTime(time.Now()))

	lr.SetSeverityNumber(etwLevelToSeverityNumber(event.System.Level.Value))
	lr.SetSeverityText(event.System.Level.Name)

	return &out, nil
}
