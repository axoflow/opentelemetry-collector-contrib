// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package sftpreceiver

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/receiver/receivertest"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/sftpreceiver/internal/metadata"
)

func TestPoll(t *testing.T) {
	addr, knownHosts := startSFTPServer(t)
	cfg := &Config{
		Endpoint:   addr,
		Username:   "otel",
		Password:   "secret",
		KnownHosts: knownHosts,
	}
	r, err := newReceiver(cfg, receivertest.NewNopSettings(metadata.Type), consumertest.NewNop())
	require.NoError(t, err)
	require.NoError(t, r.setup())
	require.NoError(t, r.poll(t.Context()))
}

// startSFTPServer serves the local filesystem over SFTP and returns its address
// and a known_hosts file that trusts it.
func startSFTPServer(t *testing.T) (addr, knownHosts string) {
	// The client prefers ECDSA host keys, but known_hosts only lists the RSA one.
	ecdsaKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if c.User() == "otel" && string(password) == "secret" {
				return nil, nil
			}
			return nil, errors.New("access denied")
		},
	}
	var signer ssh.Signer
	for _, key := range []any{ecdsaKey, rsaKey} {
		signer, err = ssh.NewSignerFromKey(key)
		require.NoError(t, err)
		cfg.AddHostKey(signer)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveSFTP(conn, cfg)
		}
	}()

	addr = ln.Addr().String()
	knownHosts = filepath.Join(t.TempDir(), "known_hosts")
	line := knownhosts.Line([]string{addr}, signer.PublicKey())
	require.NoError(t, os.WriteFile(knownHosts, []byte(line+"\n"), 0o600))
	return addr, knownHosts
}

func serveSFTP(conn net.Conn, cfg *ssh.ServerConfig) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Minute))
	_, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for newChannel := range chans {
		channel, requests, err := newChannel.Accept()
		if err != nil {
			return
		}
		go func() {
			for req := range requests {
				_ = req.Reply(req.Type == "subsystem", nil)
			}
		}()
		server, err := sftp.NewServer(channel)
		if err != nil {
			return
		}
		_ = server.Serve()
		_ = channel.Close()
	}
}
