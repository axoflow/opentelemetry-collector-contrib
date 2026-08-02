// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package crowdstrikereceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/crowdstrikereceiver"

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/crowdstrike/gofalcon/falcon/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.uber.org/zap/zaptest"
)

// fakeAPI replays canned responses and records what the pollers asked for, so
// the checkpoint arithmetic can be asserted without the CrowdStrike API.
type fakeAPI struct {
	alertPages [][]*models.DetectsAlert
	alertSince []time.Time

	searchBatches [][]models.APIQueryJobsResultsEvents
	searchStarts  []time.Time
	searchEnds    []time.Time
}

func (f *fakeAPI) fetchAlerts(_ context.Context, since time.Time, _ int) ([]*models.DetectsAlert, error) {
	f.alertSince = append(f.alertSince, since)
	if len(f.alertPages) == 0 {
		return nil, nil
	}
	page := f.alertPages[0]
	f.alertPages = f.alertPages[1:]
	return page, nil
}

func (f *fakeAPI) runSearch(_ context.Context, start, end time.Time) ([]models.APIQueryJobsResultsEvents, error) {
	f.searchStarts = append(f.searchStarts, start)
	f.searchEnds = append(f.searchEnds, end)
	if len(f.searchBatches) == 0 {
		return nil, nil
	}
	batch := f.searchBatches[0]
	f.searchBatches = f.searchBatches[1:]
	return batch, nil
}

func newTestReceiver(t *testing.T, api falconAPI, next consumer.Logs) *crowdstrikeReceiver {
	t.Helper()
	return &crowdstrikeReceiver{
		logger:       zaptest.NewLogger(t),
		nextConsumer: next,
		config:       createDefaultConfig().(*Config),
		api:          api,
	}
}

func alertUpdatedAt(t *testing.T, value string) *models.DetectsAlert {
	t.Helper()
	return &models.DetectsAlert{UpdatedTimestamp: dateTime(t, value)}
}

func fullAlertPage(t *testing.T, updated string) []*models.DetectsAlert {
	t.Helper()
	page := make([]*models.DetectsAlert, alertPageSize)
	for i := range page {
		page[i] = alertUpdatedAt(t, updated)
	}
	return page
}

func TestPollAlertsAdvancesCheckpoint(t *testing.T) {
	api := &fakeAPI{alertPages: [][]*models.DetectsAlert{{
		alertUpdatedAt(t, "2026-08-02T18:14:05Z"),
		alertUpdatedAt(t, "2026-08-02T18:14:07Z"),
		alertUpdatedAt(t, "2026-08-02T18:14:06Z"),
	}}}
	r := newTestReceiver(t, api, consumertest.NewNop())
	start := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	r.alertCheckpoint = start

	require.NoError(t, r.pollAlertsOnce(t.Context()))
	assert.Equal(t, "2026-08-02T18:14:07Z", r.alertCheckpoint.UTC().Format(time.RFC3339))

	// A short page ends the poll, so exactly one query went out, from the
	// checkpoint the receiver started with.
	assert.Equal(t, []time.Time{start}, api.alertSince)
}

// The checkpoint must not move when the pipeline rejects the batch, otherwise
// the alerts are lost instead of retried on the next tick.
func TestPollAlertsKeepsCheckpointOnConsumeError(t *testing.T) {
	api := &fakeAPI{alertPages: [][]*models.DetectsAlert{{alertUpdatedAt(t, "2026-08-02T18:14:05Z")}}}
	r := newTestReceiver(t, api, consumertest.NewErr(errors.New("pipeline is down")))
	start := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	r.alertCheckpoint = start

	require.Error(t, r.pollAlertsOnce(t.Context()))
	assert.Equal(t, start, r.alertCheckpoint)
}

func TestPollAlertsPaginates(t *testing.T) {
	api := &fakeAPI{alertPages: [][]*models.DetectsAlert{
		fullAlertPage(t, "2026-08-02T18:14:05Z"),
		{alertUpdatedAt(t, "2026-08-02T18:14:09Z")},
	}}
	r := newTestReceiver(t, api, consumertest.NewNop())
	start := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	r.alertCheckpoint = start

	require.NoError(t, r.pollAlertsOnce(t.Context()))
	assert.Equal(t, "2026-08-02T18:14:09Z", r.alertCheckpoint.UTC().Format(time.RFC3339))
	// The second page was requested from where the first one ended, not from
	// an offset.
	require.Len(t, api.alertSince, 2)
	assert.Equal(t, start, api.alertSince[0])
	assert.Equal(t, "2026-08-02T18:14:05Z", api.alertSince[1].UTC().Format(time.RFC3339))
}

// A full page whose alerts carry no usable updated_timestamp would otherwise be
// requeried forever.
func TestPollAlertsStepsPastAStalledPage(t *testing.T) {
	api := &fakeAPI{alertPages: [][]*models.DetectsAlert{
		make([]*models.DetectsAlert, alertPageSize),
		nil,
	}}
	for i := range api.alertPages[0] {
		api.alertPages[0][i] = &models.DetectsAlert{}
	}
	r := newTestReceiver(t, api, consumertest.NewNop())
	start := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	r.alertCheckpoint = start

	require.NoError(t, r.pollAlertsOnce(t.Context()))
	assert.Equal(t, start.Add(time.Millisecond), r.alertCheckpoint)
	require.Len(t, api.alertSince, 2)
	assert.Equal(t, start.Add(time.Millisecond), api.alertSince[1])
}

func searchBatch(size int, ingestMillis int64) []models.APIQueryJobsResultsEvents {
	batch := make([]models.APIQueryJobsResultsEvents, size)
	for i := range batch {
		batch[i] = map[string]any{"@ingesttimestamp": float64(ingestMillis), "@rawstring": "event"}
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

func TestPollSearchKeepsCheckpointOnConsumeError(t *testing.T) {
	api := &fakeAPI{searchBatches: [][]models.APIQueryJobsResultsEvents{searchBatch(1, 1754157245000)}}
	r := newTestReceiver(t, api, consumertest.NewErr(errors.New("pipeline is down")))
	start := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	r.searchCheckpoint = start

	require.Error(t, r.pollSearchOnce(t.Context()))
	assert.Equal(t, start, r.searchCheckpoint)
}
