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
	file := filepath.Join(t.TempDir(), "app.log")
	cfg := &Config{
		Endpoint:   addr,
		Username:   "otel",
		Password:   "secret",
		KnownHosts: knownHosts,
		Include:    []string{filepath.Join(filepath.Dir(file), "*.log")},
	}
	sink := new(consumertest.LogsSink)
	newStarted := func() *sftpReceiver {
		r, err := newReceiver(cfg, receivertest.NewNopSettings(metadata.Type), sink)
		require.NoError(t, err)
		require.NoError(t, r.setup())
		return r
	}
	// pollAndExpect asserts on the bodies delivered by one poll.
	pollAndExpect := func(r *sftpReceiver, want ...string) {
		t.Helper()
		sink.Reset()
		require.NoError(t, r.poll(t.Context()))
		var got []string
		for _, logs := range sink.AllLogs() {
			for _, lr := range logs.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().All() {
				got = append(got, lr.Body().Str())
				path, _ := lr.Attributes().Get("log.file.path")
				require.Equal(t, file, path.Str())
			}
		}
		require.Equal(t, want, got)
	}
	write := func(flag int, content string) {
		t.Helper()
		f, err := os.OpenFile(file, flag|os.O_CREATE|os.O_WRONLY, 0o600)
		require.NoError(t, err)
		_, err = f.WriteString(content)
		require.NoError(t, err)
		require.NoError(t, f.Close())
	}

	// CRLF is stripped, blank lines are skipped, the unterminated tail waits for the file to settle.
	write(os.O_TRUNC, "one\r\ntwo\n\npart")
	r := newStarted()
	pollAndExpect(r, "one", "two")
	pollAndExpect(r, "part")
	pollAndExpect(r)

	write(os.O_APPEND, "\nthree\n")
	pollAndExpect(r, "three")

	// A refused batch is delivered again by the next poll.
	write(os.O_APPEND, "four\n")
	r.next = consumertest.NewErr(errors.New("pipeline is full"))
	require.Error(t, r.poll(t.Context()))
	r.next = sink
	pollAndExpect(r, "four")

	// A truncated file is read from the start.
	write(os.O_TRUNC, "new\n")
	pollAndExpect(r, "new")

	// The checkpoint of a deleted file is dropped.
	require.NoError(t, os.Remove(file))
	pollAndExpect(r)
	require.Empty(t, r.files)
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
