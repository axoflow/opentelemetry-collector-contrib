// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package crowdstrikereceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/crowdstrikereceiver"

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"time"

	"github.com/crowdstrike/gofalcon/falcon"
	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/crowdstrikereceiver/internal/metadata"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/receiver"
	"go.uber.org/zap"
	"golang.org/x/oauth2"
)

// NewFactory creates a factory for CrowdStrike receiver
func NewFactory() receiver.Factory {
	return newFactoryAdapter()
}

func newFactoryAdapter() receiver.Factory {
	return receiver.NewFactory(
		metadata.Type,
		createDefaultConfig,
		receiver.WithLogs(createLogsReceiver, metadata.LogsStability),
	)
}

func createDefaultConfig() component.Config {
	return &CrowdstrikeReceiverConfig{}
}

func createLogsReceiver(ctx context.Context, settings receiver.Settings, cc component.Config, consumer consumer.Logs) (receiver.Logs, error) {
	return newCrowdstrikeReceiver(ctx, cc.(*CrowdstrikeReceiverConfig), consumer, settings)
}

func newCrowdstrikeReceiver(ctx context.Context, cfg *CrowdstrikeReceiverConfig, consumer consumer.Logs, settings receiver.Settings) (receiver.Logs, error) {
	logger := settings.Logger.With(zap.String("receiver", "crowdstrikereceiver"))

	if cfg.TLS.InsecureSkipVerify {
		logger.Warn("TLS certificate verification is DISABLED")
	}

	var tlsConfig *tls.Config
	tlsConfig, err := cfg.TLS.LoadTLSConfig(ctx)
	if err != nil {
		logger.Error("Failed to load TLS configuration", zap.Error(err))
		return nil, err
	}

	// Create custom HTTP client with TLS config
	customTransport := &http.Transport{
		TLSClientConfig:       tlsConfig,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	customHTTPClient := &http.Client{
		Transport: customTransport,
		Timeout:   5 * time.Minute,
	}

	// Inject HTTP client into context for OAuth2
	ctx = context.WithValue(ctx, oauth2.HTTPClient, customHTTPClient)

	// Temporarily set as default transport during client creation
	originalDefaultTransport := http.DefaultTransport
	http.DefaultTransport = customTransport
	defer func() {
		http.DefaultTransport = originalDefaultTransport
	}()

	// Determine cloud type
	var cloudType falcon.CloudType = falcon.CloudAutoDiscover
	if cfg.Cloud != "" {
		c, err := falcon.CloudValidate(cfg.Cloud)
		if err != nil {
			return nil, err
		}
		cloudType = c
	}

	// Build API config
	apiConfig := &falcon.ApiConfig{
		Context:          ctx,
		Cloud:            cloudType,
		MemberCID:        cfg.MemberCID,
		BasePathOverride: cfg.BasePathOverride,
		Debug:            cfg.Debug,
	}

	// Use access token OR client credentials
	if cfg.AccessToken != "" {
		logger.Info("Using access token for authentication")
		apiConfig.AccessToken = cfg.AccessToken
	} else {
		logger.Info("Using client credentials for authentication")
		apiConfig.ClientId = cfg.ClientID
		apiConfig.ClientSecret = cfg.ClientSecret
	}

	// Configure host override
	if cfg.HostOverride != "" {
		apiConfig.HostOverride = cfg.HostOverride

		logger.Info("Using host override", zap.String("host", cfg.HostOverride))

	}

	client, err := falcon.NewClient(apiConfig)
	if err != nil {
		logger.Error("Failed to create CrowdStrike client", zap.Error(err))
		return nil, fmt.Errorf("failed to create CrowdStrike client: %w", err)
	}

	logger.Info("CrowdStrike client created successfully")

	// Determine poll interval
	pollInterval := 1 * time.Second
	if cfg.PollInterval != nil {
		pollInterval = *cfg.PollInterval
	}

	return &crowdstrikeReceiver{
		logger:       logger,
		nextConsumer: consumer,
		config:       cfg,
		client:       client,
		pollInterval: pollInterval,
	}, nil
}
