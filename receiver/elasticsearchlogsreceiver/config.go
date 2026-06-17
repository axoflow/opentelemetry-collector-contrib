// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearchlogsreceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/elasticsearchlogsreceiver"

import (
	"errors"
	"fmt"
	"net/url"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/config/confighttp"
	"go.opentelemetry.io/collector/config/configopaque"
)

const (
	startAtBeginning = "beginning"
	startAtEnd       = "end"
)

var (
	errEmptyEndpoint        = errors.New("endpoint must be specified")
	errEndpointBadScheme    = errors.New("endpoint scheme must be http or https")
	errUsernameNotSpecified = errors.New("password was specified, but not username")
	errPasswordNotSpecified = errors.New("username was specified, but not password")
	errBasicAndAPIKey       = errors.New("only one of basic auth (username/password) or api_key may be specified")
	errNoIndices            = errors.New("at least one entry must be specified in 'indices'")
	errNoTimestampField     = errors.New("'timestamp_field' must be specified")
	errSortEmpty            = errors.New("'sort' must contain at least one field; for reliable search_after pagination the last entry should be a field that is unique per document")
	errSortBadOrder         = errors.New("each 'sort' entry must map exactly one field to either 'asc' or 'desc'")
	errBadPageSize          = errors.New("'page_size' must be greater than 0")
	errBadPollInterval      = errors.New("'poll_interval' must be greater than 0")
	errBadStartAt           = fmt.Errorf("'start_at' must be one of %q or %q", startAtBeginning, startAtEnd)
)

// Config defines the configuration for the Elasticsearch logs receiver.
type Config struct {
	// ClientConfig provides the endpoint, TLS, timeout and other HTTP client
	// settings used to reach Elasticsearch.
	confighttp.ClientConfig `mapstructure:",squash"`

	// Username is the username used for HTTP basic auth. Must be set together with Password.
	Username string `mapstructure:"username"`
	// Password is the password used for HTTP basic auth. Must be set together with Username.
	Password configopaque.String `mapstructure:"password"`
	// APIKey is an Elasticsearch API key. When set it is sent as an "Authorization: ApiKey <key>"
	// header. It is mutually exclusive with Username/Password.
	APIKey configopaque.String `mapstructure:"api_key"`

	// Indices is the list of index or data-stream patterns to search (e.g. ["logs-*"]).
	Indices []string `mapstructure:"indices"`
	// Query is an optional raw Elasticsearch query DSL object. It is combined (ANDed) with the
	// time-range filter the receiver applies on the first poll. If empty, all documents match.
	Query map[string]any `mapstructure:"query"`

	// TimestampField is the document field used as the time cursor and for the initial range filter.
	TimestampField string `mapstructure:"timestamp_field"`
	// Sort is the stable sort applied to every search. Each entry maps a single field to "asc" or
	// "desc". At least one field is required. For reliable search_after pagination the last entry
	// should be a field that is unique per document so paging is deterministic.
	Sort []map[string]string `mapstructure:"sort"`
	// PageSize is the number of documents requested per _search call (the query "size").
	PageSize int `mapstructure:"page_size"`

	// PollInterval is how often a new search cycle is started.
	PollInterval time.Duration `mapstructure:"poll_interval"`
	// InitialDelay is how long to wait before the first poll after startup.
	InitialDelay time.Duration `mapstructure:"initial_delay"`

	// StartAt controls where to begin reading when no checkpoint exists yet. One of "beginning"
	// (read all matching history) or "end" (only read documents newer than now - initial_lookback).
	StartAt string `mapstructure:"start_at"`
	// InitialLookback, when StartAt is "end", is how far back from "now" to begin reading on a fresh
	// start. Zero means only documents ingested after the receiver starts.
	InitialLookback time.Duration `mapstructure:"initial_lookback"`

	// StorageID points to a storage extension used to persist the search_after cursor across
	// restarts. If unset, the cursor is kept in memory only and reading restarts on each boot.
	StorageID *component.ID `mapstructure:"storage"`
}

// Validate checks the receiver configuration is well formed.
func (cfg *Config) Validate() error {
	var errs []error

	if cfg.Endpoint == "" {
		errs = append(errs, errEmptyEndpoint)
	} else if u, err := url.Parse(cfg.Endpoint); err != nil {
		errs = append(errs, fmt.Errorf("invalid endpoint %q: %w", cfg.Endpoint, err))
	} else if u.Scheme != "http" && u.Scheme != "https" {
		errs = append(errs, errEndpointBadScheme)
	}

	if cfg.Username == "" && cfg.Password != "" {
		errs = append(errs, errUsernameNotSpecified)
	}
	if cfg.Password == "" && cfg.Username != "" {
		errs = append(errs, errPasswordNotSpecified)
	}
	if cfg.APIKey != "" && (cfg.Username != "" || cfg.Password != "") {
		errs = append(errs, errBasicAndAPIKey)
	}

	if len(cfg.Indices) == 0 {
		errs = append(errs, errNoIndices)
	}

	if cfg.TimestampField == "" {
		errs = append(errs, errNoTimestampField)
	}

	if len(cfg.Sort) == 0 {
		errs = append(errs, errSortEmpty)
	}
	for _, s := range cfg.Sort {
		if len(s) != 1 {
			errs = append(errs, errSortBadOrder)
			continue
		}
		for _, order := range s {
			if order != "asc" && order != "desc" {
				errs = append(errs, errSortBadOrder)
			}
		}
	}

	if cfg.PageSize <= 0 {
		errs = append(errs, errBadPageSize)
	}

	if cfg.PollInterval <= 0 {
		errs = append(errs, errBadPollInterval)
	}

	switch cfg.StartAt {
	case startAtBeginning, startAtEnd:
	default:
		errs = append(errs, errBadStartAt)
	}

	return errors.Join(errs...)
}
