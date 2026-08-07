// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package crowdstrikereceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/crowdstrikereceiver"

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/extension/xextension/storage"
	"go.uber.org/zap"
)

// The pollers checkpoint independently, and an NG-SIEM checkpoint only means
// anything for the repository it was taken against.
const alertCheckpointKey = "crowdstrike/alerts"

func searchCheckpointKey(repository string) string {
	return "crowdstrike/ngsiem_search/" + repository
}

// checkpointStore persists the poll checkpoints. A missing, unreadable or
// corrupt checkpoint is never fatal: the receiver falls back to the configured
// lookback and says so, because refusing to start would leave the tenant
// uncollected until an operator intervenes.
type checkpointStore struct {
	client storage.Client
	logger *zap.Logger
}

func newCheckpointStore(client storage.Client, logger *zap.Logger) *checkpointStore {
	return &checkpointStore{client: client, logger: logger}
}

func (s *checkpointStore) load(ctx context.Context, key string, fallback time.Time) time.Time {
	data, err := s.client.Get(ctx, key)
	if err != nil {
		s.logger.Warn("reading the stored checkpoint failed, falling back to the configured lookback",
			zap.String("key", key), zap.Error(err))
		return fallback
	}
	if len(data) == 0 {
		return fallback
	}

	var stored time.Time
	if err := json.Unmarshal(data, &stored); err != nil {
		s.logger.Warn("the stored checkpoint is corrupt, falling back to the configured lookback",
			zap.String("key", key), zap.Error(err))
		return fallback
	}
	s.logger.Info("resuming from the stored checkpoint", zap.String("key", key), zap.Time("checkpoint", stored))
	return stored
}

// save reports failures instead of returning them: the batch has already been
// delivered, so the only cost of a lost write is re-delivery after a restart.
func (s *checkpointStore) save(ctx context.Context, key string, at time.Time) {
	data, err := json.Marshal(at)
	if err == nil {
		err = s.client.Set(ctx, key, data)
	}
	if err != nil {
		s.logger.Warn("storing the checkpoint failed, a restart will resume from an older one",
			zap.String("key", key), zap.Time("checkpoint", at), zap.Error(err))
	}
}

// getStorageClient resolves the configured storage extension, or hands back an
// in-memory client so the rest of the receiver has one code path.
func getStorageClient(ctx context.Context, host component.Host, storageID *component.ID, componentID component.ID) (storage.Client, error) {
	if storageID == nil {
		return storage.NewNopClient(), nil
	}

	extension, ok := host.GetExtensions()[*storageID]
	if !ok {
		return nil, fmt.Errorf("storage extension '%s' not found", storageID)
	}

	storageExtension, ok := extension.(storage.Extension)
	if !ok {
		return nil, fmt.Errorf("non-storage extension '%s' found", storageID)
	}

	// Make storage immune to component renames that add underscores to the component type.
	// This is a workaround for https://github.com/open-telemetry/opentelemetry-collector/issues/14988.
	normalizedComponentType := strings.ReplaceAll(componentID.Type().String(), "_", "")
	normalizedComponentID := component.MustNewIDWithName(normalizedComponentType, componentID.Name())
	return storageExtension.GetClient(ctx, component.KindReceiver, normalizedComponentID, "")
}
