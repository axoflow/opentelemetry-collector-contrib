// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package crowdstrikereceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/crowdstrikereceiver"

import (
	"testing"
	"time"

	"github.com/crowdstrike/gofalcon/falcon/models"
	"github.com/go-openapi/strfmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.uber.org/zap/zaptest"

	"github.com/open-telemetry/opentelemetry-collector-contrib/extension/storage/storagetest"
	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/crowdstrikereceiver/internal/metadata"
)

func inMemoryClient() *storagetest.TestClient {
	return storagetest.NewInMemoryClient(component.KindReceiver, component.NewID(metadata.Type), "")
}

func TestCheckpointStoreLoad(t *testing.T) {
	fallback := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	stored := time.Date(2026, 8, 2, 18, 14, 5, 0, time.UTC)
	storedJSON, err := stored.MarshalJSON()
	require.NoError(t, err)

	cases := []struct {
		name     string
		stored   []byte
		closed   bool
		expected time.Time
	}{
		{name: "absent", expected: fallback},
		{name: "present", stored: storedJSON, expected: stored},
		{name: "corrupt", stored: []byte("not a timestamp"), expected: fallback},
		// A client whose extension is already gone fails every read.
		{name: "unreadable", closed: true, expected: fallback},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := inMemoryClient()
			if tc.stored != nil {
				require.NoError(t, client.Set(t.Context(), alertCheckpointKey, tc.stored))
			}
			if tc.closed {
				require.NoError(t, client.Close(t.Context()))
			}

			store := newCheckpointStore(client, zaptest.NewLogger(t))
			assert.Equal(t, tc.expected, store.load(t.Context(), alertCheckpointKey, fallback).UTC())
		})
	}
}

// A failed write must not take the poller down: the batch is already
// delivered, so the only cost is re-delivery after a restart.
func TestCheckpointStoreSurvivesAFailedWrite(t *testing.T) {
	client := inMemoryClient()
	require.NoError(t, client.Close(t.Context()))
	store := newCheckpointStore(client, zaptest.NewLogger(t))

	store.save(t.Context(), alertCheckpointKey, time.Now())
}

func TestGetStorageClient(t *testing.T) {
	nonStorage := storagetest.NewNonStorageID("test")
	fileBacked := storagetest.NewFileBackedStorageExtension("test", t.TempDir())
	missing := storagetest.NewStorageID("absent")
	host := storagetest.NewStorageHost().
		WithExtension(fileBacked.ID, fileBacked).
		WithNonStorageExtension("test")

	id := component.NewID(metadata.Type)

	t.Run("unset means in-memory", func(t *testing.T) {
		client, err := getStorageClient(t.Context(), host, nil, id)
		require.NoError(t, err)
		require.NotNil(t, client)
	})

	t.Run("configured extension", func(t *testing.T) {
		client, err := getStorageClient(t.Context(), host, &fileBacked.ID, id)
		require.NoError(t, err)
		require.NoError(t, client.Close(t.Context()))
	})

	t.Run("unknown extension", func(t *testing.T) {
		_, err := getStorageClient(t.Context(), host, &missing, id)
		require.Error(t, err)
	})

	t.Run("non-storage extension", func(t *testing.T) {
		_, err := getStorageClient(t.Context(), host, &nonStorage, id)
		require.Error(t, err)
	})
}

// The point of persistence: a restarted receiver resumes from where the
// previous process stopped, not from now-initial_lookback.
func TestCheckpointsSurviveRestart(t *testing.T) {
	storageDir := t.TempDir()
	// Inside the lookback window, so the poller accepts it as an advance.
	lastUpdated := strfmt.DateTime(time.Now().Add(-time.Hour).UTC().Truncate(time.Second))

	newReceiver := func(t *testing.T, api falconAPI, storageID *component.ID) *crowdstrikeReceiver {
		t.Helper()
		cfg := createDefaultConfig().(*Config)
		cfg.PollInterval = 10 * time.Millisecond
		cfg.InitialLookback = 24 * time.Hour
		cfg.StorageID = storageID
		return &crowdstrikeReceiver{
			id:           component.NewID(metadata.Type),
			logger:       zaptest.NewLogger(t),
			nextConsumer: consumertest.NewNop(),
			config:       cfg,
			api:          api,
		}
	}

	ext := storagetest.NewFileBackedStorageExtension("test", storageDir)
	host := storagetest.NewStorageHost().WithExtension(ext.ID, ext)

	first := &fakeAPI{alertPages: [][]*models.DetectsAlert{{{UpdatedTimestamp: &lastUpdated}}}}
	r := newReceiver(t, first, &ext.ID)
	require.NoError(t, r.Start(t.Context(), host))
	require.Eventually(t, func() bool { return len(first.since()) > 0 }, time.Second, 5*time.Millisecond)
	require.NoError(t, r.Shutdown(t.Context()))
	require.NoError(t, ext.Shutdown(t.Context()))

	// A fresh process: new extension over the same directory, new receiver.
	ext = storagetest.NewFileBackedStorageExtension("test", storageDir)
	host = storagetest.NewStorageHost().WithExtension(ext.ID, ext)

	second := &fakeAPI{}
	r = newReceiver(t, second, &ext.ID)
	require.NoError(t, r.Start(t.Context(), host))
	require.Eventually(t, func() bool { return len(second.since()) > 0 }, time.Second, 5*time.Millisecond)
	require.NoError(t, r.Shutdown(t.Context()))
	require.NoError(t, ext.Shutdown(t.Context()))

	assert.Equal(t, time.Time(lastUpdated), second.since()[0].UTC(),
		"the restarted receiver must resume from the stored checkpoint")
}

// Without a storage extension the same restart replays the lookback window.
func TestCheckpointsAreLostWithoutStorage(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	cfg.PollInterval = 10 * time.Millisecond
	cfg.InitialLookback = time.Hour

	api := &fakeAPI{}
	r := &crowdstrikeReceiver{
		id:           component.NewID(metadata.Type),
		logger:       zaptest.NewLogger(t),
		nextConsumer: consumertest.NewNop(),
		config:       cfg,
		api:          api,
	}
	require.NoError(t, r.Start(t.Context(), componenttest.NewNopHost()))
	require.Eventually(t, func() bool { return len(api.since()) > 0 }, time.Second, 5*time.Millisecond)
	require.NoError(t, r.Shutdown(t.Context()))

	assert.WithinDuration(t, time.Now().Add(-time.Hour), api.since()[0], time.Minute)
}
