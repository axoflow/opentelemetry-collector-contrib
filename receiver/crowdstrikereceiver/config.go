// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package crowdstrikereceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/crowdstrikereceiver"

import (
	"errors"
	"time"

	"go.opentelemetry.io/collector/config/configopaque"
	"go.opentelemetry.io/collector/config/configtls"
)

const defaultPollInterval = 30 * time.Second

// defaultSearchQuery matches every event in the repository.
const defaultSearchQuery = "*"

var (
	errNoCredentials    = errors.New("either access_token or both client_id and client_secret must be set")
	errNoTokenHost      = errors.New("access_token needs either cloud or host_override: nothing in a token identifies the cloud, so it cannot be autodiscovered")
	errNoPollInterval   = errors.New("poll_interval must be positive")
	errNegativeLookback = errors.New("initial_lookback must not be negative")
	errNoSource         = errors.New("nothing to collect: disable_alerts is set and ngsiem_search::repository is empty")
)

// NGSIEMSearchConfig configures pulling log events from an NG-SIEM repository
// via the query-jobs API.
type NGSIEMSearchConfig struct {
	// Repository is the NG-SIEM repository (view) to query, e.g. "third-party".
	// Setting it enables the NG-SIEM search poller.
	Repository string `mapstructure:"repository"`

	// QueryString is the CQL filter selecting the events to pull.
	// Defaults to a match-all query. Aggregating functions must not be used
	// here, as each matched event is emitted as one log record.
	QueryString string `mapstructure:"query_string"`
}

type Config struct {
	// AccessToken is the access token used to access the CrowdStrike Falcon platform.
	// If used, either Cloud or HostOverride must be provided.
	// *required* if ClientID and ClientSecret are empty.
	AccessToken configopaque.String `mapstructure:"access_token"`

	// ClientID used for authentication with CrowdStrike Falcon platform.
	// *required* if AccessToken is empty.
	ClientID string `mapstructure:"client_id"`
	// ClientSecret used for authentication with CrowdStrike Falcon platform.
	// *required* if AccessToken is empty.
	ClientSecret configopaque.String `mapstructure:"client_secret"`

	// MemberCID is an optional CID selector for cases when the ClientID/ClientSecret
	// has access to multiple CIDs.
	MemberCID string `mapstructure:"member_cid"`

	// Cloud specifies the Falcon Cloud to connect to (e.g., "us-1", "us-2", "eu-1").
	Cloud string `mapstructure:"cloud"`

	// HostOverride allows to override host. Cloud will be ignored.
	HostOverride string `mapstructure:"host_override"`
	// BasePathOverride allows to override default base path
	BasePathOverride string `mapstructure:"base_path_override"`

	// PollInterval specifies how often to poll the CrowdStrike API for new data.
	PollInterval time.Duration `mapstructure:"poll_interval"`

	// InitialLookback bounds how far back the first poll reaches. Zero means
	// only data arriving after the receiver starts is collected.
	InitialLookback time.Duration `mapstructure:"initial_lookback"`

	// DisableAlerts turns off the Alerts API poller.
	DisableAlerts bool `mapstructure:"disable_alerts"`

	// NGSIEMSearch enables pulling log events from an NG-SIEM repository.
	NGSIEMSearch NGSIEMSearchConfig `mapstructure:"ngsiem_search"`

	// Debug enables debug logging of all HTTP traffic going through the API runtime.
	Debug bool `mapstructure:"debug"`

	// TLS settings
	TLS configtls.ClientConfig `mapstructure:"tls,omitempty"`
}

func (c *Config) Validate() error {
	var errs error
	if c.AccessToken == "" && (c.ClientID == "" || c.ClientSecret == "") {
		errs = errors.Join(errs, errNoCredentials)
	}
	// Client credentials can autodiscover the cloud, a token cannot: the SDK
	// refuses to build a client for it, which would only surface at startup.
	if c.AccessToken != "" && c.Cloud == "" && c.HostOverride == "" {
		errs = errors.Join(errs, errNoTokenHost)
	}
	// A zero poll_interval would panic time.NewTicker rather than fail
	// config validation.
	if c.PollInterval <= 0 {
		errs = errors.Join(errs, errNoPollInterval)
	}
	if c.InitialLookback < 0 {
		errs = errors.Join(errs, errNegativeLookback)
	}
	if c.DisableAlerts && c.NGSIEMSearch.Repository == "" {
		errs = errors.Join(errs, errNoSource)
	}
	return errs
}
