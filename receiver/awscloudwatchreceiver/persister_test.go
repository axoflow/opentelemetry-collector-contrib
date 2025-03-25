// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package awscloudwatchreceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/awscloudwatchreceiver"

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/extension/xextension/storage"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

const logGroupName = "test-log-group"

// MockStorageClient is a mock implementation of the storage.Client interface
type mockStorageClient struct {
	cache      map[string][]byte
	cacheMux   sync.Mutex
	forceError bool
}

func newMockStorageClient() *mockStorageClient {
	return &mockStorageClient{
		cache: make(map[string][]byte),
	}
}

func (m *mockStorageClient) Get(_ context.Context, key string) ([]byte, error) {
	m.cacheMux.Lock()
	defer m.cacheMux.Unlock()

	if m.forceError {
		return nil, errors.New("forced storage error")
	}

	if value, ok := m.cache[key]; ok {
		return value, nil
	}

	return nil, errors.New("not found")
}

func (m *mockStorageClient) Set(_ context.Context, key string, value []byte) error {
	m.cacheMux.Lock()
	defer m.cacheMux.Unlock()

	if m.forceError {
		return errors.New("forced storage error")
	}

	m.cache[key] = value

	return nil
}

func (m *mockStorageClient) Delete(_ context.Context, key string) error {
	m.cacheMux.Lock()
	defer m.cacheMux.Unlock()

	if m.forceError {
		return errors.New("forced storage error")
	}

	delete(m.cache, key)

	return nil
}

func (m *mockStorageClient) Batch(_ context.Context, ops ...*storage.Operation) error {
	m.cacheMux.Lock()
	defer m.cacheMux.Unlock()

	if m.forceError {
		return errors.New("forced storage error")
	}

	for _, op := range ops {
		switch op.Type {
		case storage.Get:
			op.Value = m.cache[op.Key]
		case storage.Set:
			m.cache[op.Key] = op.Value
		case storage.Delete:
			delete(m.cache, op.Key)
		default:
			return errors.New("wrong operation type")
		}
	}

	return nil
}

func (m *mockStorageClient) Close(_ context.Context) error {
	m.cacheMux.Lock()
	defer m.cacheMux.Unlock()
	m.cache = nil
	return nil
}

func TestSetCheckpoint_StorageError(t *testing.T) {
	mockClient := newMockStorageClient()
	mockClient.forceError = true
	logger := zap.NewNop()
	persister := newCloudwatchCheckpointPersister(mockClient, logger)

	ctx := context.Background()
	timestamp := time.Now().Format(time.RFC3339)
	err := persister.SetCheckpoint(ctx, logGroupName, timestamp)
	assert.Error(t, err)
	assert.ErrorContains(t, err, "forced storage error")
}

func TestSetCheckpoint_NilClient(t *testing.T) {
	logger := zap.NewNop()
	persister := newCloudwatchCheckpointPersister(nil, logger)

	ctx := context.Background()
	timestamp := time.Now().Format(time.RFC3339)

	err := persister.SetCheckpoint(ctx, logGroupName, timestamp)
	assert.Error(t, err)
	assert.ErrorContains(t, err, "storage client is nil")
}

func TestGetCheckpoint(t *testing.T) {
	mockClient := newMockStorageClient()
	logger := zap.NewNop()
	persister := newCloudwatchCheckpointPersister(mockClient, logger)

	ctx := context.Background()
	timestamp := time.Now().Format(time.RFC3339)

	err := persister.SetCheckpoint(ctx, logGroupName, timestamp)
	assert.NoError(t, err)

	result, err := persister.GetCheckpoint(ctx, logGroupName)
	assert.NoError(t, err)
	assert.Equal(t, timestamp, result)
}

func TestGetCheckpoint_NotFound(t *testing.T) {
	mockClient := newMockStorageClient()
	logger := zap.NewNop()
	persister := newCloudwatchCheckpointPersister(mockClient, logger)

	ctx := context.Background()
	result, err := persister.GetCheckpoint(ctx, "non-existent-group")
	assert.NoError(t, err)
	assert.Equal(t, newCheckpointTimeFromStartOfStream(), result)
}

func TestGetCheckpoint_EmptyValue(t *testing.T) {
	mockClient := newMockStorageClient()
	logger := zap.NewNop()
	persister := newCloudwatchCheckpointPersister(mockClient, logger)

	ctx := context.Background()
	err := persister.SetCheckpoint(ctx, logGroupName, "") // Set an empty value
	assert.Error(t, err)
	assert.ErrorContains(t, err, "timestamp is empty")

	result, err := persister.GetCheckpoint(ctx, logGroupName)
	assert.NoError(t, err)
	assert.Equal(t, newCheckpointTimeFromStartOfStream(), result)
}

func TestGetCheckpoint_NilClient(t *testing.T) {
	logger := zap.NewNop()
	persister := newCloudwatchCheckpointPersister(nil, logger)

	ctx := context.Background()
	_, err := persister.GetCheckpoint(ctx, logGroupName)
	assert.Error(t, err)
	assert.ErrorContains(t, err, "storage client is nil")
}

func TestGetCheckpoint_StorageError(t *testing.T) {
	mockClient := newMockStorageClient()
	mockClient.forceError = true
	logger := zap.NewNop()
	persister := newCloudwatchCheckpointPersister(mockClient, logger)

	// Nothing is set
	ctx := context.Background()
	result, err := persister.GetCheckpoint(ctx, logGroupName)
	assert.NoError(t, err)
	assert.Equal(t, newCheckpointTimeFromStartOfStream(), result)
}

func TestSetCheckpoint_EmptyLogGroupName(t *testing.T) {
	mockClient := newMockStorageClient()
	logger := zap.NewNop()
	persister := newCloudwatchCheckpointPersister(mockClient, logger)

	ctx := context.Background()
	timestamp := time.Now().Format(time.RFC3339)

	err := persister.SetCheckpoint(ctx, "", timestamp)
	assert.Error(t, err)
	assert.ErrorContains(t, err, "checkpoint key is empty")
}

func TestValidateCheckpoint_Logging(t *testing.T) {
	core, logs := observer.New(zap.DebugLevel)
	logger := zap.New(core)

	mockClient := newMockStorageClient()
	persister := newCloudwatchCheckpointPersister(mockClient, logger)

	// Test error case
	persister.validateCheckpoint(logGroupName, nil, errors.New("test error"))
	errorLogs := logs.FilterMessage("Error retrieving checkpoint, starting from the beginning").All()
	require.Len(t, errorLogs, 1)
	assert.Equal(t, "test error", errorLogs[0].ContextMap()["error"])

	// Test nil data case
	logs.TakeAll() // Clear logs
	persister.validateCheckpoint(logGroupName, nil, nil)
	nilLogs := logs.FilterMessage("No checkpoint found, starting from the beginning").All()
	require.Len(t, nilLogs, 1)

	// Test empty data case
	logs.TakeAll() // Clear logs
	persister.validateCheckpoint(logGroupName, []byte{}, nil)
	emptyLogs := logs.FilterMessage("Checkpoint key exists but value is empty, starting from the beginning").All()
	require.Len(t, emptyLogs, 1)
}
