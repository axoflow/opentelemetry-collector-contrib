// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package idirareceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/idirareceiver"

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	createQueryPath = "/api/audits/stream/createQuery"
	resultsPath     = "/api/audits/stream/results"

	// dateLayout is the format the filterModel date filter expects.
	dateLayout = "2006-01-02 15:04:05"
)

// selectedFields is the field set documented for the createQuery API.
var selectedFields = []string{
	"tenant_id", "custom_data", "arrival_timestamp", "checksum", "application_code",
	"audit_code", "timestamp", "user_id", "session_id", "source", "action_type",
	"audit_type", "component", "target", "command", "message", "username", "action",
	"uuid", "icon", "service_name", "identity_type",
}

type client struct {
	httpClient *http.Client
	endpoint   string
	apiKey     string
}

type createQueryRequest struct {
	Query query `json:"query"`
}

type query struct {
	PageSize       int         `json:"pageSize"`
	SelectedFields []string    `json:"selectedFields"`
	FilterModel    filterModel `json:"filterModel"`
	SortModel      []sortModel `json:"sortModel"`
}

type filterModel struct {
	Date            dateFilter    `json:"date"`
	ApplicationCode []filterEntry `json:"applicationCode,omitempty"`
}

type dateFilter struct {
	DateFrom string `json:"dateFrom"`
	DateTo   string `json:"dateTo"`
}

type filterEntry struct {
	Op     string   `json:"op"`
	Params []string `json:"params"`
}

type sortModel struct {
	FieldName string `json:"field_name"`
	Direction string `json:"direction"`
}

type createQueryResponse struct {
	CursorRef string `json:"cursorRef"`
}

type resultsRequest struct {
	CursorRef string `json:"cursorRef"`
}

type resultsResponse struct {
	Data   []map[string]any `json:"data"`
	Paging struct {
		Cursor struct {
			CursorRef string `json:"cursorRef"`
		} `json:"cursor"`
	} `json:"paging"`
}

// createQuery opens a query over [from, to] and returns the cursor its results are read with.
func (c *client) createQuery(ctx context.Context, from, to time.Time, pageSize int, applicationCodes []string) (string, error) {
	req := createQueryRequest{Query: query{
		PageSize:       pageSize,
		SelectedFields: selectedFields,
		FilterModel: filterModel{Date: dateFilter{
			DateFrom: from.UTC().Format(dateLayout),
			DateTo:   to.UTC().Format(dateLayout),
		}},
		SortModel: []sortModel{{FieldName: "timestamp", Direction: "asc"}},
	}}
	if len(applicationCodes) > 0 {
		req.Query.FilterModel.ApplicationCode = []filterEntry{{Op: "include", Params: applicationCodes}}
	}

	var resp createQueryResponse
	if err := c.post(ctx, createQueryPath, req, &resp); err != nil {
		return "", err
	}
	if resp.CursorRef == "" {
		return "", fmt.Errorf("%s returned an empty cursorRef", createQueryPath)
	}
	return resp.CursorRef, nil
}

// results fetches a single page of audit events for the given cursor.
func (c *client) results(ctx context.Context, cursorRef string) (*resultsResponse, error) {
	var resp resultsResponse
	if err := c.post(ctx, resultsPath, resultsRequest{CursorRef: cursorRef}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *client) post(ctx context.Context, path string, payload, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(c.endpoint, "/")+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		// The API's error bodies are undocumented, so report a truncated raw body.
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s returned %d: %s", path, resp.StatusCode, bytes.TrimSpace(snippet))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
