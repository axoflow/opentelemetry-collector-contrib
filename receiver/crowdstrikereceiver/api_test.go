// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package crowdstrikereceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/crowdstrikereceiver"

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/crowdstrike/gofalcon/falcon/client"
	"github.com/crowdstrike/gofalcon/falcon/client/alerts"
	"github.com/crowdstrike/gofalcon/falcon/client/ngsiem"
	"github.com/crowdstrike/gofalcon/falcon/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

// fakeAlertsClient stands in for the swagger client's Alerts service, which is
// an interface on the generated API struct. Embedding it leaves the methods
// the receiver never calls unimplemented.
type fakeAlertsClient struct {
	alerts.ClientService

	queried *alerts.QueryV2Params
	fetched *alerts.GetV2Params

	queryPayload *models.DetectsapiAlertQueryResponse
	getPayload   *models.DetectsapiPostEntitiesAlertsV2Response
}

func (f *fakeAlertsClient) QueryV2(params *alerts.QueryV2Params, _ ...alerts.ClientOption) (*alerts.QueryV2OK, error) {
	f.queried = params
	return &alerts.QueryV2OK{Payload: f.queryPayload}, nil
}

func (f *fakeAlertsClient) GetV2(params *alerts.GetV2Params, _ ...alerts.ClientOption) (*alerts.GetV2OK, error) {
	f.fetched = params
	return &alerts.GetV2OK{Payload: f.getPayload}, nil
}

func newTestGofalconAPI(t *testing.T, service *fakeAlertsClient) *gofalconAPI {
	t.Helper()
	return &gofalconAPI{
		client: &client.CrowdStrikeAPISpecification{Alerts: service},
		logger: zaptest.NewLogger(t),
	}
}

// fakeNgsiemClient does the same for the Ngsiem service. It answers every
// status poll from statuses and errors once they run out, so a loop that keeps
// polling a job which already reported a terminal state fails the test instead
// of sitting out searchMaxWait.
type fakeNgsiemClient struct {
	ngsiem.ClientService

	jobID    string
	statuses []*models.APIQueryJobsResults

	started *ngsiem.StartSearchV1Params
	stopped *ngsiem.StopSearchV1Params
	polls   int
}

func (f *fakeNgsiemClient) StartSearchV1(params *ngsiem.StartSearchV1Params, _ ...ngsiem.ClientOption) (*ngsiem.StartSearchV1OK, error) {
	f.started = params
	return &ngsiem.StartSearchV1OK{Payload: &models.APIQueryJobResponse{ID: &f.jobID}}, nil
}

func (f *fakeNgsiemClient) GetSearchStatusV1(_ *ngsiem.GetSearchStatusV1Params, _ ...ngsiem.ClientOption) (*ngsiem.GetSearchStatusV1OK, error) {
	f.polls++
	if f.polls > len(f.statuses) {
		return nil, fmt.Errorf("query job polled %d times for %d statuses", f.polls, len(f.statuses))
	}
	return &ngsiem.GetSearchStatusV1OK{Payload: f.statuses[f.polls-1]}, nil
}

func (f *fakeNgsiemClient) StopSearchV1(params *ngsiem.StopSearchV1Params, _ ...ngsiem.ClientOption) (*ngsiem.StopSearchV1OK, error) {
	f.stopped = params
	return &ngsiem.StopSearchV1OK{}, nil
}

func newTestSearchAPI(t *testing.T, service *fakeNgsiemClient, query string) *gofalconAPI {
	t.Helper()
	return &gofalconAPI{
		client:     &client.CrowdStrikeAPISpecification{Ngsiem: service},
		logger:     zaptest.NewLogger(t),
		repository: "third-party",
		query:      query,
	}
}

func searchStatus(done, cancelled bool, events ...models.APIQueryJobsResultsEvents) *models.APIQueryJobsResults {
	return &models.APIQueryJobsResults{Done: &done, Cancelled: &cancelled, Events: events}
}

func alertQueryResponse(ids ...string) *models.DetectsapiAlertQueryResponse {
	return &models.DetectsapiAlertQueryResponse{Resources: ids}
}

func alertGetResponse(count int) *models.DetectsapiPostEntitiesAlertsV2Response {
	return &models.DetectsapiPostEntitiesAlertsV2Response{Resources: make([]*models.DetectsAlert, count)}
}

func TestFetchAlertsRequest(t *testing.T) {
	service := &fakeAlertsClient{
		queryPayload: alertQueryResponse("first", "second"),
		getPayload:   alertGetResponse(2),
	}
	api := newTestGofalconAPI(t, service)

	// A checkpoint outside UTC still has to render as a UTC FQL literal.
	since := time.Date(2026, 8, 2, 20, 14, 5, 123_000_000, time.FixedZone("CEST", 2*60*60))
	got, err := api.fetchAlerts(t.Context(), since, 500)
	require.NoError(t, err)
	assert.Len(t, got, 2)

	require.NotNil(t, service.queried.Filter)
	assert.Equal(t, "updated_timestamp:>='2026-08-02T18:14:05.123Z'", *service.queried.Filter)
	require.NotNil(t, service.queried.Sort)
	assert.Equal(t, "updated_timestamp|asc", *service.queried.Sort)
	require.NotNil(t, service.queried.Limit)
	assert.Equal(t, int64(500), *service.queried.Limit)

	require.NotNil(t, service.fetched.Body)
	assert.Equal(t, []string{"first", "second"}, service.fetched.Body.CompositeIds)
}

// Nothing matched the filter, so there is nothing to ask the entities endpoint
// for either.
func TestFetchAlertsSkipsAnEmptyQuery(t *testing.T) {
	service := &fakeAlertsClient{queryPayload: alertQueryResponse()}
	api := newTestGofalconAPI(t, service)

	got, err := api.fetchAlerts(t.Context(), time.Now(), 500)
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.Nil(t, service.fetched)
}

func apiError(id, message string) *models.MsaAPIError {
	return &models.MsaAPIError{ID: id, Message: &message}
}

// Both endpoints answer 200 with the failures listed in the envelope. Reading
// only the resources turns those into an empty or short page, which the poller
// cannot tell from being caught up.
func TestFetchAlertsSurfacesPayloadErrors(t *testing.T) {
	cases := []struct {
		name     string
		service  *fakeAlertsClient
		expected string
	}{
		{
			name: "the query failed",
			service: &fakeAlertsClient{
				queryPayload: &models.DetectsapiAlertQueryResponse{
					Errors: []*models.MsaAPIError{apiError("filter", "invalid FQL")},
				},
			},
			expected: "filter: invalid FQL",
		},
		{
			name: "every alert failed",
			service: &fakeAlertsClient{
				queryPayload: alertQueryResponse("first", "second"),
				getPayload: &models.DetectsapiPostEntitiesAlertsV2Response{
					Errors: []*models.MsaAPIError{apiError("first", "not found"), apiError("second", "not found")},
				},
			},
			expected: "first: not found; second: not found",
		},
		{
			// Message is required by the spec but the receiver is not the
			// place to find out that it was not sent.
			name: "an error carries no message",
			service: &fakeAlertsClient{
				queryPayload: alertQueryResponse("first"),
				getPayload: &models.DetectsapiPostEntitiesAlertsV2Response{
					Errors: []*models.MsaAPIError{nil, {ID: "first"}},
				},
			},
			expected: "first: unspecified error",
		},
		{
			// Nothing says which alerts are missing, so the whole page has to
			// be re-read rather than stepped over.
			name: "alerts went missing without an explanation",
			service: &fakeAlertsClient{
				queryPayload: alertQueryResponse("first", "second", "third"),
				getPayload:   alertGetResponse(2),
			},
			expected: "asked for 3 alerts, got 2",
		},
		{
			name:     "the query carried no payload",
			service:  &fakeAlertsClient{},
			expected: "response carried no payload",
		},
		{
			name:     "the fetch carried no payload",
			service:  &fakeAlertsClient{queryPayload: alertQueryResponse("first")},
			expected: "response carried no payload",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := newTestGofalconAPI(t, tc.service).fetchAlerts(t.Context(), time.Now(), 500)
			require.ErrorContains(t, err, tc.expected)
			assert.Nil(t, got)
		})
	}
}

func TestRunSearchRequest(t *testing.T) {
	event := models.APIQueryJobsResultsEvents(map[string]any{"@id": "one"})
	service := &fakeNgsiemClient{
		jobID:    "job-1",
		statuses: []*models.APIQueryJobsResults{searchStatus(true, false, event)},
	}
	api := newTestSearchAPI(t, service, "#event.module=panos")

	events, err := api.runSearch(t.Context(), time.UnixMilli(1785694385519), time.UnixMilli(1785694445519))
	require.NoError(t, err)
	assert.Equal(t, []models.APIQueryJobsResultsEvents{event}, events)

	require.NotNil(t, service.started.Body)
	require.NotNil(t, service.started.Body.QueryString)
	// The checkpoint arithmetic resumes from @ingesttimestamp, so the query
	// has to sort on the same field; the explicit limit is what lifts the
	// server's 200-event cap on a query job.
	assert.Equal(t, "#event.module=panos | sort(@ingesttimestamp, order=asc, limit=10000)", *service.started.Body.QueryString)
	assert.Equal(t, "1785694385519", service.started.Body.IngestStart)
	assert.Equal(t, "1785694445519", service.started.Body.IngestEnd)
	assert.Equal(t, "third-party", service.started.Repository)

	// A job that is not stopped holds server-side resources until it expires.
	require.NotNil(t, service.stopped)
	assert.Equal(t, "job-1", service.stopped.ID)
}

// A cancelled job is terminal and its events are partial. Returning them as if
// the job had completed advances the checkpoint past the events it never
// reached; waiting for done that will never come parks the poller for
// searchMaxWait and then blames the timeout.
func TestRunSearchFailsOnACancelledJob(t *testing.T) {
	cases := []struct {
		name   string
		status *models.APIQueryJobsResults
	}{
		{name: "cancelled", status: searchStatus(false, true)},
		{
			name:   "cancelled and done",
			status: searchStatus(true, true, models.APIQueryJobsResultsEvents(map[string]any{"@id": "partial"})),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service := &fakeNgsiemClient{jobID: "job-1", statuses: []*models.APIQueryJobsResults{tc.status}}

			events, err := newTestSearchAPI(t, service, "*").runSearch(t.Context(), time.UnixMilli(1785694385519), time.UnixMilli(1785694445519))
			require.ErrorContains(t, err, "cancelled")
			assert.Nil(t, events)
			assert.Equal(t, 1, service.polls)
			require.NotNil(t, service.stopped)
		})
	}
}

// The poll context is cancelled on shutdown, and Shutdown waits for the
// in-flight poll: a parked poller must not hold it up for the whole back-off.
func TestBackOffOnRateLimitReturnsOnCancel(t *testing.T) {
	api := newTestGofalconAPI(t, &fakeAlertsClient{})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	returned := make(chan struct{})
	go func() {
		defer close(returned)
		api.backOffOnRateLimit(ctx, 100, 0)
	}()

	select {
	case <-returned:
	case <-time.After(10 * time.Second):
		t.Fatal("backOffOnRateLimit kept the poller parked after its context was cancelled")
	}
}
