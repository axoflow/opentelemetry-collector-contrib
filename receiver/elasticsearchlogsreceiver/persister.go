// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearchlogsreceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/elasticsearchlogsreceiver"

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/extension/xextension/storage"
)

// cursorStorageKey is the key under which the search_after cursor is persisted.
const cursorStorageKey = "search_after_cursor"

// getStorageClient resolves the configured storage extension into a storage.Client. When no storage
// extension is configured a no-op client is returned, so the cursor is kept in memory only.
func getStorageClient(ctx context.Context, host component.Host, storageID *component.ID, componentID component.ID) (storage.Client, error) {
	if storageID == nil {
		return storage.NewNopClient(), nil
	}

	ext, ok := host.GetExtensions()[*storageID]
	if !ok {
		return nil, fmt.Errorf("storage extension '%s' not found", storageID)
	}

	storageExtension, ok := ext.(storage.Extension)
	if !ok {
		return nil, fmt.Errorf("non-storage extension '%s' found", storageID)
	}

	// Make storage immune to component renames that add underscores to the component type.
	normalizedComponentType := strings.ReplaceAll(componentID.Type().String(), "_", "")
	normalizedComponentID := component.MustNewIDWithName(normalizedComponentType, componentID.Name())
	return storageExtension.GetClient(ctx, component.KindReceiver, normalizedComponentID, "")
}

// cursorPersister stores and retrieves the search_after cursor using a storage client.
type cursorPersister struct {
	client storage.Client
}

func newCursorPersister(client storage.Client) *cursorPersister {
	return &cursorPersister{client: client}
}

// Load returns the persisted cursor, or nil if none has been stored yet.
func (p *cursorPersister) Load(ctx context.Context) ([]any, error) {
	data, err := p.client.Get(ctx, cursorStorageKey)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve cursor: %w", err)
	}
	if len(data) == 0 {
		return nil, nil
	}

	var cursor []any
	if err := json.Unmarshal(data, &cursor); err != nil {
		return nil, fmt.Errorf("failed to unmarshal cursor: %w", err)
	}
	return cursor, nil
}

// Save persists the cursor.
func (p *cursorPersister) Save(ctx context.Context, cursor []any) error {
	data, err := json.Marshal(cursor)
	if err != nil {
		return fmt.Errorf("failed to marshal cursor: %w", err)
	}
	if err := p.client.Set(ctx, cursorStorageKey, data); err != nil {
		return fmt.Errorf("failed to store cursor: %w", err)
	}
	return nil
}

func (p *cursorPersister) Close(ctx context.Context) error {
	return p.client.Close(ctx)
}
