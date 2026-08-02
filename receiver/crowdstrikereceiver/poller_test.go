// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package crowdstrikereceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/crowdstrikereceiver"

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/crowdstrike/gofalcon/falcon/models"
	"github.com/go-openapi/strfmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/extension/xextension/storage"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/receiver/receiverhelper"
	"go.opentelemetry.io/collector/receiver/receivertest"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest"
	"go.uber.org/zap/zaptest/observer"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/crowdstrikereceiver/internal/metadata"
)

// fakeAPI replays canned responses and records what the pollers asked for, so
// the checkpoint arithmetic can be asserted without the CrowdStrike API.
//
// fetchAlerts serves the whole alerts corpus the way the Alerts API does — the
// same updated_timestamp filter the production FQL asks for, ascending, capped
// at the requested limit — because a fake that ignores `since` cannot tell a
// paginating poller from one that re-reads the same page forever.
type fakeAPI struct {
	mu          sync.Mutex
	alerts      []*models.DetectsAlert
	alertErr    error
	alertSince  []time.Time
	alertLimits []int

	searchBatches [][]models.APIQueryJobsResultsEvents
	searchStarts  []time.Time
	searchEnds    []time.Time
}

// maxFakeAlertCalls bounds a single poll: a poller that cannot advance its
// filter would otherwise page forever and hang the test instead of failing it.
const maxFakeAlertCalls = 64

func (f *fakeAPI) fetchAlerts(_ context.Context, since time.Time, limit int) ([]*models.DetectsAlert, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.alertSince = append(f.alertSince, since)
	f.alertLimits = append(f.alertLimits, limit)
	if len(f.alertSince) > maxFakeAlertCalls {
		return nil, fmt.Errorf("runaway pagination: %d fetchAlerts calls", len(f.alertSince))
	}
	if f.alertErr != nil {
		return nil, f.alertErr
	}

	var matched []*models.DetectsAlert
	for _, alert := range f.alerts {
		// resources[] can carry a JSON null, which comes back whatever the
		// filter was: there is nothing in it to match on.
		if alert == nil {
			matched = append(matched, nil)
			continue
		}
		// An alert without the field the filter is on cannot match it either.
		if alert.UpdatedTimestamp == nil || time.Time(*alert.UpdatedTimestamp).Before(since) {
			continue
		}
		matched = append(matched, alert)
	}
	slices.SortStableFunc(matched, func(a, b *models.DetectsAlert) int {
		switch {
		case a == nil:
			return -1
		case b == nil:
			return 1
		}
		return time.Time(*a.UpdatedTimestamp).Compare(time.Time(*b.UpdatedTimestamp))
	})
	if len(matched) > limit {
		matched = matched[:limit]
	}
	return matched, nil
}

func (f *fakeAPI) runSearch(_ context.Context, start, end time.Time) ([]models.APIQueryJobsResultsEvents, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.searchStarts = append(f.searchStarts, start)
	f.searchEnds = append(f.searchEnds, end)
	if len(f.searchBatches) == 0 {
		return nil, nil
	}
	batch := f.searchBatches[0]
	f.searchBatches = f.searchBatches[1:]
	return batch, nil
}

// since and starts report the window bounds the two pollers asked for. The
// tests that drive a poller through Start read them while its goroutine is
// still running, so those reads go through the mutex; a test calling
// pollAlertsOnce or pollSearchOnce itself is the fake's only goroutine and
// reads the recorded slices directly.
func (f *fakeAPI) since() []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Time(nil), f.alertSince...)
}

func (f *fakeAPI) starts() []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Time(nil), f.searchStarts...)
}

func newTestReceiver(t *testing.T, api falconAPI, next consumer.Logs) *crowdstrikeReceiver {
	t.Helper()
	return &crowdstrikeReceiver{
		logger:       zaptest.NewLogger(t),
		nextConsumer: next,
		config:       createDefaultConfig().(*Config),
		api:          api,
		checkpoints:  newCheckpointStore(storage.NewNopClient(), zaptest.NewLogger(t)),
		obsrecv:      newTestObsReport(t),
	}
}

// newLifecycleReceiver builds a receiver for the tests that go through Start
// and Shutdown, i.e. one that resolves its own checkpoint store from the host
// rather than being handed one.
func newLifecycleReceiver(t *testing.T, api falconAPI, next consumer.Logs, configure func(*Config)) *crowdstrikeReceiver {
	t.Helper()
	cfg := createDefaultConfig().(*Config)
	configure(cfg)
	return &crowdstrikeReceiver{
		id:           component.NewID(metadata.Type),
		logger:       zaptest.NewLogger(t),
		nextConsumer: next,
		config:       cfg,
		api:          api,
		obsrecv:      newTestObsReport(t),
	}
}

func newTestObsReport(t *testing.T) *receiverhelper.ObsReport {
	t.Helper()
	obsrecv, err := receiverhelper.NewObsReport(receiverhelper.ObsReportSettings{
		ReceiverID:             component.NewID(metadata.Type),
		Transport:              "http",
		ReceiverCreateSettings: receivertest.NewNopSettings(metadata.Type),
	})
	require.NoError(t, err)
	return obsrecv
}

func alertUpdatedAt(t *testing.T, id, value string) *models.DetectsAlert {
	t.Helper()
	return &models.DetectsAlert{CompositeID: &id, UpdatedTimestamp: dateTime(t, value)}
}

func alertsUpdatedAt(t *testing.T, count int, idPrefix, value string) []*models.DetectsAlert {
	t.Helper()
	alerts := make([]*models.DetectsAlert, count)
	for i := range alerts {
		alerts[i] = alertUpdatedAt(t, fmt.Sprintf("%s-%d", idPrefix, i), value)
	}
	return alerts
}

func deliveredIDs(t *testing.T, sink *consumertest.LogsSink) []string {
	t.Helper()
	var ids []string
	for _, logs := range sink.AllLogs() {
		records := logs.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords()
		for i := 0; i < records.Len(); i++ {
			id, ok := records.At(i).Body().Map().Get("composite_id")
			require.True(t, ok)
			ids = append(ids, id.Str())
		}
	}
	return ids
}

func TestPollAlertsAdvancesCheckpoint(t *testing.T) {
	api := &fakeAPI{alerts: []*models.DetectsAlert{
		alertUpdatedAt(t, "a", "2026-08-02T18:14:05Z"),
		alertUpdatedAt(t, "b", "2026-08-02T18:14:07Z"),
		alertUpdatedAt(t, "c", "2026-08-02T18:14:06Z"),
	}}
	sink := new(consumertest.LogsSink)
	r := newTestReceiver(t, api, sink)
	start := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	r.alertCheckpoint = start

	require.NoError(t, r.pollAlertsOnce(t.Context()))
	assert.Equal(t, "2026-08-02T18:14:07Z", r.alertCheckpoint.UTC().Format(time.RFC3339))
	assert.ElementsMatch(t, []string{"a", "b", "c"}, deliveredIDs(t, sink))

	// A short page ends the poll, so exactly one query went out, from the
	// checkpoint the receiver started with.
	assert.Equal(t, []time.Time{start}, api.alertSince)
}

// The checkpoint must not move when the pipeline rejects the batch, otherwise
// the alerts are lost instead of retried on the next tick.
func TestPollAlertsKeepsCheckpointOnConsumeError(t *testing.T) {
	api := &fakeAPI{alerts: []*models.DetectsAlert{alertUpdatedAt(t, "a", "2026-08-02T18:14:05Z")}}
	r := newTestReceiver(t, api, consumertest.NewErr(errors.New("pipeline is down")))
	start := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	r.alertCheckpoint = start

	require.Error(t, r.pollAlertsOnce(t.Context()))
	assert.Equal(t, start, r.alertCheckpoint)
}

// A JSON null in resources[] says nothing about an alert, and reading one
// takes the collector down with the poll goroutine.
func TestPollAlertsSkipsNullResources(t *testing.T) {
	api := &fakeAPI{alerts: []*models.DetectsAlert{
		alertUpdatedAt(t, "a", "2026-08-02T18:14:05Z"),
		nil,
		alertUpdatedAt(t, "b", "2026-08-02T18:14:07Z"),
	}}
	sink := new(consumertest.LogsSink)
	r := newTestReceiver(t, api, sink)
	r.alertCheckpoint = time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)

	require.NoError(t, r.pollAlertsOnce(t.Context()))

	assert.Equal(t, []string{"a", "b"}, deliveredIDs(t, sink))
	assert.Equal(t, "2026-08-02T18:14:07Z", r.alertCheckpoint.UTC().Format(time.RFC3339))
}

// A page the API could not deliver is an error, not an empty page: the
// checkpoint has to stay put so the next tick asks for it again.
func TestPollAlertsKeepsCheckpointOnFetchError(t *testing.T) {
	api := &fakeAPI{alertErr: errors.New("every alert failed")}
	r := newTestReceiver(t, api, consumertest.NewNop())
	start := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	r.alertCheckpoint = start

	require.Error(t, r.pollAlertsOnce(t.Context()))
	require.Error(t, r.pollAlertsOnce(t.Context()))

	assert.Equal(t, start, r.alertCheckpoint)
	assert.Equal(t, []time.Time{start, start}, api.alertSince)
}

func TestPollAlertsPaginates(t *testing.T) {
	api := &fakeAPI{alerts: append(
		alertsUpdatedAt(t, alertPageSize, "page", "2026-08-02T18:14:05Z"),
		alertUpdatedAt(t, "next", "2026-08-02T18:14:09Z"),
	)}
	sink := new(consumertest.LogsSink)
	r := newTestReceiver(t, api, sink)
	start := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	r.alertCheckpoint = start

	require.NoError(t, r.pollAlertsOnce(t.Context()))
	assert.Equal(t, "2026-08-02T18:14:09Z", r.alertCheckpoint.UTC().Format(time.RFC3339))
	assert.Len(t, deliveredIDs(t, sink), alertPageSize+1)
	// The second page was requested from where the first one ended, not from
	// an offset.
	require.Len(t, api.alertSince, 2)
	assert.Equal(t, start, api.alertSince[0])
	assert.Equal(t, "2026-08-02T18:14:05Z", api.alertSince[1].UTC().Format(time.RFC3339))
}

// More alerts than a page holds can share one updated_timestamp, and the
// filter is millisecond-granular, so the page boundary can fall inside a tie.
// Nothing in that millisecond may be skipped over.
func TestPollAlertsDeliversAFullPageSharingOneMillisecond(t *testing.T) {
	api := &fakeAPI{alerts: alertsUpdatedAt(t, alertPageSize+1, "tie", "2026-08-02T18:14:05Z")}
	sink := new(consumertest.LogsSink)
	r := newTestReceiver(t, api, sink)
	r.alertCheckpoint = time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)

	require.NoError(t, r.pollAlertsOnce(t.Context()))

	delivered := deliveredIDs(t, sink)
	assert.Len(t, delivered, alertPageSize+1)
	assert.Len(t, slices.Compact(slices.Sorted(slices.Values(delivered))), alertPageSize+1)
	// The second page had to make room for the alerts already consumed at the
	// checkpoint millisecond, or it would have carried the same page again.
	assert.Equal(t, []int{alertPageSize, alertPageSize + alertPageSize}, api.alertLimits)
}

// The same tie, but only a handful of alerts share the millisecond the page
// boundary happens to land on.
func TestPollAlertsDeliversATieAcrossThePageBoundary(t *testing.T) {
	base := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	corpus := make([]*models.DetectsAlert, 0, alertPageSize+2)
	for i := range alertPageSize - 2 {
		updated := strfmt.DateTime(base.Add(time.Duration(i+1) * time.Millisecond))
		id := fmt.Sprintf("uniq-%d", i)
		corpus = append(corpus, &models.DetectsAlert{CompositeID: &id, UpdatedTimestamp: &updated})
	}
	// Three alerts sharing a millisecond, so the page ends mid-tie.
	corpus = append(corpus, alertsUpdatedAt(t, 3, "tie", "2026-08-02T12:00:00.600Z")...)
	corpus = append(corpus, alertUpdatedAt(t, "after", "2026-08-02T12:00:00.700Z"))

	api := &fakeAPI{alerts: corpus}
	sink := new(consumertest.LogsSink)
	r := newTestReceiver(t, api, sink)
	r.alertCheckpoint = base

	require.NoError(t, r.pollAlertsOnce(t.Context()))

	delivered := deliveredIDs(t, sink)
	assert.Len(t, delivered, len(corpus))
	assert.Subset(t, delivered, []string{"tie-0", "tie-1", "tie-2", "after"})
}

// The filter is inclusive at the checkpoint, so the alerts consumed in its
// millisecond come back on the next tick; they must not be delivered twice.
// Alerts without a composite_id are always kept: dropping them would be a
// guess.
func TestPollAlertsDropsBoundaryDuplicates(t *testing.T) {
	unidentified := &models.DetectsAlert{UpdatedTimestamp: dateTime(t, "2026-08-02T18:14:05Z")}
	api := &fakeAPI{alerts: []*models.DetectsAlert{
		alertUpdatedAt(t, "a", "2026-08-02T18:14:05Z"),
		alertUpdatedAt(t, "b", "2026-08-02T18:14:05Z"),
		unidentified,
	}}
	sink := new(consumertest.LogsSink)
	r := newTestReceiver(t, api, sink)
	r.alertCheckpoint = time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)

	require.NoError(t, r.pollAlertsOnce(t.Context()))
	require.NoError(t, r.pollAlertsOnce(t.Context()))

	assert.Equal(t, []string{"a", "b", "", ""}, deliveredIDs(t, sink))
}

// More alerts than the query limit returns can share one millisecond. That
// page can never be drained, so the poller has to step past it and say so
// rather than re-read it forever.
func TestPollAlertsStepsPastAStalledMillisecond(t *testing.T) {
	stuck := time.Date(2026, 8, 2, 18, 14, 5, 0, time.UTC)
	api := &fakeAPI{alerts: alertsUpdatedAt(t, alertQueryMaxLimit+1, "tie", "2026-08-02T18:14:05Z")}
	sink := new(consumertest.LogsSink)
	r := newTestReceiver(t, api, sink)
	core, logs := observer.New(zapcore.WarnLevel)
	r.logger = zap.New(core)
	r.alertCheckpoint = time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)

	require.NoError(t, r.pollAlertsOnce(t.Context()))

	assert.Len(t, deliveredIDs(t, sink), alertQueryMaxLimit)
	assert.Equal(t, stuck.Add(time.Millisecond), r.alertCheckpoint)
	require.Equal(t, 1, logs.Len())
	assert.Contains(t, logs.All()[0].Message, "stepping past it")
}

// jsonNumber renders a number the way the swagger runtime's UseNumber decoder
// hands it to the poller.
func jsonNumber(value int64) json.Number {
	return json.Number(strconv.FormatInt(value, 10))
}

func searchBatch(size int, ingestMillis int64) []models.APIQueryJobsResultsEvents {
	batch := make([]models.APIQueryJobsResultsEvents, size)
	for i := range batch {
		batch[i] = map[string]any{"@ingesttimestamp": jsonNumber(ingestMillis), "@rawstring": "event"}
	}
	return batch
}

// The window ends a safety lag behind the collector's clock, and the next one
// resumes from exactly there: an event stamped inside a window but searchable
// only after its query job ran would otherwise be lost, since a closed window
// is never queried again.
func TestPollSearchAdvancesToWindowEnd(t *testing.T) {
	api := &fakeAPI{searchBatches: [][]models.APIQueryJobsResultsEvents{searchBatch(3, 1754157200000)}}
	r := newTestReceiver(t, api, consumertest.NewNop())
	start := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	r.searchCheckpoint = start

	before := time.Now()
	require.NoError(t, r.pollSearchOnce(t.Context()))

	assert.Equal(t, []time.Time{start}, api.searchStarts)
	require.Len(t, api.searchEnds, 1)
	windowEnd := api.searchEnds[0]
	assert.False(t, windowEnd.Before(before.Add(-searchIngestLag)))
	assert.False(t, windowEnd.After(time.Now().Add(-searchIngestLag)))
	assert.Equal(t, windowEnd, r.searchCheckpoint)
}

// The window opens searchIngestLag after the checkpoint, so a receiver started
// without an initial_lookback has nothing to ask for until the lag elapses. The
// tick after it opens still has to go out.
func TestPollSearchSkipsAWindowThatHasNotOpened(t *testing.T) {
	api := &fakeAPI{searchBatches: [][]models.APIQueryJobsResultsEvents{searchBatch(1, 1754157200000)}}
	r := newTestReceiver(t, api, consumertest.NewNop())
	started := time.Now()
	r.searchCheckpoint = started

	require.NoError(t, r.pollSearchOnce(t.Context()))
	assert.Empty(t, api.searchStarts)
	assert.Equal(t, started, r.searchCheckpoint)

	// Same as the lag having elapsed since the receiver started.
	opened := started.Add(-2 * searchIngestLag)
	r.searchCheckpoint = opened
	require.NoError(t, r.pollSearchOnce(t.Context()))
	assert.Equal(t, []time.Time{opened}, api.searchStarts)
	assert.True(t, r.searchCheckpoint.After(opened))
}

// A batch at the limit means the window was truncated; the next window has to
// resume from the newest event seen, not skip to the window end.
func TestPollSearchResumesFromTruncation(t *testing.T) {
	const newest = int64(1754157245000)
	api := &fakeAPI{searchBatches: [][]models.APIQueryJobsResultsEvents{searchBatch(searchBatchLimit, newest)}}
	r := newTestReceiver(t, api, consumertest.NewNop())
	r.searchCheckpoint = time.UnixMilli(newest - 60000)

	require.NoError(t, r.pollSearchOnce(t.Context()))
	assert.Equal(t, time.UnixMilli(newest), r.searchCheckpoint)
}

// More events than the batch limit sharing one ingest millisecond cannot be
// drained; the poller must step past it rather than requery it forever.
func TestPollSearchStepsPastAStalledMillisecond(t *testing.T) {
	const stuck = int64(1754157246000)
	api := &fakeAPI{searchBatches: [][]models.APIQueryJobsResultsEvents{searchBatch(searchBatchLimit, stuck)}}
	r := newTestReceiver(t, api, consumertest.NewNop())
	r.searchCheckpoint = time.UnixMilli(stuck)

	require.NoError(t, r.pollSearchOnce(t.Context()))
	assert.Equal(t, time.UnixMilli(stuck).Add(time.Millisecond), r.searchCheckpoint)
}

// A batch at the limit says the window was truncated, but without an
// @ingesttimestamp there is nothing to resume from: the poller skips to the
// window end, and has to say what that skipped.
func TestPollSearchWarnsOnAFullBatchWithoutIngestTimestamps(t *testing.T) {
	batch := make([]models.APIQueryJobsResultsEvents, searchBatchLimit)
	for i := range batch {
		batch[i] = map[string]any{"@id": fmt.Sprintf("event-%d", i), "@rawstring": "event"}
	}
	api := &fakeAPI{searchBatches: [][]models.APIQueryJobsResultsEvents{batch}}
	r := newTestReceiver(t, api, consumertest.NewNop())
	core, logs := observer.New(zapcore.WarnLevel)
	r.logger = zap.New(core)
	r.searchCheckpoint = time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)

	require.NoError(t, r.pollSearchOnce(t.Context()))

	require.Len(t, api.searchEnds, 1)
	assert.Equal(t, api.searchEnds[0], r.searchCheckpoint)
	assert.Empty(t, r.searchBoundaryIDs)
	require.Equal(t, 1, logs.Len())
	assert.Contains(t, logs.All()[0].Message, "@ingesttimestamp")
}

func TestPollSearchKeepsCheckpointOnConsumeError(t *testing.T) {
	api := &fakeAPI{searchBatches: [][]models.APIQueryJobsResultsEvents{searchBatch(1, 1754157245000)}}
	r := newTestReceiver(t, api, consumertest.NewErr(errors.New("pipeline is down")))
	start := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	r.searchCheckpoint = start

	require.Error(t, r.pollSearchOnce(t.Context()))
	assert.Equal(t, start, r.searchCheckpoint)
}

// The ingest window is inclusive at its lower bound, so the events at the
// checkpoint millisecond come back on the next poll; @id identifies them.
func TestPollSearchDropsBoundaryDuplicates(t *testing.T) {
	const boundary = int64(1754157245000)
	atBoundary := func(id string) models.APIQueryJobsResultsEvents {
		return map[string]any{"@id": id, "@ingesttimestamp": jsonNumber(boundary), "@rawstring": "event " + id}
	}

	// A first batch at the limit leaves the checkpoint on the boundary
	// millisecond, then the second window returns those events again plus one
	// new one.
	first := searchBatch(searchBatchLimit-1, boundary)
	first = append(first, atBoundary("a"))
	second := []models.APIQueryJobsResultsEvents{
		atBoundary("a"),
		map[string]any{}, // an event without @id is never dropped
		map[string]any{"@id": "b", "@ingesttimestamp": jsonNumber(boundary + 1), "@rawstring": "event b"},
	}

	api := &fakeAPI{searchBatches: [][]models.APIQueryJobsResultsEvents{first, second}}
	sink := new(consumertest.LogsSink)
	r := newTestReceiver(t, api, sink)
	r.searchCheckpoint = time.UnixMilli(boundary - 1000)

	require.NoError(t, r.pollSearchOnce(t.Context()))
	require.Equal(t, time.UnixMilli(boundary), r.searchCheckpoint)
	require.Len(t, sink.AllLogs(), 1)

	require.NoError(t, r.pollSearchOnce(t.Context()))
	require.Len(t, sink.AllLogs(), 2)

	bodies := []string{}
	lrs := sink.AllLogs()[1].ResourceLogs().At(0).ScopeLogs().At(0).LogRecords()
	for i := 0; i < lrs.Len(); i++ {
		bodies = append(bodies, lrs.At(i).Body().Str())
	}
	assert.ElementsMatch(t, []string{"{}", "event b"}, bodies)
}

// The first poll goes out on Start rather than an interval later: a collector
// restarting more often than the cadence it was given would never reach a tick
// and so never collect anything.
func TestStartPollsImmediately(t *testing.T) {
	api := &fakeAPI{}
	r := newLifecycleReceiver(t, api, consumertest.NewNop(), func(cfg *Config) {
		cfg.PollInterval = time.Hour
		cfg.InitialLookback = time.Hour
	})

	require.NoError(t, r.Start(t.Context(), componenttest.NewNopHost()))
	defer func() { require.NoError(t, r.Shutdown(t.Context())) }()

	require.Eventually(t, func() bool { return len(api.since()) == 1 }, time.Second, 5*time.Millisecond)
	assert.Empty(t, api.starts(), "no repository is configured, so nothing should search")
}

// The NG-SIEM poller runs whenever a repository is configured, and inherits
// the top-level cadence when it is given none of its own. Without that
// fallback the zero value reaches time.NewTicker, which panics the poll
// goroutine and takes the collector down with it.
func TestSearchPollInheritsTheTopLevelInterval(t *testing.T) {
	api := &fakeAPI{}
	r := newLifecycleReceiver(t, api, consumertest.NewNop(), func(cfg *Config) {
		cfg.PollInterval = 10 * time.Millisecond
		cfg.InitialLookback = time.Hour
		cfg.NGSIEMSearch.Repository = "third-party"
	})

	require.NoError(t, r.Start(t.Context(), componenttest.NewNopHost()))
	defer func() { require.NoError(t, r.Shutdown(t.Context())) }()

	require.Eventually(t, func() bool { return len(api.starts()) > 1 }, time.Second, 5*time.Millisecond)
}

// Its own poll_interval overrides the top-level one, which is the point of
// having it: a query job costs far more than an alert page.
func TestSearchPollIntervalOverridesTheTopLevelOne(t *testing.T) {
	api := &fakeAPI{}
	r := newLifecycleReceiver(t, api, consumertest.NewNop(), func(cfg *Config) {
		cfg.PollInterval = time.Hour
		cfg.InitialLookback = time.Hour
		cfg.NGSIEMSearch.Repository = "third-party"
		cfg.NGSIEMSearch.PollInterval = 10 * time.Millisecond
	})

	require.NoError(t, r.Start(t.Context(), componenttest.NewNopHost()))
	defer func() { require.NoError(t, r.Shutdown(t.Context())) }()

	require.Eventually(t, func() bool { return len(api.starts()) > 1 }, time.Second, 5*time.Millisecond)
	assert.Len(t, api.since(), 1, "the alert poller stays on the top-level cadence")
}

// disable_alerts leaves the NG-SIEM poller running on its own — collecting the
// ingested events without the detections derived from them is the reason the
// option exists.
func TestStartWithAlertsDisabled(t *testing.T) {
	api := &fakeAPI{}
	r := newLifecycleReceiver(t, api, consumertest.NewNop(), func(cfg *Config) {
		cfg.PollInterval = 10 * time.Millisecond
		cfg.InitialLookback = time.Hour
		cfg.DisableAlerts = true
		cfg.NGSIEMSearch.Repository = "third-party"
	})

	require.NoError(t, r.Start(t.Context(), componenttest.NewNopHost()))
	defer func() { require.NoError(t, r.Shutdown(t.Context())) }()

	require.Eventually(t, func() bool { return len(api.starts()) > 1 }, time.Second, 5*time.Millisecond)
	assert.Empty(t, api.since(), "the alert poller must not run at all")
}

// blockingConsumer parks inside ConsumeLogs until it is released, so a
// Shutdown racing an in-flight delivery can be observed.
type blockingConsumer struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (*blockingConsumer) Capabilities() consumer.Capabilities { return consumer.Capabilities{} }

func (c *blockingConsumer) ConsumeLogs(context.Context, plog.Logs) error {
	c.once.Do(func() { close(c.entered) })
	<-c.release
	return nil
}

// Shutdown has to outlast the in-flight poll's ConsumeLogs. Returning earlier
// hands records to a pipeline already being torn down, and closes the
// checkpoint store under a poll that is still going to write to it.
func TestShutdownWaitsForAnInFlightDelivery(t *testing.T) {
	recent := strfmt.DateTime(time.Now().Add(-time.Minute))
	api := &fakeAPI{alerts: []*models.DetectsAlert{{UpdatedTimestamp: &recent}}}
	next := &blockingConsumer{entered: make(chan struct{}), release: make(chan struct{})}
	r := newLifecycleReceiver(t, api, next, func(cfg *Config) {
		cfg.PollInterval = time.Hour
		cfg.InitialLookback = time.Hour
	})

	require.NoError(t, r.Start(t.Context(), componenttest.NewNopHost()))
	<-next.entered

	stopped := make(chan error, 1)
	go func() { stopped <- r.Shutdown(t.Context()) }()
	select {
	case <-stopped:
		t.Fatal("Shutdown returned while a poll was still inside ConsumeLogs")
	case <-time.After(50 * time.Millisecond):
	}

	close(next.release)
	require.NoError(t, <-stopped)
}
