// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package sftpreceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/sftpreceiver"

import (
	"errors"
	"fmt"
	"path"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/config/configopaque"
)

type Config struct {
	// Endpoint is the host:port of the SFTP server.
	Endpoint string `mapstructure:"endpoint"`
	Username string `mapstructure:"username"`
	// Password authenticates the user, or decrypts KeyFile when both are set.
	Password configopaque.String `mapstructure:"password"`
	// KeyFile is the path to a private key used for authentication.
	KeyFile string `mapstructure:"key_file"`

	// KnownHosts is the path to the known_hosts file used to verify the server.
	KnownHosts string `mapstructure:"known_hosts"`
	// IgnoreHostKey disables server verification. Testing only.
	IgnoreHostKey bool `mapstructure:"ignore_host_key"`

	// Include lists glob patterns of remote files to read.
	Include []string `mapstructure:"include"`
	// PollInterval is the time between two pulls.
	PollInterval time.Duration `mapstructure:"poll_interval"`
	// DeleteAfterRead removes a remote file once it is fully delivered and stopped growing.
	DeleteAfterRead bool `mapstructure:"delete_after_read"`
	// StorageID is the storage extension that keeps read offsets across restarts.
	StorageID *component.ID `mapstructure:"storage"`
}

func (c *Config) Validate() error {
	var errs []error
	if c.Endpoint == "" {
		errs = append(errs, errors.New("endpoint is required"))
	}
	if c.Username == "" {
		errs = append(errs, errors.New("username is required"))
	}
	if c.Password == "" && c.KeyFile == "" {
		errs = append(errs, errors.New("password or key_file is required"))
	}
	if c.KnownHosts == "" && !c.IgnoreHostKey {
		errs = append(errs, errors.New("known_hosts is required unless ignore_host_key is set"))
	}
	if len(c.Include) == 0 {
		errs = append(errs, errors.New("include requires at least one glob"))
	}
	for _, p := range c.Include {
		if _, err := path.Match(p, ""); err != nil {
			errs = append(errs, fmt.Errorf("include %q: %w", p, err))
		}
	}
	if c.PollInterval <= 0 {
		errs = append(errs, errors.New("poll_interval must be positive"))
	}
	return errors.Join(errs...)
}
