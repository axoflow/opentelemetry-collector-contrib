// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package crowdstrikereceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/crowdstrikereceiver"

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/crowdstrike/gofalcon/falcon/models"
	"github.com/go-openapi/strfmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
)

func dateTime(t *testing.T, value string) *strfmt.DateTime {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	require.NoError(t, err)
	converted := strfmt.DateTime(parsed)
	return &converted
}

func onlyRecord(t *testing.T, logs *plog.Logs) plog.LogRecord {
	t.Helper()
	require.Equal(t, 1, logs.LogRecordCount())
	return logs.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0)
}

func TestConvertAlertToPlogLogs(t *testing.T) {
	name := "Hidden HTTP Tunnel"
	severityName := "Critical"

	logs, err := convertAlertToPlogLogs([]*models.DetectsAlert{{
		Name:         &name,
		SeverityName: &severityName,
		Timestamp:    dateTime(t, "2026-08-02T18:14:05Z"),
	}})
	require.NoError(t, err)

	lr := onlyRecord(t, logs)
	assert.Equal(t, "2026-08-02T18:14:05Z", lr.Timestamp().AsTime().Format(time.RFC3339))
	assert.NotZero(t, lr.ObservedTimestamp())
	assert.Equal(t, "Critical", lr.SeverityText())
	assert.Equal(t, plog.SeverityNumberFatal, lr.SeverityNumber())

	require.Equal(t, pcommon.ValueTypeMap, lr.Body().Type())
	got, ok := lr.Body().Map().Get("name")
	require.True(t, ok)
	assert.Equal(t, "Hidden HTTP Tunnel", got.Str())
	assert.Equal(t, 0, lr.Attributes().Len())
}

func TestConvertAlertSeverityNumbers(t *testing.T) {
	cases := []struct {
		severityName string
		expected     plog.SeverityNumber
	}{
		{"Informational", plog.SeverityNumberInfo},
		{"Low", plog.SeverityNumberWarn},
		{"Medium", plog.SeverityNumberWarn3},
		{"High", plog.SeverityNumberError},
		{"Critical", plog.SeverityNumberFatal},
		{"Something New", plog.SeverityNumberUnspecified},
	}

	for _, tc := range cases {
		t.Run(tc.severityName, func(t *testing.T) {
			logs, err := convertAlertToPlogLogs([]*models.DetectsAlert{{SeverityName: &tc.severityName}})
			require.NoError(t, err)

			lr := onlyRecord(t, logs)
			assert.Equal(t, tc.severityName, lr.SeverityText())
			assert.Equal(t, tc.expected, lr.SeverityNumber())
		})
	}
}

// An alert without a timestamp still has to carry one, so the record is not
// dropped or bucketed at the epoch downstream.
func TestConvertAlertToPlogLogsWithoutTimestamp(t *testing.T) {
	before := time.Now()

	logs, err := convertAlertToPlogLogs([]*models.DetectsAlert{{}})
	require.NoError(t, err)

	lr := onlyRecord(t, logs)
	assert.False(t, lr.Timestamp().AsTime().Before(before))
	assert.Empty(t, lr.SeverityText())
	assert.Equal(t, plog.SeverityNumberUnspecified, lr.SeverityNumber())
}

func TestConvertSearchEventsToPlogLogs(t *testing.T) {
	cases := []struct {
		name          string
		event         models.APIQueryJobsResultsEvents
		expectedBody  string
		expectedNanos int64
		expectedAttrs map[string]any
	}{
		{
			name: "the raw line is the body, the computed fields travel next to it",
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
			expectedAttrs: map[string]any{
				"@timestamp":      int64(1754157245000),
				"#Vendor":         "palo-alto-networks",
				"#event.dataset":  "palo-alto-networks.panos",
				"#repo":           "example_events",
				"observer.vendor": "Palo Alto Networks",
				"source.ip":       "192.168.41.30",
			},
		},
		{
			name: "no rawstring falls back to the whole event",
			event: map[string]any{
				"@timestamp":  json.Number("1754157245000"),
				"#event.kind": "event",
				"field":       "value",
			},
			expectedBody:  `{"#event.kind":"event","@timestamp":1754157245000,"field":"value"}`,
			expectedNanos: 1754157245000 * int64(time.Millisecond),
			expectedAttrs: map[string]any{
				"@timestamp":  int64(1754157245000),
				"#event.kind": "event",
				"field":       "value",
			},
		},
		{
			name:          "empty rawstring falls back to the whole event and is no attribute of its own",
			event:         map[string]any{"@rawstring": ""},
			expectedBody:  `{"@rawstring":""}`,
			expectedAttrs: map[string]any{},
		},
		{
			name:          "non-map event is emitted as JSON and has no fields to emit",
			event:         []any{"a", "b"},
			expectedBody:  `["a","b"]`,
			expectedAttrs: map[string]any{},
		},
		{
			name: "value types are preserved",
			event: map[string]any{
				"@ingesttimestamp": json.Number("1785694385519"),
				"score":            json.Number("1.5"),
				"bytes":            float64(1024),
				"suppressed":       false,
				"user.name":        nil,
			},
			expectedBody: `{"@ingesttimestamp":1785694385519,"bytes":1024,"score":1.5,"suppressed":false,"user.name":null}`,
			expectedAttrs: map[string]any{
				"@ingesttimestamp": int64(1785694385519),
				"score":            1.5,
				"bytes":            float64(1024),
				"suppressed":       false,
				"user.name":        nil,
			},
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
			assert.Equal(t, tc.expectedAttrs, lr.Attributes().AsRaw())
		})
	}
}

// decodeEvent decodes an event the way the swagger runtime decodes a query-job
// response: with UseNumber, which makes every number of the event a json.Number
// at whatever depth it sits.
func decodeEvent(t *testing.T, event string) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(event))
	decoder.UseNumber()
	var fields map[string]any
	require.NoError(t, decoder.Decode(&fields))
	return fields
}

// Nested objects and arrays stay nested — and keep their element types, which
// only holds if the json.Number the production decoder leaves inside them is
// converted there too, instead of the whole object being stringified.
func TestConvertSearchEventsAttributesNested(t *testing.T) {
	const event = `{
		"@rawstring": "line",
		"@timestamp": 1754157245000,
		"score": 1.5,
		"source": {"ip": "192.168.41.30", "port": 443},
		"event": {"category": ["network", "intrusion_detection"], "duration": 0.5, "counts": [1, 2]},
		"related.ip": ["10.0.0.1", "10.0.0.2"]
	}`

	logs, err := convertSearchEventsToPlogLogs([]models.APIQueryJobsResultsEvents{decodeEvent(t, event)})
	require.NoError(t, err)

	lr := onlyRecord(t, logs)
	assert.Equal(t, "line", lr.Body().Str())
	assert.Equal(t, 1754157245000*int64(time.Millisecond), int64(lr.Timestamp()))
	assert.Equal(t, map[string]any{
		"@timestamp": int64(1754157245000),
		"score":      1.5,
		"source":     map[string]any{"ip": "192.168.41.30", "port": int64(443)},
		"event": map[string]any{
			"category": []any{"network", "intrusion_detection"},
			"duration": 0.5,
			"counts":   []any{int64(1), int64(2)},
		},
		"related.ip": []any{"10.0.0.1", "10.0.0.2"},
	}, lr.Attributes().AsRaw())
}

func TestEpochMillis(t *testing.T) {
	cases := []struct {
		name     string
		value    any
		expected int64
		ok       bool
	}{
		{name: "json.Number", value: json.Number("1754157245000"), expected: 1754157245000, ok: true},
		{name: "not a whole number of milliseconds", value: json.Number("1754157245000.5")},
		// What a decoder not in UseNumber mode would have produced.
		{name: "float64", value: float64(1754157245000)},
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

func TestIngestBoundary(t *testing.T) {
	cases := []struct {
		name     string
		events   []models.APIQueryJobsResultsEvents
		expected int64
		ids      map[string]struct{}
		ok       bool
	}{
		{
			name: "the highest millisecond and the ids sharing it",
			events: []models.APIQueryJobsResultsEvents{
				map[string]any{"@ingesttimestamp": json.Number("10"), "@id": "older"},
				map[string]any{"@ingesttimestamp": json.Number("30"), "@id": "a"},
				map[string]any{"@ingesttimestamp": json.Number("20"), "@id": "b"},
				map[string]any{"@ingesttimestamp": json.Number("30"), "@id": "c"},
				map[string]any{"@ingesttimestamp": json.Number("30")},
			},
			expected: 30,
			ids:      map[string]struct{}{"a": {}, "c": {}},
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
			events: []models.APIQueryJobsResultsEvents{map[string]any{"other": "value", "@id": "a"}},
		},
		{name: "no events"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			millis, ids, ok := ingestBoundary(tc.events)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.expected, millis)
			assert.Equal(t, tc.ids, ids)
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
