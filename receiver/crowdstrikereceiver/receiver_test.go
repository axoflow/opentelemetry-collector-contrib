// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package crowdstrikereceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/crowdstrikereceiver"

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/crowdstrike/gofalcon/falcon/client/alerts"
	"github.com/crowdstrike/gofalcon/falcon/models"
	"github.com/go-openapi/strfmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/plog"
)

func dateTime(t *testing.T, value string) *strfmt.DateTime {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	require.NoError(t, err)
	converted := strfmt.DateTime(parsed)
	return &converted
}

func alertsResponse(resources ...*models.DetectsAlert) *alerts.GetV2OK {
	return &alerts.GetV2OK{
		Payload: &models.DetectsapiPostEntitiesAlertsV2Response{Resources: resources},
	}
}

func onlyRecord(t *testing.T, logs *plog.Logs) plog.LogRecord {
	t.Helper()
	require.Equal(t, 1, logs.LogRecordCount())
	return logs.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0)
}

func TestConvertAlertToPlogLogs(t *testing.T) {
	name := "Hidden HTTP Tunnel"
	severityName := "Critical"

	logs, err := convertAlertToPlogLogs(alertsResponse(&models.DetectsAlert{
		Name:         &name,
		SeverityName: &severityName,
		Timestamp:    dateTime(t, "2026-08-02T18:14:05Z"),
	}))
	require.NoError(t, err)

	lr := onlyRecord(t, logs)
	assert.Equal(t, "2026-08-02T18:14:05Z", lr.Timestamp().AsTime().Format(time.RFC3339))
	assert.NotZero(t, lr.ObservedTimestamp())
	assert.Equal(t, "Critical", lr.SeverityText())

	got, ok := lr.Attributes().Get("name")
	require.True(t, ok)
	assert.Equal(t, "Hidden HTTP Tunnel", got.Str())
}

// An alert without a timestamp still has to carry one, so the record is not
// dropped or bucketed at the epoch downstream.
func TestConvertAlertToPlogLogsWithoutTimestamp(t *testing.T) {
	before := time.Now()

	logs, err := convertAlertToPlogLogs(alertsResponse(&models.DetectsAlert{}))
	require.NoError(t, err)

	lr := onlyRecord(t, logs)
	assert.False(t, lr.Timestamp().AsTime().Before(before))
	assert.Empty(t, lr.SeverityText())
}

func TestConvertSearchEventsToPlogLogs(t *testing.T) {
	cases := []struct {
		name          string
		event         models.APIQueryJobsResultsEvents
		expectedBody  string
		expectedNanos int64
	}{
		{
			name: "rawstring body",
			event: map[string]any{
				"@timestamp":      json.Number("1754157245000"),
				"@rawstring":      "<134>Aug 02 07:38:12 host 1,2026/08/02 07:38:12,TRAFFIC,end",
				"#Vendor":         "palo-alto-networks",
				"#event.dataset":  "palo-alto-networks.panos",
				"#repo":           "example_events",
				"observer.vendor": "Palo Alto Networks",
				"source.ip":       "192.168.41.30",
			},
			expectedBody:  "<134>Aug 02 07:38:12 host 1,2026/08/02 07:38:12,TRAFFIC,end",
			expectedNanos: 1754157245000 * int64(time.Millisecond),
		},
		{
			name: "no rawstring falls back to the whole event",
			event: map[string]any{
				"@timestamp": json.Number("1754157245000"),
				"field":      "value",
			},
			expectedBody:  `{"@timestamp":1754157245000,"field":"value"}`,
			expectedNanos: 1754157245000 * int64(time.Millisecond),
		},
		{
			name: "empty rawstring falls back to the whole event",
			event: map[string]any{
				"@rawstring": "",
			},
			expectedBody: `{"@rawstring":""}`,
		},
		{
			name:         "non-map event is emitted as JSON",
			event:        []any{"a", "b"},
			expectedBody: `["a","b"]`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs, err := convertSearchEventsToPlogLogs([]models.APIQueryJobsResultsEvents{tc.event})
			require.NoError(t, err)

			lr := onlyRecord(t, logs)
			assert.Equal(t, tc.expectedBody, lr.Body().Str())
			assert.Equal(t, tc.expectedNanos, int64(lr.Timestamp()))
			assert.NotZero(t, lr.ObservedTimestamp())
		})
	}
}

func TestEpochMillis(t *testing.T) {
	cases := []struct {
		name     string
		value    any
		expected int64
		ok       bool
	}{
		{name: "float64", value: float64(1754157245000), expected: 1754157245000, ok: true},
		{name: "json.Number", value: json.Number("1754157245000"), expected: 1754157245000, ok: true},
		{name: "string", value: "1754157245000", expected: 1754157245000, ok: true},
		{name: "unparseable string", value: "not-a-number"},
		{name: "wrong type", value: true},
		{name: "absent", value: nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			millis, ok := epochMillis(tc.value)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.expected, millis)
		})
	}
}

func TestMaxIngestTimestamp(t *testing.T) {
	cases := []struct {
		name     string
		events   []models.APIQueryJobsResultsEvents
		expected int64
		ok       bool
	}{
		{
			name: "highest of several",
			events: []models.APIQueryJobsResultsEvents{
				map[string]any{"@ingesttimestamp": json.Number("10"), "@id": "older"},
				map[string]any{"@ingesttimestamp": json.Number("30"), "@id": "a"},
				map[string]any{"@ingesttimestamp": json.Number("20"), "@id": "b"},
				map[string]any{"@ingesttimestamp": json.Number("30"), "@id": "c"},
				map[string]any{"@ingesttimestamp": json.Number("30")},
			},
			expected: 30,
			ok:       true,
		},
		{
			name: "events without the field are skipped",
			events: []models.APIQueryJobsResultsEvents{
				map[string]any{"other": "value"},
				"not-a-map",
				map[string]any{"@ingesttimestamp": json.Number("7")},
			},
			expected: 7,
			ok:       true,
		},
		{
			name:   "no timestamps at all",
			events: []models.APIQueryJobsResultsEvents{map[string]any{"other": "value"}},
		},
		{name: "no events"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			millis, ok := maxIngestTimestamp(tc.events)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.expected, millis)
		})
	}
}

func TestSearchStatusPollDelay(t *testing.T) {
	pollAfter := func(millis int64) *models.APIQueryJobsResults {
		return &models.APIQueryJobsResults{MetaData: &models.APIQueryMetadataJSON{PollAfter: &millis}}
	}

	cases := []struct {
		name     string
		results  *models.APIQueryJobsResults
		expected time.Duration
	}{
		{name: "no metadata", results: &models.APIQueryJobsResults{}, expected: searchStatusPollInterval},
		{name: "no hint", results: &models.APIQueryJobsResults{MetaData: &models.APIQueryMetadataJSON{}}, expected: searchStatusPollInterval},
		{name: "server hint", results: pollAfter(926), expected: 926 * time.Millisecond},
		{name: "hint below the floor", results: pollAfter(0), expected: searchStatusPollFloor},
		{name: "hint above the ceiling", results: pollAfter(600000), expected: searchStatusPollCeiling},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, searchStatusPollDelay(tc.results))
		})
	}
}
