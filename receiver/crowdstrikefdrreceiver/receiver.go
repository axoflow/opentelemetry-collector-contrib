// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package crowdstrikefdrreceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/crowdstrikefdrreceiver"

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/receiver"
	"go.opentelemetry.io/collector/receiver/receiverhelper"
	"go.uber.org/zap"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/crowdstrikefdrreceiver/internal/metadata"
)

// SQS long poll maximum; a shorter wait only burns API calls.
const waitTimeSeconds = 20

type sqsAPI interface {
	ReceiveMessage(ctx context.Context, params *sqs.ReceiveMessageInput, optFns ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessage(ctx context.Context, params *sqs.DeleteMessageInput, optFns ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
}

type s3API interface {
	HeadObject(ctx context.Context, params *s3.HeadObjectInput, optFns ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

// fdrNotification is the message FDR publishes once a batch of files has been written to S3.
// See: https://github.com/CrowdStrike/FDR
type fdrNotification struct {
	Bucket     string `json:"bucket"`
	PathPrefix string `json:"pathPrefix"`
	Files      []struct {
		Path string `json:"path"`
	} `json:"files"`
}

type fdrReceiver struct {
	cfg      *Config
	logger   *zap.Logger
	version  string
	consumer consumer.Logs
	obsrecv  *receiverhelper.ObsReport

	sqs sqsAPI
	s3  s3API

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func newReceiver(cfg *Config, settings receiver.Settings, logs consumer.Logs) (*fdrReceiver, error) {
	obsrecv, err := receiverhelper.NewObsReport(receiverhelper.ObsReportSettings{
		ReceiverID:             settings.ID,
		Transport:              "sqs",
		ReceiverCreateSettings: settings,
	})
	if err != nil {
		return nil, err
	}
	return &fdrReceiver{cfg: cfg, logger: settings.Logger, version: settings.BuildInfo.Version, consumer: logs, obsrecv: obsrecv}, nil
}

func (r *fdrReceiver) Start(ctx context.Context, _ component.Host) error {
	if r.sqs == nil {
		opts := []func(*config.LoadOptions) error{config.WithRegion(r.cfg.Region)}
		if r.cfg.AccessKeyID != "" {
			opts = append(opts, config.WithCredentialsProvider(
				credentials.NewStaticCredentialsProvider(r.cfg.AccessKeyID, string(r.cfg.SecretAccessKey), "")))
		}
		awsCfg, err := config.LoadDefaultConfig(ctx, opts...)
		if err != nil {
			return fmt.Errorf("failed to load AWS config: %w", err)
		}
		r.sqs = sqs.NewFromConfig(awsCfg)
		r.s3 = s3.NewFromConfig(awsCfg)
	}
	r.logger.Info("Starting CrowdStrike FDR receiver",
		zap.String("queue_url", r.cfg.QueueURL),
		zap.String("region", r.cfg.Region),
		zap.Bool("static_credentials", r.cfg.AccessKeyID != ""),
		zap.Duration("visibility_timeout", r.cfg.VisibilityTimeout),
		zap.Int32("max_number_of_messages", r.cfg.MaxNumberOfMessages))

	pollCtx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.wg.Go(func() { r.poll(pollCtx) })
	return nil
}

func (r *fdrReceiver) Shutdown(context.Context) error {
	if r.cancel != nil {
		r.cancel()
	}
	r.wg.Wait()
	return nil
}

func (r *fdrReceiver) poll(ctx context.Context) {
	for ctx.Err() == nil {
		r.logger.Debug("Polling SQS queue", zap.Int("wait_time_seconds", waitTimeSeconds))
		pollStart := time.Now()
		out, err := r.sqs.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:            aws.String(r.cfg.QueueURL),
			MaxNumberOfMessages: r.cfg.MaxNumberOfMessages,
			WaitTimeSeconds:     waitTimeSeconds,
			VisibilityTimeout:   int32(r.cfg.VisibilityTimeout.Seconds()),
		})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			r.logger.Warn("Failed to receive SQS messages", zap.Error(err))
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}
		r.logger.Debug("Received SQS messages", zap.Int("count", len(out.Messages)), zap.Duration("took", time.Since(pollStart)))
		for _, msg := range out.Messages {
			if ctx.Err() != nil {
				return
			}
			if !r.handleMessage(ctx, msg) {
				continue
			}
			if _, err := r.sqs.DeleteMessage(ctx, &sqs.DeleteMessageInput{
				QueueUrl:      aws.String(r.cfg.QueueURL),
				ReceiptHandle: msg.ReceiptHandle,
			}); err != nil {
				r.logger.Warn("Failed to delete SQS message", zap.Error(err))
				continue
			}
			r.logger.Debug("Deleted SQS message", zap.String("message_id", aws.ToString(msg.MessageId)))
		}
	}
}

// handleMessage ingests one FDR batch. It returns true when the message can be deleted from the queue.
// A batch that is not complete or failed to ingest is left queued and retried after the visibility timeout.
func (r *fdrReceiver) handleMessage(ctx context.Context, msg sqstypes.Message) bool {
	var batch fdrNotification
	if err := json.Unmarshal([]byte(aws.ToString(msg.Body)), &batch); err != nil || len(batch.Files) == 0 {
		// Anything else on the queue can never be processed, so drop it instead of retrying forever.
		r.logger.Warn("Discarding SQS message that is not an FDR batch notification",
			zap.String("message_id", aws.ToString(msg.MessageId)), zap.String("body", aws.ToString(msg.Body)), zap.Error(err))
		return true
	}
	logger := r.logger.With(
		zap.String("message_id", aws.ToString(msg.MessageId)),
		zap.String("bucket", batch.Bucket),
		zap.String("path_prefix", batch.PathPrefix))
	logger.Debug("Handling FDR batch notification", zap.Int("files", len(batch.Files)))

	// FDR marks a fully written batch with an empty _SUCCESS object.
	if _, err := r.s3.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(batch.Bucket),
		Key:    aws.String(batch.PathPrefix + "/_SUCCESS"),
	}); err != nil {
		logger.Info("FDR batch not complete yet, will retry", zap.Error(err))
		return false
	}
	logger.Debug("FDR batch _SUCCESS marker found")

	batchStart := time.Now()
	for i, f := range batch.Files {
		fileLogger := logger.With(zap.String("key", f.Path), zap.Int("file_index", i))
		fileLogger.Debug("Downloading FDR file")
		if err := r.ingestFile(ctx, fileLogger, batch.Bucket, f.Path); err != nil {
			fileLogger.Error("Failed to ingest FDR file, batch will be retried", zap.Error(err))
			return false
		}
	}
	logger.Info("Ingested FDR batch", zap.Int("files", len(batch.Files)), zap.Duration("took", time.Since(batchStart)))
	return true
}

func (r *fdrReceiver) ingestFile(ctx context.Context, logger *zap.Logger, bucket, key string) error {
	out, err := r.s3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return err
	}
	defer out.Body.Close()
	logger.Debug("Download started", zap.Int64("content_length", aws.ToInt64(out.ContentLength)))

	raw, err := io.ReadAll(out.Body)
	if err != nil {
		return err
	}
	data := raw
	if strings.HasSuffix(key, ".gz") {
		if data, err = gunzip(raw); err != nil {
			logger.Warn("Failed to decompress FDR file, payload attached base64 encoded",
				zap.String("payload_encoding", "base64"), zap.String("payload", base64.StdEncoding.EncodeToString(raw)), zap.Error(err))
			return err
		}
	}
	logger.Debug("Download finished", zap.Int("decompressed_bytes", len(data)))

	obsCtx := r.obsrecv.StartLogsOp(ctx)
	logs, err := decodeFile(data, r.version)
	if err != nil {
		r.obsrecv.EndLogsOp(obsCtx, metadata.Type.String(), 0, err)
		logger.Warn("Failed to decode FDR file, payload attached base64 encoded",
			zap.String("payload_encoding", "base64"), zap.String("payload", base64.StdEncoding.EncodeToString(data)), zap.Error(err))
		return err
	}
	logger.Debug("Decoded FDR file, delivering to pipeline", zap.Int("records", logs.LogRecordCount()))
	for i := 0; i < logs.ResourceLogs().Len(); i++ {
		attrs := logs.ResourceLogs().At(i).Resource().Attributes()
		attrs.PutStr("aws.s3.bucket", bucket)
		attrs.PutStr("aws.s3.key", key)
	}
	err = r.consumer.ConsumeLogs(ctx, logs)
	r.obsrecv.EndLogsOp(obsCtx, metadata.Type.String(), logs.LogRecordCount(), err)
	if err == nil {
		logger.Debug("Delivered FDR file to pipeline", zap.Int("records", logs.LogRecordCount()))
	}
	return err
}

func gunzip(raw []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	return io.ReadAll(gz)
}
