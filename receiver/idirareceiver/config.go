// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package idirareceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/idirareceiver"

import (
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/config/confighttp"
	"go.opentelemetry.io/collector/config/configopaque"
)

// Config defines configuration for the Idira receiver.
type Config struct {
	// ClientConfig carries the audit API base URL in `endpoint`, plus TLS, proxy and timeout settings.
	confighttp.ClientConfig `mapstructure:",squash"`

	// APIKey is the SIEM integration API key, sent as the x-api-key header.
	APIKey configopaque.String `mapstructure:"api_key"`

	// TokenURL is the Identity Administration OAuth2 token endpoint, in the form
	// https://<identity_fqdn>/OAuth2/Token/<web_app_id>.
	TokenURL     string              `mapstructure:"token_url"`
	ClientID     string              `mapstructure:"client_id"`
	ClientSecret configopaque.String `mapstructure:"client_secret"`
	Scopes       []string            `mapstructure:"scopes"`

	// PollInterval is how often a new query is created. The API accepts one query per minute.
	PollInterval time.Duration `mapstructure:"poll_interval"`
	// InitialLookback is how far back the first poll queries when no checkpoint is stored.
	InitialLookback time.Duration `mapstructure:"initial_lookback"`
	// PageSize is the number of events fetched per results page.
	PageSize int `mapstructure:"page_size"`
	// ApplicationCodes restricts the query to these application codes, e.g. [DPA]. Empty means no filter.
	ApplicationCodes []string `mapstructure:"application_codes"`
	// StorageID names a storage extension used to persist the poll checkpoint across restarts.
	StorageID *component.ID `mapstructure:"storage"`
}

var (
	errNoEndpoint     = errors.New("endpoint must be specified")
	errNoAPIKey       = errors.New("api_key must be specified")
	errNoTokenURL     = errors.New("token_url must be specified")
	errNoClientID     = errors.New("client_id must be specified")
	errNoClientSecret = errors.New("client_secret must be specified")
)

func (c *Config) Validate() error {
	var errs []error
	if c.Endpoint == "" {
		errs = append(errs, errNoEndpoint)
	}
	if c.APIKey == "" {
		errs = append(errs, errNoAPIKey)
	}
	if c.TokenURL == "" {
		errs = append(errs, errNoTokenURL)
	}
	if c.ClientID == "" {
		errs = append(errs, errNoClientID)
	}
	if c.ClientSecret == "" {
		errs = append(errs, errNoClientSecret)
	}
	// The API rejects more than one query per minute.
	if c.PollInterval < time.Minute {
		errs = append(errs, fmt.Errorf("poll_interval must be at least 1m, got %s", c.PollInterval))
	}
	if c.InitialLookback <= 0 {
		errs = append(errs, fmt.Errorf("initial_lookback must be positive, got %s", c.InitialLookback))
	}
	if c.PageSize <= 0 {
		errs = append(errs, fmt.Errorf("page_size must be positive, got %d", c.PageSize))
	}
	return errors.Join(errs...)
}
