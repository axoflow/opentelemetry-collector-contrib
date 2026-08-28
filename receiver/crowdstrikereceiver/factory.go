// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package crowdstrikereceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/crowdstrikereceiver"

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/crowdstrike/gofalcon/falcon"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/receiver"
	"go.opentelemetry.io/collector/receiver/receiverhelper"
	"go.uber.org/zap"
	"golang.org/x/oauth2"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/crowdstrikereceiver/internal/metadata"
)

// NewFactory creates a factory for CrowdStrike receiver
func NewFactory() receiver.Factory {
	return receiver.NewFactory(
		metadata.Type,
		createDefaultConfig,
		receiver.WithLogs(createLogsReceiver, metadata.LogsStability),
	)
}

func createDefaultConfig() component.Config {
	return &Config{
		PollInterval: defaultPollInterval,
		NGSIEMSearch: NGSIEMSearchConfig{QueryString: defaultSearchQuery},
	}
}

func createLogsReceiver(ctx context.Context, settings receiver.Settings, cc component.Config, consumer consumer.Logs) (receiver.Logs, error) {
	return newCrowdstrikeReceiver(ctx, cc.(*Config), consumer, settings)
}

func newCrowdstrikeReceiver(ctx context.Context, cfg *Config, consumer consumer.Logs, settings receiver.Settings) (receiver.Logs, error) {
	logger := settings.Logger.With(zap.String("receiver", "crowdstrikereceiver"))

	if cfg.TLS.InsecureSkipVerify {
		logger.Warn("TLS certificate verification is DISABLED")
	}

	tlsConfig, err := cfg.TLS.LoadTLSConfig(ctx)
	if err != nil {
		logger.Error("Failed to load TLS configuration", zap.Error(err))
		return nil, err
	}

	customTransport := http.DefaultTransport.(*http.Transport).Clone()
	customTransport.TLSClientConfig = tlsConfig

	customHTTPClient := &http.Client{
		Transport: customTransport,
		Timeout:   5 * time.Minute,
	}

	// The one request that transport does not cover is gofalcon's cloud
	// autodiscovery, which goes through http.DefaultTransport internally. It
	// only matters when the settings change trust or client identity.
	customTLS := cfg.TLS.InsecureSkipVerify || cfg.TLS.CAFile != "" || cfg.TLS.CAPem != "" ||
		cfg.TLS.CertFile != "" || cfg.TLS.CertPem != "" || cfg.TLS.KeyFile != "" || cfg.TLS.KeyPem != ""
	if customTLS && cfg.Cloud == "" && cfg.HostOverride == "" {
		logger.Warn("tls settings do not apply to Falcon cloud autodiscovery; set cloud explicitly to skip that request")
	}

	cloudType := falcon.CloudType(falcon.CloudAutoDiscover)
	if cfg.Cloud != "" {
		cloudType, err = falcon.CloudValidate(cfg.Cloud)
		if err != nil {
			return nil, err
		}
	}

	apiConfig := &falcon.ApiConfig{
		Cloud:            cloudType,
		MemberCID:        cfg.MemberCID,
		BasePathOverride: cfg.BasePathOverride,
		Debug:            cfg.Debug,
	}

	if cfg.AccessToken != "" {
		logger.Info("Using access token for authentication")
		apiConfig.AccessToken = string(cfg.AccessToken)
	} else {
		logger.Info("Using client credentials for authentication")
		apiConfig.ClientId = cfg.ClientID
		apiConfig.ClientSecret = string(cfg.ClientSecret)
	}

	if cfg.HostOverride != "" {
		apiConfig.HostOverride = cfg.HostOverride
		logger.Info("Using host override", zap.String("host", cfg.HostOverride))
	}

	obsrecv, err := receiverhelper.NewObsReport(receiverhelper.ObsReportSettings{
		ReceiverID:             settings.ID,
		Transport:              "http",
		ReceiverCreateSettings: settings,
	})
	if err != nil {
		return nil, err
	}

	return &crowdstrikeReceiver{
		id:           settings.ID,
		logger:       logger,
		nextConsumer: consumer,
		config:       cfg,
		obsrecv:      obsrecv,
		newAPI: func(ctx context.Context) (falconAPI, error) {
			// Inject the HTTP client into the context for OAuth2. gofalcon
			// builds the API client's transport on top of it, so the TLS
			// settings reach every API call without touching the
			// process-global http.DefaultTransport, which races with any
			// other component constructing an HTTP client.
			apiConfig.Context = context.WithValue(ctx, oauth2.HTTPClient, customHTTPClient)
			client, err := falcon.NewClient(apiConfig)
			if err != nil {
				return nil, fmt.Errorf("failed to create CrowdStrike client: %w", err)
			}
			return &gofalconAPI{
				client:     client,
				logger:     logger,
				repository: cfg.NGSIEMSearch.Repository,
				query:      cfg.NGSIEMSearch.QueryString,
			}, nil
		},
	}, nil
}
