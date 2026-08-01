// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package crowdstrikereceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/crowdstrikereceiver"

import (
	"time"

	"go.opentelemetry.io/collector/config/configtls"
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

type CrowdstrikeReceiverConfig struct {
	// AccessToken is the access token used to access the CrowdStrike Falcon platform.
	// If used, Cloud must be provided.
	// *required* if ClientID and ClientSecret are empty.
	AccessToken string `mapstructure:"access_token"`

	// ClientID used for authentication with CrowdStrike Falcon platform.
	// *required* if AccessToken is empty.
	ClientID string `mapstructure:"client_id"`
	// ClientSecret used for authentication with CrowdStrike Falcon platform.
	// *required* if AccessToken is empty.
	ClientSecret string `mapstructure:"client_secret"`

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
	PollInterval *time.Duration `mapstructure:"poll_interval"`

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
