// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package sftpreceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/sftpreceiver"

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/pkg/sftp"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/receiver"
	"go.opentelemetry.io/collector/receiver/receiverhelper"
	"go.uber.org/zap"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

const (
	transport   = "sftp"
	dialTimeout = 30 * time.Second
)

type sftpReceiver struct {
	cfg     *Config
	set     receiver.Settings
	next    consumer.Logs
	obsrecv *receiverhelper.ObsReport

	sshCfg *ssh.ClientConfig

	cancel context.CancelFunc
	done   chan struct{}
}

func newReceiver(cfg *Config, set receiver.Settings, next consumer.Logs) (*sftpReceiver, error) {
	obsrecv, err := receiverhelper.NewObsReport(receiverhelper.ObsReportSettings{
		ReceiverID:             set.ID,
		Transport:              transport,
		ReceiverCreateSettings: set,
	})
	if err != nil {
		return nil, err
	}
	return &sftpReceiver{cfg: cfg, set: set, next: next, obsrecv: obsrecv}, nil
}

func (r *sftpReceiver) Start(context.Context, component.Host) error {
	if err := r.setup(); err != nil {
		return err
	}
	pollCtx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.done = make(chan struct{})
	go func() {
		defer close(r.done)
		ticker := time.NewTicker(r.cfg.PollInterval)
		defer ticker.Stop()
		for {
			if err := r.poll(pollCtx); err != nil && pollCtx.Err() == nil {
				r.set.Logger.Error("SFTP poll failed", zap.Error(err))
			}
			select {
			case <-pollCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return nil
}

func (r *sftpReceiver) Shutdown(context.Context) error {
	if r.cancel == nil {
		return nil
	}
	r.cancel()
	<-r.done
	return nil
}

// setup prepares the SSH credentials.
func (r *sftpReceiver) setup() (err error) {
	r.sshCfg, err = r.cfg.sshClientConfig()
	return err
}

func (c *Config) sshClientConfig() (*ssh.ClientConfig, error) {
	auth := ssh.Password(string(c.Password))
	if c.KeyFile != "" {
		key, err := os.ReadFile(c.KeyFile)
		if err != nil {
			return nil, err
		}
		var signer ssh.Signer
		if c.Password != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(key, []byte(c.Password))
		} else {
			signer, err = ssh.ParsePrivateKey(key)
		}
		if err != nil {
			return nil, fmt.Errorf("parse key_file: %w", err)
		}
		auth = ssh.PublicKeys(signer)
	}
	cfg := &ssh.ClientConfig{
		User: c.Username,
		Auth: []ssh.AuthMethod{auth},
		//nolint:gosec // G106: explicit opt-in through ignore_host_key
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}
	if !c.IgnoreHostKey {
		verify, err := knownhosts.New(c.KnownHosts)
		if err != nil {
			return nil, err
		}
		cfg.HostKeyCallback = verify
		// Request a key type that known_hosts lists. Otherwise the client may negotiate
		// another type the server has and fail with a key mismatch.
		var known *knownhosts.KeyError
		if errors.As(verify(c.Endpoint, &net.TCPAddr{}, noKey{}), &known) {
			for _, k := range known.Want {
				if t := k.Key.Type(); t == ssh.KeyAlgoRSA {
					cfg.HostKeyAlgorithms = append(cfg.HostKeyAlgorithms, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA)
				} else {
					cfg.HostKeyAlgorithms = append(cfg.HostKeyAlgorithms, t)
				}
			}
		}
	}
	return cfg, nil
}

// noKey matches no host key, so verifying it reports the keys known_hosts lists for a host.
type noKey struct{}

func (noKey) Type() string { return "none" }

func (noKey) Marshal() []byte { return nil }

func (noKey) Verify([]byte, *ssh.Signature) error { return errors.New("not a key") }

// poll connects to the server.
func (r *sftpReceiver) poll(ctx context.Context) error {
	// ponytail: reconnects on every poll; keep the connection open if the handshake cost matters.
	conn, err := (&net.Dialer{Timeout: dialTimeout}).DialContext(ctx, "tcp", r.cfg.Endpoint)
	if err != nil {
		return err
	}
	defer conn.Close()
	// Closing the connection unblocks in-flight I/O on shutdown.
	defer context.AfterFunc(ctx, func() { conn.Close() })()

	// The deadline bounds the SSH handshake only.
	_ = conn.SetDeadline(time.Now().Add(dialTimeout))
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, r.cfg.Endpoint, r.sshCfg)
	if err != nil {
		return err
	}
	_ = conn.SetDeadline(time.Time{})
	sshClient := ssh.NewClient(sshConn, chans, reqs)
	defer sshClient.Close()
	client, err := sftp.NewClient(sshClient)
	if err != nil {
		return err
	}
	defer client.Close()
	return nil
}
