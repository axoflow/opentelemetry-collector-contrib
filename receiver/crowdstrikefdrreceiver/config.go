// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package crowdstrikefdrreceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/crowdstrikefdrreceiver"

import (
	"errors"
	"time"

	"go.opentelemetry.io/collector/config/configopaque"
)

type Config struct {
	// QueueURL is the SQS queue CrowdStrike hands out together with the FDR credentials.
	QueueURL string `mapstructure:"queue_url"`
	// Region is the AWS region of the queue and of the bucket named in its messages.
	Region string `mapstructure:"region"`
	// AccessKeyID and SecretAccessKey are the FDR credentials issued by CrowdStrike.
	// Leave both empty to use the default AWS credential chain.
	AccessKeyID     string              `mapstructure:"access_key_id"`
	SecretAccessKey configopaque.String `mapstructure:"secret_access_key"`
	// VisibilityTimeout is how long a batch notification stays hidden from other consumers while it is ingested.
	VisibilityTimeout time.Duration `mapstructure:"visibility_timeout"`
	// MaxNumberOfMessages is the number of batch notifications fetched per poll (1-10).
	MaxNumberOfMessages int32 `mapstructure:"max_number_of_messages"`

	// prevent unkeyed literal initialization
	_ struct{}
}

func (cfg *Config) Validate() error {
	var errs []error
	if cfg.QueueURL == "" {
		errs = append(errs, errors.New("queue_url is required"))
	}
	if cfg.Region == "" {
		errs = append(errs, errors.New("region is required"))
	}
	if (cfg.AccessKeyID == "") != (cfg.SecretAccessKey == "") {
		errs = append(errs, errors.New("access_key_id and secret_access_key must be set together"))
	}
	if cfg.VisibilityTimeout <= 0 || cfg.VisibilityTimeout > 12*time.Hour {
		errs = append(errs, errors.New("visibility_timeout must be between 1s and 12h"))
	}
	if cfg.MaxNumberOfMessages < 1 || cfg.MaxNumberOfMessages > 10 {
		errs = append(errs, errors.New("max_number_of_messages must be between 1 and 10"))
	}
	return errors.Join(errs...)
}
