// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package crowdstrikefdrreceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/crowdstrikefdrreceiver"

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/crowdstrikefdrreceiver/internal/metadata"
)

// timestampFields lists, in priority order, the field each FDR feed uses for the event time:
// sensor events ("timestamp", epoch ms), external events ("UTCTimestamp", epoch ms),
// aidmaster ("Time", epoch s), other inventory feeds ("_time", epoch s) and
// zero trust host assessment ("modified_time", RFC 3339).
var timestampFields = []string{"timestamp", "UTCTimestamp", "Time", "_time", "modified_time"}

// decodeFile turns one decompressed FDR file (newline-delimited JSON, one event per line) into logs.
func decodeFile(buf []byte, version string) (plog.Logs, error) {
	logs := plog.NewLogs()
	scopeLogs := logs.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty()
	scopeLogs.Scope().SetName(metadata.ScopeName)
	scopeLogs.Scope().SetVersion(version)

	now := pcommon.NewTimestampFromTime(time.Now())
	lineNo := 0
	for line := range bytes.SplitSeq(buf, []byte{'\n'}) {
		lineNo++
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil {
			return plog.Logs{}, fmt.Errorf("line %d: %w", lineNo, err)
		}
		record := scopeLogs.LogRecords().AppendEmpty()
		record.SetObservedTimestamp(now)
		if ts, ok := eventTimestamp(event); ok {
			record.SetTimestamp(ts)
		}
		if err := record.Body().SetEmptyMap().FromRaw(event); err != nil {
			return plog.Logs{}, fmt.Errorf("line %d: %w", lineNo, err)
		}
	}
	return logs, nil
}

func eventTimestamp(event map[string]any) (pcommon.Timestamp, bool) {
	for _, field := range timestampFields {
		switch v := event[field].(type) {
		case float64:
			return epochTimestamp(strconv.FormatFloat(v, 'f', -1, 64))
		case string:
			if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
				return pcommon.NewTimestampFromTime(t), true
			}
			return epochTimestamp(v)
		}
	}
	return 0, false
}

// epochTimestamp parses "<seconds>[.<fraction>]" or "<milliseconds>" without going through float64,
// so fractional inventory timestamps keep their digits.
func epochTimestamp(s string) (pcommon.Timestamp, bool) {
	whole, frac, _ := strings.Cut(s, ".")
	n, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, false
	}
	// ponytail: FDR mixes epoch seconds and milliseconds; 12+ digits of seconds would be year 5138+.
	if len(whole) >= 12 {
		return pcommon.Timestamp(n * int64(time.Millisecond)), true
	}
	ns, err := strconv.ParseInt((frac + "000000000")[:9], 10, 64)
	if err != nil {
		return 0, false
	}
	return pcommon.Timestamp(n*int64(time.Second) + ns), true
}
