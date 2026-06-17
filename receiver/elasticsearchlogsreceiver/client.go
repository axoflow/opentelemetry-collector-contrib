// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearchlogsreceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/elasticsearchlogsreceiver"

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"go.opentelemetry.io/collector/component"
	"go.uber.org/zap"
)

var (
	errUnauthenticated = errors.New("status 401, unauthenticated")
	errUnauthorized    = errors.New("status 403, unauthorized")
)

// searchRequest is the body sent to the Elasticsearch _search endpoint.
type searchRequest struct {
	Size        int                 `json:"size"`
	Query       map[string]any      `json:"query,omitempty"`
	Sort        []map[string]string `json:"sort"`
	SearchAfter []any               `json:"search_after,omitempty"`
}

// searchResponse models the subset of the _search response the receiver consumes.
type searchResponse struct {
	Hits struct {
		Hits []searchHit `json:"hits"`
	} `json:"hits"`
}

// searchHit is a single document returned by _search.
type searchHit struct {
	Index  string         `json:"_index"`
	ID     string         `json:"_id"`
	Source map[string]any `json:"_source"`
	// Sort holds the sort values for this hit; the values are used as the next search_after cursor.
	Sort []any `json:"sort"`
}

// esLogsClient queries Elasticsearch for log documents.
type esLogsClient interface {
	// Search issues a single _search request and returns the matching page of hits.
	Search(ctx context.Context, req searchRequest) (*searchResponse, error)
}

type defaultESLogsClient struct {
	client     *http.Client
	endpoint   *url.URL
	searchPath string
	authHeader string
	logger     *zap.Logger
}

var _ esLogsClient = (*defaultESLogsClient)(nil)

func newESLogsClient(ctx context.Context, settings component.TelemetrySettings, cfg *Config, host component.Host) (*defaultESLogsClient, error) {
	httpClient, err := cfg.ToClient(ctx, host.GetExtensions(), settings)
	if err != nil {
		return nil, err
	}

	endpoint, err := url.Parse(cfg.Endpoint)
	if err != nil {
		return nil, err
	}

	var authHeader string
	switch {
	case cfg.APIKey != "":
		authHeader = "ApiKey " + string(cfg.APIKey)
	case cfg.Username != "" && cfg.Password != "":
		userPass := fmt.Sprintf("%s:%s", cfg.Username, string(cfg.Password))
		authHeader = "Basic " + base64.StdEncoding.EncodeToString([]byte(userPass))
	}

	return &defaultESLogsClient{
		client:     httpClient,
		endpoint:   endpoint,
		searchPath: strings.Join(cfg.Indices, ",") + "/_search",
		authHeader: authHeader,
		logger:     settings.Logger,
	}, nil
}

func (c *defaultESLogsClient) Search(ctx context.Context, req searchRequest) (*searchResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal search request: %w", err)
	}

	respBody, err := c.doRequest(ctx, c.searchPath, body)
	if err != nil {
		return nil, err
	}

	var resp searchResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal search response: %w", err)
	}
	return &resp, nil
}

func (c *defaultESLogsClient) doRequest(ctx context.Context, path string, body []byte) ([]byte, error) {
	endpoint, err := c.endpoint.Parse(path)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	if c.authHeader != "" {
		req.Header.Add("Authorization", c.authHeader)
	}
	// Content-Type and Accept must agree on API compatibility: if one requests a compatible-with
	// version, the other must request the same. We use plain JSON on both, which the _search API
	// supports across Elasticsearch 7.x, 8.x and 9.x.
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return io.ReadAll(resp.Body)
	}

	respBody, readErr := io.ReadAll(resp.Body)
	c.logger.Debug(
		"Failed to make request to Elasticsearch",
		zap.String("path", path),
		zap.Int("status_code", resp.StatusCode),
		zap.ByteString("body", respBody),
		zap.NamedError("body_read_error", readErr),
	)

	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return nil, errUnauthenticated
	case http.StatusForbidden:
		return nil, errUnauthorized
	default:
		// Elasticsearch returns a JSON error body explaining the failure (e.g. an illegal sort
		// field). Include it in the error so the cause is visible without enabling debug logging.
		return nil, fmt.Errorf("got non 200 status code %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
}
