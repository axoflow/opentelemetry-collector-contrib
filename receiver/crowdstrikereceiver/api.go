// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package crowdstrikereceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/crowdstrikereceiver"

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/crowdstrike/gofalcon/falcon/client"
	"github.com/crowdstrike/gofalcon/falcon/client/alerts"
	"github.com/crowdstrike/gofalcon/falcon/client/ngsiem"
	"github.com/crowdstrike/gofalcon/falcon/models"
	"go.uber.org/zap"
)

// falconAPI is the seam over gofalcon: one method per poller's remote work, so
// the pollers' checkpoint arithmetic can be exercised without the swagger
// client. gofalconAPI is the only production implementation.
type falconAPI interface {
	// fetchAlerts returns at most limit alerts updated at or after since,
	// oldest first.
	fetchAlerts(ctx context.Context, since time.Time, limit int) ([]*models.DetectsAlert, error)
	// runSearch runs one NG-SIEM query job over the ingest-time window and
	// returns the events it matched.
	runSearch(ctx context.Context, start, end time.Time) ([]models.APIQueryJobsResultsEvents, error)
}

// rateLimitBackOff is how long to park once the API rate budget is nearly
// spent, rather than spending the remainder on retries. How fast the budget
// refills is the API's business, so it is not derived from poll_interval.
const rateLimitBackOff = time.Minute

type gofalconAPI struct {
	client     *client.CrowdStrikeAPISpecification
	logger     *zap.Logger
	repository string
	query      string
}

func (a *gofalconAPI) fetchAlerts(ctx context.Context, since time.Time, limit int) ([]*models.DetectsAlert, error) {
	filter := fmt.Sprintf("updated_timestamp:>='%s'", since.UTC().Format(fqlTimestampLayout))
	sort := "updated_timestamp|asc"
	pageSize := int64(limit)
	queried, err := a.client.Alerts.QueryV2(alerts.NewQueryV2Params().
		WithContext(ctx).
		WithFilter(&filter).
		WithSort(&sort).
		WithLimit(&pageSize))
	if err != nil {
		return nil, fmt.Errorf("querying alert IDs: %w", err)
	}
	// The swagger client leaves Payload nil for a body it cannot decode, so
	// every payload access has to be guarded: a nil dereference in a poll
	// goroutine takes the whole collector down.
	queriedPayload := queried.GetPayload()
	if queriedPayload == nil {
		return nil, errors.New("querying alert IDs: response carried no payload")
	}
	if apiErr := payloadError(queriedPayload.Errors); apiErr != nil {
		return nil, fmt.Errorf("querying alert IDs: %w", apiErr)
	}
	if len(queriedPayload.Resources) == 0 {
		return nil, nil
	}

	fetched, err := a.client.Alerts.GetV2(alerts.NewGetV2Params().
		WithContext(ctx).
		WithBody(&models.DetectsapiPostEntitiesAlertsV2Request{CompositeIds: queriedPayload.Resources}))
	if err != nil {
		return nil, fmt.Errorf("fetching alerts: %w", err)
	}

	a.backOffOnRateLimit(ctx, fetched.XRateLimitLimit, fetched.XRateLimitRemaining)

	fetchedPayload := fetched.GetPayload()
	if fetchedPayload == nil {
		return nil, fmt.Errorf("fetching alerts (trace_id %s): response carried no payload", fetched.XCSTRACEID)
	}
	if apiErr := payloadError(fetchedPayload.Errors); apiErr != nil {
		return nil, fmt.Errorf("fetching alerts (trace_id %s): %w", fetched.XCSTRACEID, apiErr)
	}
	// Alerts that went missing with nothing said about them cannot be told
	// apart from ones that were never there, and the response says nothing
	// about which ids are affected. Failing the page keeps the checkpoint
	// where it is, so the next tick asks for the same alerts again instead of
	// advancing over the missing ones.
	if len(fetchedPayload.Resources) < len(queriedPayload.Resources) {
		return nil, fmt.Errorf("fetching alerts (trace_id %s): asked for %d alerts, got %d and no errors",
			fetched.XCSTRACEID, len(queriedPayload.Resources), len(fetchedPayload.Resources))
	}
	a.logger.Debug("fetched alerts",
		zap.Int("alerts", len(fetchedPayload.Resources)),
		zap.String("trace_id", fetched.XCSTRACEID))
	return fetchedPayload.Resources, nil
}

// payloadError converts the errors a 200 response carries in its envelope into
// a Go error. falcon.AssertNoError does the same, but dereferences
// MsaAPIError.Message, which is only required by the spec, not by the wire.
func payloadError(apiErrors []*models.MsaAPIError) error {
	messages := make([]string, 0, len(apiErrors))
	for _, apiError := range apiErrors {
		if apiError == nil {
			continue
		}
		message := "unspecified error"
		if apiError.Message != nil {
			message = *apiError.Message
		}
		if apiError.ID != "" {
			message = apiError.ID + ": " + message
		}
		messages = append(messages, message)
	}
	if len(messages) == 0 {
		return nil
	}
	return errors.New(strings.Join(messages, "; "))
}

func (a *gofalconAPI) runSearch(ctx context.Context, start, end time.Time) ([]models.APIQueryJobsResultsEvents, error) {
	query := a.query
	if query == "" {
		// createDefaultConfig fills this in, so only a query_string explicitly
		// set to "" arrives empty — and an empty one builds a query the server
		// rejects rather than a match-all.
		query = defaultSearchQuery
	}
	// The sort is what the poller resumes a truncated batch from, so it has to
	// order on the same field the checkpoint arithmetic reads.
	query = fmt.Sprintf("%s | sort(%s, order=asc, limit=%d)", query, ingestTimestampField, searchBatchLimit)
	started, err := a.client.Ngsiem.StartSearchV1(ngsiem.NewStartSearchV1Params().
		WithContext(ctx).
		WithRepository(a.repository).
		WithBody(&models.APIQueryJobInput{
			QueryString: &query,
			IngestStart: strconv.FormatInt(start.UnixMilli(), 10),
			IngestEnd:   strconv.FormatInt(end.UnixMilli(), 10),
		}))
	if err != nil {
		return nil, fmt.Errorf("starting query job: %w", err)
	}
	startedPayload := started.GetPayload()
	if startedPayload == nil || startedPayload.ID == nil {
		return nil, errors.New("starting query job: response carried no job ID")
	}
	jobID := *startedPayload.ID
	// A query job lives on server-side until it is stopped or expires; a
	// receiver that only ever starts them leaks one per tick.
	defer a.stopSearch(ctx, jobID)

	deadline := time.Now().Add(searchMaxWait)
	for {
		status, err := a.client.Ngsiem.GetSearchStatusV1(ngsiem.NewGetSearchStatusV1Params().
			WithContext(ctx).
			WithRepository(a.repository).
			WithID(jobID))
		if err != nil {
			return nil, fmt.Errorf("polling query job %s: %w", jobID, err)
		}
		results := status.GetPayload()
		if results == nil {
			return nil, fmt.Errorf("polling query job %s: response carried no payload", jobID)
		}
		// A cancelled job is over, and the events it reached are whatever it
		// had matched when it was cancelled. The server can report it done as
		// well, so this comes first: honoring done would deliver a partial
		// window and move the checkpoint past everything the job never got to.
		if results.Cancelled != nil && *results.Cancelled {
			return nil, fmt.Errorf("query job %s was cancelled server-side", jobID)
		}
		if results.Done != nil && *results.Done {
			return results.Events, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("query job %s did not complete within %s", jobID, searchMaxWait)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(searchStatusPollDelay(results)):
		}
	}
}

// stopSearch releases the server-side query job. It runs on a context detached
// from the poll one so shutdown still cleans up after itself.
func (a *gofalconAPI) stopSearch(ctx context.Context, jobID string) {
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), searchStopTimeout)
	defer cancel()

	if _, err := a.client.Ngsiem.StopSearchV1(ngsiem.NewStopSearchV1Params().
		WithContext(stopCtx).
		WithRepository(a.repository).
		WithID(jobID)); err != nil {
		a.logger.Warn("stopping NG-SIEM query job failed, it will expire server-side",
			zap.String("job_id", jobID), zap.Error(err))
	}
}

func (a *gofalconAPI) backOffOnRateLimit(ctx context.Context, limit, remaining int64) {
	if remaining >= limit/10 {
		return
	}
	a.logger.Warn("CrowdStrike API rate limit nearly exhausted, backing off",
		zap.Int64("remaining", remaining),
		zap.Int64("limit", limit),
	)
	select {
	case <-ctx.Done():
	case <-time.After(rateLimitBackOff):
	}
}

func searchStatusPollDelay(results *models.APIQueryJobsResults) time.Duration {
	if results.MetaData == nil || results.MetaData.PollAfter == nil {
		return searchStatusPollInterval
	}
	return min(max(time.Duration(*results.MetaData.PollAfter)*time.Millisecond, searchStatusPollFloor), searchStatusPollCeiling)
}
