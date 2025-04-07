// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package etwreceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/etwreceiver"

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/bi-zone/etw"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/receiver"
	"go.opentelemetry.io/collector/receiver/receiverhelper"
	"go.uber.org/zap"
	"golang.org/x/sys/windows"

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
	guid            windows.GUID
	session         *etw.Session
	sessionStarted  bool
	cancel          context.CancelFunc
	logger          *zap.Logger
	obsrecv         *receiverhelper.ObsReport
	logsConsumer    consumer.Logs
	logsUnmarshaler plog.JSONUnmarshaler
	wg              sync.WaitGroup
}

func (cfg *WindowsEtwConfig) extractProviderGUID() (*windows.GUID, error) {
	if !strings.HasPrefix(cfg.Provider, "{") {
		return etw.ProviderGUIDFromString(cfg.Provider)
	}

	guid, err := windows.GUIDFromString(cfg.Provider)
	if err != nil {
		return nil, err
	}

	return &guid, nil
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

	guidPtr, err := cfg.extractProviderGUID()
	if err != nil {
		settings.Logger.Fatal("Could not find provider", zap.Any("provider", cfg.Provider), zap.Error(err))
		return nil, err
	}
	guid := *guidPtr

	settings.Logger.Info("Using provider", zap.Any("provider", guid))

	traceLevel, err := TraceLevelFromString(cfg.Level)
	if err != nil {
		return nil, err
	}

	sessionName := strings.Join([]string{sessionNamePrefix, settings.ID.String()}, "-")
	var exists etw.ExistsError
	session, err := etw.NewSession(guid, etw.WithName(sessionName), etw.WithLevel(etw.TraceLevel(traceLevel)))
	if errors.As(err, &exists) {
		settings.Logger.Info("ETW session already exists, deleting previous session, then creating new one", zap.String("session_name", exists.SessionName))
		err = etw.KillSession(exists.SessionName)
		session, err = etw.NewSession(guid, etw.WithName(sessionName))
	}
	if err != nil {
		settings.Logger.Fatal("Could not create ETW session", zap.Error(err))
		return nil, err
	}

	return &etwReceiver{
		obsrecv:         obsrecv,
		guid:            guid,
		logsConsumer:    consumer,
		logger:          settings.Logger,
		logsUnmarshaler: plog.JSONUnmarshaler{},
		session:         session,
	}, nil
}

func (r *etwReceiver) Shutdown(_ context.Context) error {
	r.logger.Info("Shutting down ETW receiver")

	if r.sessionStarted {
		r.sessionStarted = false
		if err := r.session.Close(); err != nil {
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
	r.logger.Debug("Starting ETW receiver")
	r.sessionStarted = true
	var cancelCtx context.Context
	cancelCtx, r.cancel = context.WithCancel(ctx)

	var err error
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.logger.Info("Reading ETW traces")
		if err := r.session.Process(func(event *etw.Event) {
			props, _ := event.EventProperties()
			r.logger.Info("Received ETW event", zap.Any("event", event), zap.Any("props", props))

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
		}); err != nil {
			r.logger.Error("Failed to read from ETW session", zap.Error(err))
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
	eventProperties, err := event.EventProperties()
	if err != nil {
		eventProperties = map[string]any{}
	}

	providerID := event.Header.ProviderID.String()
	activityID := event.Header.ActivityID.String()

	unifiedMap := map[string]any{
		"EventData":         eventProperties,
		"ExtendedEventInfo": event.ExtendedInfo(),
		"System":            event,
		"ProviderID":        providerID,
		"ActivityID":        activityID,
	}

	buff, err := json.Marshal(unifiedMap)
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
		r.logger.Error("Failed to populate from rawMap", zap.Error(err))
		return nil, err
	}

	lr.SetTimestamp(pcommon.NewTimestampFromTime(event.Header.TimeStamp))
	lr.SetObservedTimestamp(pcommon.NewTimestampFromTime(time.Now()))

	lr.SetSeverityNumber(etwLevelToSeverityNumber(event.Header.Level))

	return &out, nil
}
