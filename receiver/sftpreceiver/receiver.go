// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package sftpreceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/sftpreceiver"

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/consumer/consumererror"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/receiver"
	"go.opentelemetry.io/collector/receiver/receiverhelper"
	"go.uber.org/zap"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

const (
	transport    = "sftp"
	dialTimeout  = 30 * time.Second
	maxLineSize  = 1 << 20 // longer lines are split, like filelog's max_log_size
	maxBatchSize = 1000    // log records per ConsumeLogs call
)

// fileState is the checkpoint of one remote file.
type fileState struct {
	Offset int64 // bytes already delivered
	Size   int64 // size seen by the previous poll
}

type sftpReceiver struct {
	cfg     *Config
	set     receiver.Settings
	next    consumer.Logs
	obsrecv *receiverhelper.ObsReport

	sshCfg *ssh.ClientConfig
	files  map[string]fileState // only touched by the polling goroutine

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

// setup prepares the SSH credentials and the file states.
func (r *sftpReceiver) setup() (err error) {
	r.files = map[string]fileState{}
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

// poll connects to the server and delivers what was added to the matching files since the last poll.
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

	var errs []error
	seen := map[string]bool{}
	for _, pattern := range r.cfg.Include {
		// Glob swallows I/O errors, so an empty result does not mean the files are gone.
		paths, err := client.Glob(pattern)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, p := range paths {
			if seen[p] {
				continue
			}
			seen[p] = true
			if err := r.readFile(ctx, client, p); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", p, err))
			}
		}
	}

	// Forget a file only when the server confirms it no longer exists.
	for p := range r.files {
		if seen[p] {
			continue
		}
		if _, err := client.Stat(p); errors.Is(err, os.ErrNotExist) {
			delete(r.files, p)
		}
	}
	return errors.Join(errs...)
}

// readFile delivers the lines of p that follow its checkpoint.
// The checkpoint only advances over lines the pipeline accepted.
func (r *sftpReceiver) readFile(ctx context.Context, client *sftp.Client, p string) error {
	info, err := client.Stat(p)
	if err != nil || !info.Mode().IsRegular() {
		return err
	}
	st, tracked := r.files[p]
	size := info.Size()
	if size < st.Offset {
		// ponytail: truncation is the only rotation detected; a file replaced by a larger one
		// is read from the old offset. Add a content fingerprint if files get replaced in place.
		st.Offset = 0
	}
	// A file that kept its size since the previous poll is taken as complete,
	// even when its last line has no newline.
	idle := tracked && size == st.Size
	st.Size = size
	r.files[p] = st
	if st.Offset < size {
		return r.readLines(ctx, client, p, idle)
	}
	return nil
}

// readLines delivers the lines between the checkpoint of p and the size seen by this poll.
func (r *sftpReceiver) readLines(ctx context.Context, client *sftp.Client, p string, idle bool) error {
	st := r.files[p]
	f, err := client.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = f.Seek(st.Offset, io.SeekStart); err != nil {
		return err
	}
	br := bufio.NewReaderSize(io.LimitReader(f, st.Size-st.Offset), maxLineSize)

	var pending int64 // bytes read since the last flush
	logs, records := newLogs()
	flush := func() error {
		if records.Len() > 0 {
			if err := r.consume(ctx, logs); err != nil {
				return err
			}
			logs, records = newLogs()
		}
		if pending == 0 {
			return nil
		}
		st.Offset += pending
		pending = 0
		r.files[p] = st
		return nil
	}

	now := pcommon.NewTimestampFromTime(time.Now())
	for {
		// ErrBufferFull returns a maxLineSize chunk of an oversized line.
		line, err := br.ReadSlice('\n')
		eof := errors.Is(err, io.EOF)
		if err != nil && !eof && !errors.Is(err, bufio.ErrBufferFull) {
			return errors.Join(err, flush())
		}
		if eof && !idle {
			break // the unterminated tail may still be growing
		}
		pending += int64(len(line))
		if body := bytes.TrimRight(line, "\r\n"); len(body) > 0 {
			lr := records.AppendEmpty()
			lr.SetObservedTimestamp(now)
			lr.Body().SetStr(strings.ToValidUTF8(string(body), "\uFFFD"))
			lr.Attributes().PutStr("log.file.path", p)
		}
		if eof {
			break
		}
		if records.Len() >= maxBatchSize {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	return flush()
}

func newLogs() (plog.Logs, plog.LogRecordSlice) {
	logs := plog.NewLogs()
	return logs, logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords()
}

func (r *sftpReceiver) consume(ctx context.Context, logs plog.Logs) error {
	n := logs.LogRecordCount()
	obsCtx := r.obsrecv.StartLogsOp(ctx)
	err := r.next.ConsumeLogs(obsCtx, logs)
	r.obsrecv.EndLogsOp(obsCtx, transport, n, err)
	if consumererror.IsPermanent(err) {
		// Retrying cannot succeed, and holding the checkpoint would block the file forever.
		r.set.Logger.Error("Dropping logs rejected by the pipeline", zap.Error(err))
		return nil
	}
	return err
}
