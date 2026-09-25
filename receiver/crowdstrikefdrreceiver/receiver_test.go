// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package crowdstrikefdrreceiver

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"io"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/receiver/receivertest"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/crowdstrikefdrreceiver/internal/metadata"
)

const batchMessage = `{"cid":"abc","timestamp":1594323680000,"fileCount":2,"totalSize":42,
	"bucket":"cs-bucket","pathPrefix":"data/batch-1",
	"files":[{"path":"data/batch-1/part-00000.gz","size":21,"checksum":"x"},{"path":"data/batch-1/part-00001.gz","size":21,"checksum":"y"}]}`

// fakeSQS hands out the given messages once, then blocks until the receiver shuts down.
type fakeSQS struct {
	messages []sqstypes.Message
	deleted  []string
}

func (f *fakeSQS) ReceiveMessage(ctx context.Context, _ *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	if f.messages != nil {
		out := &sqs.ReceiveMessageOutput{Messages: f.messages}
		f.messages = nil
		return out, nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (f *fakeSQS) DeleteMessage(_ context.Context, in *sqs.DeleteMessageInput, _ ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	f.deleted = append(f.deleted, aws.ToString(in.ReceiptHandle))
	return &sqs.DeleteMessageOutput{}, nil
}

type fakeS3 struct {
	objects map[string][]byte
}

func (f *fakeS3) HeadObject(_ context.Context, in *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	if _, ok := f.objects[aws.ToString(in.Key)]; !ok {
		return nil, &s3types.NoSuchKey{}
	}
	return &s3.HeadObjectOutput{}, nil
}

func (f *fakeS3) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	data, ok := f.objects[aws.ToString(in.Key)]
	if !ok {
		return nil, &s3types.NoSuchKey{}
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(data))}, nil
}

func gz(t *testing.T, s string) []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	_, err := w.Write([]byte(s))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return buf.Bytes()
}

func run(t *testing.T, sqsClient *fakeSQS, s3Client *fakeS3, loggers ...*zap.Logger) *consumertest.LogsSink {
	cfg := createDefaultConfig().(*Config)
	cfg.QueueURL = "https://sqs.us-west-1.amazonaws.com/123/queue"
	cfg.Region = "us-west-1"

	sink := new(consumertest.LogsSink)
	settings := receivertest.NewNopSettings(metadata.Type)
	if len(loggers) > 0 {
		settings.Logger = loggers[0]
	}
	r, err := newReceiver(cfg, settings, sink)
	require.NoError(t, err)
	r.sqs, r.s3 = sqsClient, s3Client

	require.NoError(t, r.Start(t.Context(), componenttest.NewNopHost()))
	// Give the poll loop time to process the single batch of messages; the fake then blocks.
	time.Sleep(100 * time.Millisecond)
	require.NoError(t, r.Shutdown(t.Context()))
	return sink
}

func message(body string) []sqstypes.Message {
	return []sqstypes.Message{{Body: aws.String(body), ReceiptHandle: aws.String("rh-1")}}
}

func TestCompleteBatch(t *testing.T) {
	sqsClient := &fakeSQS{messages: message(batchMessage)}
	s3Client := &fakeS3{objects: map[string][]byte{
		"data/batch-1/_SUCCESS":      {},
		"data/batch-1/part-00000.gz": gz(t, `{"n":"one"}`+"\n"+`{"n":"two"}`+"\n"),
		"data/batch-1/part-00001.gz": gz(t, `{"n":"three"}`+"\n"),
	}}

	sink := run(t, sqsClient, s3Client)

	assert.Equal(t, []string{"rh-1"}, sqsClient.deleted)
	require.Equal(t, 3, sink.LogRecordCount())
	first := sink.AllLogs()[0].ResourceLogs().At(0)
	bucket, _ := first.Resource().Attributes().Get("aws.s3.bucket")
	key, _ := first.Resource().Attributes().Get("aws.s3.key")
	assert.Equal(t, "cs-bucket", bucket.Str())
	assert.Equal(t, "data/batch-1/part-00000.gz", key.Str())
	n, _ := first.ScopeLogs().At(0).LogRecords().At(0).Body().Map().Get("n")
	assert.Equal(t, "one", n.Str())
}

func TestIncompleteBatchStaysQueued(t *testing.T) {
	sqsClient := &fakeSQS{messages: message(batchMessage)}
	s3Client := &fakeS3{objects: map[string][]byte{"data/batch-1/part-00000.gz": gz(t, `{"n":"one"}`+"\n")}}

	sink := run(t, sqsClient, s3Client)

	assert.Empty(t, sqsClient.deleted)
	assert.Equal(t, 0, sink.LogRecordCount())
}

func TestDecodeFailureStaysQueued(t *testing.T) {
	core, observed := observer.New(zap.WarnLevel)
	sqsClient := &fakeSQS{messages: message(batchMessage)}
	s3Client := &fakeS3{objects: map[string][]byte{
		"data/batch-1/_SUCCESS":      {},
		"data/batch-1/part-00000.gz": gz(t, `{"n":"one"}`+"\n"),
		"data/batch-1/part-00001.gz": gz(t, "not json\n"),
	}}

	sink := run(t, sqsClient, s3Client, zap.New(core))

	assert.Empty(t, sqsClient.deleted)
	assert.Equal(t, 1, sink.LogRecordCount(), "the good file is delivered before the failure is detected")

	warnings := observed.FilterMessageSnippet("decode").All()
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0].Message, "base64")
	fields := warnings[0].ContextMap()
	assert.Equal(t, "base64", fields["payload_encoding"])
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte("not json\n")), fields["payload"])
	assert.Equal(t, "data/batch-1/part-00001.gz", fields["key"])
	assert.NotEmpty(t, fields["error"])
}

func TestDecompressFailureLogsPayload(t *testing.T) {
	core, observed := observer.New(zap.WarnLevel)
	sqsClient := &fakeSQS{messages: message(batchMessage)}
	notGzip := []byte("plain text, not gzip")
	s3Client := &fakeS3{objects: map[string][]byte{
		"data/batch-1/_SUCCESS":      {},
		"data/batch-1/part-00000.gz": notGzip,
		"data/batch-1/part-00001.gz": gz(t, `{"n":"two"}`+"\n"),
	}}

	sink := run(t, sqsClient, s3Client, zap.New(core))

	assert.Empty(t, sqsClient.deleted, "batch must stay queued")
	assert.Equal(t, 0, sink.LogRecordCount(), "ingestion stops at the first broken file")

	warnings := observed.FilterMessageSnippet("decompress").All()
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0].Message, "base64")
	fields := warnings[0].ContextMap()
	assert.Equal(t, "base64", fields["payload_encoding"])
	assert.Equal(t, base64.StdEncoding.EncodeToString(notGzip), fields["payload"])
	assert.Equal(t, "data/batch-1/part-00000.gz", fields["key"])
	assert.NotEmpty(t, fields["error"])
}

func TestForeignMessageIsDiscarded(t *testing.T) {
	sqsClient := &fakeSQS{messages: message(`{"Records":[]}`)}

	sink := run(t, sqsClient, &fakeS3{})

	assert.Equal(t, []string{"rh-1"}, sqsClient.deleted)
	assert.Equal(t, 0, sink.LogRecordCount())
}
