// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package crowdstrikefdrreceiver

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/plog"
)

func TestDecodeFile(t *testing.T) {
	buf, err := os.ReadFile("testdata/events.jsonl")
	require.NoError(t, err)

	logs, err := decodeFile(buf, "test")
	require.NoError(t, err)

	scope := logs.ResourceLogs().At(0).ScopeLogs().At(0)
	assert.Equal(t, "test", scope.Scope().Version())
	records := scope.LogRecords()
	require.Equal(t, 6, records.Len())

	want := []struct {
		field string
		ts    string
	}{
		{"event_simpleName", "2020-07-09T19:41:20.123Z"},
		{"EventType", "2020-07-09T19:41:20Z"},
		{"ComputerName", "2020-07-09T19:41:20.1234567Z"},
		{"LocalAddressIP4", "2020-07-09T19:41:20.123Z"},
		{"event_type", "2020-07-09T19:41:20.123456789Z"},
		{"aid", ""},
	}
	for i, w := range want {
		record := records.At(i)
		_, ok := record.Body().Map().Get(w.field)
		assert.True(t, ok, "record %d body missing %q", i, w.field)
		assert.NotZero(t, record.ObservedTimestamp())
		if w.ts == "" {
			assert.Zero(t, record.Timestamp(), "record %d", i)
			continue
		}
		wantTS, err := time.Parse(time.RFC3339Nano, w.ts)
		require.NoError(t, err)
		assert.Equal(t, wantTS.UTC(), record.Timestamp().AsTime(), "record %d", i)
	}
}

func TestDecodeFileInvalidLine(t *testing.T) {
	_, err := decodeFile([]byte("{\"ok\":true}\nnot json\n"), "")
	require.ErrorContains(t, err, "line 2")
}

func TestDecodeFileEmpty(t *testing.T) {
	logs, err := decodeFile(nil, "")
	require.NoError(t, err)
	assert.Equal(t, 0, logs.LogRecordCount())
	assert.Equal(t, plog.NewLogs().ResourceLogs().Len()+1, logs.ResourceLogs().Len())
}
