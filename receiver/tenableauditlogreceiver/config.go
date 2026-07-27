// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package tenableauditlogreceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/tenableauditlogreceiver"

import (
	"errors"
	"fmt"
	"net/url"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/config/confighttp"
	"go.opentelemetry.io/collector/config/configopaque"
)

// maxPageSize is the largest `limit` the Tenable audit log API accepts.
const maxPageSize = 5000

// Config defines the configuration of the Tenable audit log receiver.
type Config struct {
	confighttp.ClientConfig `mapstructure:",squash"`

	// AccessKey and SecretKey are the Tenable API keys used for the X-ApiKeys header.
	AccessKey configopaque.String `mapstructure:"access_key"`
	SecretKey configopaque.String `mapstructure:"secret_key"`

	// PollInterval is how often the audit log API is queried.
	PollInterval time.Duration `mapstructure:"poll_interval"`

	// PageSize is the number of events requested per API call.
	PageSize int `mapstructure:"page_size"`

	// MaxRecordsPerPoll caps how many events a single poll collects. Remaining events are
	// picked up by the following poll.
	MaxRecordsPerPoll int `mapstructure:"max_records_per_poll"`

	// InitialLookback is how far back events are collected when no checkpoint exists.
	// The Tenable audit log only retains 30 days of events.
	InitialLookback time.Duration `mapstructure:"initial_lookback"`

	// StorageID is the storage extension used to persist the checkpoint across restarts.
	StorageID *component.ID `mapstructure:"storage"`
}

func (c *Config) Validate() error {
	var errs []error
	if c.Endpoint == "" {
		errs = append(errs, errors.New("endpoint must be specified"))
	} else if _, err := url.Parse(c.Endpoint); err != nil {
		errs = append(errs, fmt.Errorf("invalid endpoint: %w", err))
	}
	if c.AccessKey == "" {
		errs = append(errs, errors.New("access_key must be specified"))
	}
	if c.SecretKey == "" {
		errs = append(errs, errors.New("secret_key must be specified"))
	}
	if c.PollInterval <= 0 {
		errs = append(errs, errors.New("poll_interval must be positive"))
	}
	if c.PageSize <= 0 || c.PageSize > maxPageSize {
		errs = append(errs, fmt.Errorf("page_size must be between 1 and %d", maxPageSize))
	}
	if c.MaxRecordsPerPoll <= 0 {
		errs = append(errs, errors.New("max_records_per_poll must be positive"))
	}
	if c.InitialLookback <= 0 {
		errs = append(errs, errors.New("initial_lookback must be positive"))
	}
	return errors.Join(errs...)
}
