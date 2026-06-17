// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearchlogsreceiver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
)

func TestSearchRequestAndParsing(t *testing.T) {
	var gotPath, gotAuth, gotContentType string
	var gotBody searchRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &gotBody))

		_, _ = w.Write([]byte(`{
			"hits": {
				"hits": [
					{"_index": "logs-000001", "_id": "abc", "_source": {"message": "hello"}, "sort": ["2026-06-17T10:15:23.123Z", "abc"]},
					{"_index": "logs-000001", "_id": "def", "_source": {"message": "world"}, "sort": ["2026-06-17T10:15:24.000Z", "def"]}
				]
			}
		}`))
	}))
	defer srv.Close()

	cfg := validConfig()
	cfg.Endpoint = srv.URL
	cfg.Username = "user"
	cfg.Password = "pass"
	cfg.PageSize = 1000

	client, err := newESLogsClient(context.Background(), componenttest.NewNopTelemetrySettings(), cfg, componenttest.NewNopHost())
	require.NoError(t, err)

	resp, err := client.Search(context.Background(), searchRequest{
		Size:        cfg.PageSize,
		Sort:        cfg.Sort,
		SearchAfter: []any{"2026-06-17T10:15:23.123Z", "abc123"},
	})
	require.NoError(t, err)

	// request assertions
	assert.Equal(t, "/logs-*/_search", gotPath)
	assert.Equal(t, "Basic dXNlcjpwYXNz", gotAuth)
	assert.Equal(t, "application/json", gotContentType)
	assert.Equal(t, 1000, gotBody.Size)
	assert.Equal(t, cfg.Sort, gotBody.Sort)
	assert.Equal(t, []any{"2026-06-17T10:15:23.123Z", "abc123"}, gotBody.SearchAfter)

	// response assertions
	require.Len(t, resp.Hits.Hits, 2)
	assert.Equal(t, "abc", resp.Hits.Hits[0].ID)
	assert.Equal(t, "logs-000001", resp.Hits.Hits[0].Index)
	assert.Equal(t, "hello", resp.Hits.Hits[0].Source["message"])
	assert.Equal(t, []any{"2026-06-17T10:15:24.000Z", "def"}, resp.Hits.Hits[1].Sort)
}

func TestSearchAPIKeyAuth(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"hits": {"hits": []}}`))
	}))
	defer srv.Close()

	cfg := validConfig()
	cfg.Endpoint = srv.URL
	cfg.APIKey = "mykey=="

	client, err := newESLogsClient(context.Background(), componenttest.NewNopTelemetrySettings(), cfg, componenttest.NewNopHost())
	require.NoError(t, err)

	_, err = client.Search(context.Background(), searchRequest{Size: 10, Sort: cfg.Sort})
	require.NoError(t, err)
	assert.Equal(t, "ApiKey mykey==", gotAuth)
}

func TestSearchErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	cfg := validConfig()
	cfg.Endpoint = srv.URL

	client, err := newESLogsClient(context.Background(), componenttest.NewNopTelemetrySettings(), cfg, componenttest.NewNopHost())
	require.NoError(t, err)

	_, err = client.Search(context.Background(), searchRequest{Size: 10, Sort: cfg.Sort})
	assert.ErrorIs(t, err, errUnauthenticated)
}
