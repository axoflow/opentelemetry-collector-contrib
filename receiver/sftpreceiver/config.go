// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package sftpreceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/sftpreceiver"

import (
	"bufio"
	"errors"
	"fmt"
	"path"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/config/configopaque"
	"golang.org/x/text/encoding"

	"github.com/open-telemetry/opentelemetry-collector-contrib/internal/coreinternal/textutils"
	"github.com/open-telemetry/opentelemetry-collector-contrib/pkg/stanza/split"
	"github.com/open-telemetry/opentelemetry-collector-contrib/pkg/stanza/trim"
)

const defaultLineLength = 1 << 20

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
	// MaxLineLength is the longest log record in bytes. Longer records are split,
	// so a file without newlines cannot exhaust memory.
	MaxLineLength int `mapstructure:"max_line_length"`
	// Encoding is the character set of the files, as in the filelog receiver.
	Encoding string `mapstructure:"encoding"`
	// Multiline groups several lines into one record, as in the filelog receiver.
	Multiline split.Config `mapstructure:"multiline"`
	// TrimConfig controls the whitespace removed around each record, as in the filelog receiver.
	TrimConfig trim.Config `mapstructure:",squash"`
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
	if c.MaxLineLength <= 0 {
		errs = append(errs, errors.New("max_line_length must be positive"))
	}
	if _, err := c.splitFunc(true); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// splitFunc cuts the records of a file. flushAtEOF also emits the last one,
// which would otherwise wait for the start of the next record.
func (c *Config) splitFunc(flushAtEOF bool) (bufio.SplitFunc, error) {
	enc, err := c.encoding()
	if err != nil {
		return nil, err
	}
	if enc == encoding.Nop {
		// ponytail: nop emits raw byte chunks regardless of line boundaries; add when someone pulls binary files.
		return nil, errors.New("encoding nop is not supported")
	}
	splitFunc, err := c.Multiline.Func(enc, flushAtEOF, c.MaxLineLength)
	if err != nil {
		return nil, fmt.Errorf("multiline: %w", err)
	}
	// Same order as the filelog receiver: cut to length, then trim.
	return trim.WithFunc(trim.ToLength(splitFunc, c.MaxLineLength), c.TrimConfig.Func()), nil
}

func (c *Config) encoding() (encoding.Encoding, error) {
	return textutils.LookupEncoding(c.Encoding)
}
