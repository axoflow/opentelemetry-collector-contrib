// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package crowdstrikereceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/crowdstrikereceiver"

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/crowdstrike/gofalcon/falcon/client"
	"github.com/crowdstrike/gofalcon/falcon/client/alerts"
	"github.com/crowdstrike/gofalcon/falcon/client/ngsiem"
	"github.com/crowdstrike/gofalcon/falcon/models"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"
)

// FQL timestamp literal with millisecond precision, which is the granularity
// updated_timestamp carries. The filter on it is inclusive at the checkpoint:
// alerts can share a millisecond, and a strictly-greater filter would skip the
// ones a full page had no room for.
const fqlTimestampLayout = "2006-01-02T15:04:05.000Z"

// alertPageSize is how many not-yet-seen alerts a page asks for.
const alertPageSize = 500

// alertQueryMaxLimit is the largest limit QueryV2 accepts, and so the most
// alerts that can share one millisecond and still all be collected.
const alertQueryMaxLimit = 10000

// The query-job status response carries a metaData.pollAfter hint telling us
// when the server expects to have progressed; clamp it so a missing or absurd
// hint can neither busy-loop the poller nor park it for minutes. searchMaxWait
// bounds the whole job: a query that never reports done is abandoned (and
// stopped) rather than pinning the poller forever.
const (
	searchStatusPollInterval = time.Second
	searchStatusPollFloor    = 100 * time.Millisecond
	searchStatusPollCeiling  = 10 * time.Second
	searchMaxWait            = 10 * time.Minute
	searchStopTimeout        = 15 * time.Second
)

// searchBatchLimit is the result-set size requested via an explicit
// sort(limit=...); without it the server caps a query job at 200 events.
// A plain query reports truncation in metaData.extraData.hasMoreEvents, but
// the sort suppresses that field, so a full batch is what signals backlog
// here: worth at most one redundant poll when the window holds exactly
// searchBatchLimit events, and never misses backlog.
const searchBatchLimit = 10000

// searchIngestLag holds the ingest window's end that far behind the collector's
// clock. The window is closed on the collector's own time and never re-queried,
// so an event stamped inside it but searchable only afterwards — NG-SIEM's own
// indexing lag, or a collector clock running ahead of CrowdStrike's ingest
// clock — would be lost for good. Live probes had events searchable within
// seconds of their ingest stamp, so this margin covers both with room to spare;
// the cost is that events arrive that much later, and the @id boundary
// deduplication already covers the seam the window ends on.
const searchIngestLag = 30 * time.Second

// The fields NG-SIEM puts on every event it returns. @ingesttimestamp is the
// one the ingest window, the sort the query job is given and the checkpoint
// arithmetic are all expressed in — they have to name the same field or the
// batch cannot be resumed from where it was truncated. @id identifies an event
// within the repository, and @timestamp is the event's own time.
const (
	ingestTimestampField = "@ingesttimestamp"
	eventIDField         = "@id"
	timestampField       = "@timestamp"
)

type crowdstrikeReceiver struct {
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	logger       *zap.Logger
	nextConsumer consumer.Logs
	config       *CrowdstrikeReceiverConfig
	client       *client.CrowdStrikeAPISpecification
	pollInterval time.Duration

	// alertCheckpoint is the highest updated_timestamp consumed so far;
	// searchCheckpoint is the ingest-time lower bound of the next search
	// window. Both are only advanced after a successful ConsumeLogs, so a
	// failed delivery is retried on the next tick.
	alertCheckpoint  time.Time
	searchCheckpoint time.Time
}

// Shutdown stops the pollers and waits for the in-flight poll — including its
// ConsumeLogs call — to finish, so no records reach a torn-down pipeline.
func (r *crowdstrikeReceiver) Shutdown(ctx context.Context) error {
	if r.cancel == nil {
		return nil
	}
	r.cancel()

	stopped := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(stopped)
	}()
	select {
	case <-stopped:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("waiting for CrowdStrike pollers to stop: %w", ctx.Err())
	}
}

func (r *crowdstrikeReceiver) Start(_ context.Context, _ component.Host) error {
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel

	start := time.Now().Add(-r.config.InitialLookback)
	r.alertCheckpoint = start
	r.searchCheckpoint = start

	if !r.config.DisableAlerts {
		r.wg.Go(func() { r.poll(ctx, "alerts", r.pollAlertsOnce) })
	}
	if r.config.NGSIEMSearch.Repository != "" {
		r.wg.Go(func() { r.poll(ctx, "ngsiem_search", r.pollSearchOnce) })
	}
	return nil
}

func (r *crowdstrikeReceiver) poll(ctx context.Context, name string, once func(context.Context) error) {
	ticker := time.NewTicker(r.pollInterval)
	defer ticker.Stop()

	// The first poll goes out on start rather than an interval later: a
	// collector restarted more often than the cadence it was given — a
	// crashlooping pod on a minutes-long poll_interval — would never reach a
	// tick, and so never collect anything at all.
	for ctx.Err() == nil {
		r.logger.Debug("CrowdStrike receiver tick", zap.String("poller", name))
		if err := once(ctx); err != nil {
			r.logger.Error("CrowdStrike poll failed", zap.String("poller", name), zap.Error(err))
		}

		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
}

func (r *crowdstrikeReceiver) pollAlertsOnce(ctx context.Context) error {
	// Instead of offset pagination, each page advances the updated_timestamp
	// filter: the result set cannot shift under us while we page through it.
	// The filter is inclusive, so the alerts already consumed at the
	// checkpoint millisecond come back with every page; they are recognized by
	// composite_id and the page budget is widened to make room for them, which
	// is what keeps a tie straddling the page boundary from being skipped.
	for {
		pageStart := r.alertCheckpoint
		filter := fmt.Sprintf("updated_timestamp:>'%s'", pageStart.UTC().Format(fqlTimestampLayout))
		sort := "updated_timestamp|asc"
		limit := int64(alertPageSize)
		queried, err := r.client.Alerts.QueryV2(alerts.NewQueryV2Params().
			WithContext(ctx).
			WithFilter(&filter).
			WithSort(&sort).
			WithLimit(&limit))
		if err != nil {
			return fmt.Errorf("querying alert IDs: %w", err)
		}
		// The swagger client leaves Payload nil for a body it cannot decode,
		// so every payload access has to be guarded: a nil dereference here
		// takes the whole collector down.
		queriedPayload := queried.GetPayload()
		if queriedPayload == nil {
			return errors.New("querying alert IDs: response carried no payload")
		}
		ids := queriedPayload.Resources
		if len(ids) == 0 {
			return nil
		}

		fetched, err := r.client.Alerts.GetV2(alerts.NewGetV2Params().
			WithContext(ctx).
			WithBody(&models.DetectsapiPostEntitiesAlertsV2Request{CompositeIds: ids}))
		if err != nil {
			return fmt.Errorf("fetching alerts: %w", err)
		}

		r.backOffOnRateLimit(ctx, fetched.XRateLimitLimit, fetched.XRateLimitRemaining)

		if fetched.GetPayload() == nil {
			return fmt.Errorf("fetching alerts (trace_id %s): response carried no payload", fetched.XCSTRACEID)
		}

		logs, err := convertAlertToPlogLogs(fetched)
		if err != nil {
			return fmt.Errorf("converting alerts (trace_id %s): %w", fetched.XCSTRACEID, err)
		}
		if err := r.nextConsumer.ConsumeLogs(ctx, *logs); err != nil {
			return fmt.Errorf("consuming alerts (trace_id %s): %w", fetched.XCSTRACEID, err)
		}

		for _, alert := range fetched.Payload.Resources {
			if alert.UpdatedTimestamp != nil && time.Time(*alert.UpdatedTimestamp).After(r.alertCheckpoint) {
				r.alertCheckpoint = time.Time(*alert.UpdatedTimestamp)
			}
		}

		if len(ids) < alertPageSize {
			return nil
		}
		if !r.alertCheckpoint.After(pageStart) {
			// A full page carrying no newer updated_timestamp — alerts
			// missing the field, or more than alertPageSize of them sharing
			// one millisecond — is requeried unchanged forever. Step past it
			// and say what may have been lost.
			r.alertCheckpoint = pageStart.Add(time.Millisecond)
			r.logger.Warn("alert page did not advance the update checkpoint, stepping past it; "+
				"alerts beyond the page limit in that millisecond are not collected",
				zap.Time("updated_millisecond", pageStart),
				zap.Int("page", len(ids)))
		}
	}
}

// withoutSeenAlerts drops the alerts whose composite_id is in seen. Alerts
// without one are kept: dropping them would be a guess.
func withoutSeenAlerts(alerts []*models.DetectsAlert, seen map[string]struct{}) []*models.DetectsAlert {
	if len(seen) == 0 {
		return alerts
	}
	fresh := make([]*models.DetectsAlert, 0, len(alerts))
	for _, alert := range alerts {
		if id, ok := alertID(alert); ok {
			if _, dup := seen[id]; dup {
				continue
			}
		}
		fresh = append(fresh, alert)
	}
	return fresh
}

func alertIDsAt(alerts []*models.DetectsAlert, millis int64) map[string]struct{} {
	var ids map[string]struct{}
	for _, alert := range alerts {
		if alert.UpdatedTimestamp == nil || time.Time(*alert.UpdatedTimestamp).UnixMilli() != millis {
			continue
		}
		id, ok := alertID(alert)
		if !ok {
			continue
		}
		if ids == nil {
			ids = make(map[string]struct{})
		}
		ids[id] = struct{}{}
	}
	return ids
}

func alertID(alert *models.DetectsAlert) (string, bool) {
	if alert.CompositeID == nil {
		return "", false
	}
	return *alert.CompositeID, *alert.CompositeID != ""
}

func (r *crowdstrikeReceiver) pollSearchOnce(ctx context.Context) error {
	windowEnd := time.Now().Add(-searchIngestLag)
	if !windowEnd.After(r.searchCheckpoint) {
		// The window only opens searchIngestLag after the checkpoint, so a
		// receiver started without an initial_lookback has nothing to ask for
		// until the lag has elapsed. Asking anyway would re-query what the
		// previous window already delivered and move the checkpoint backwards.
		r.logger.Debug("NG-SIEM search window has not opened yet, skipping the tick",
			zap.Time("checkpoint", r.searchCheckpoint))
		return nil
	}

	query := r.config.NGSIEMSearch.QueryString
	if query == "" {
		query = "*"
	}
	query = fmt.Sprintf("%s | sort(@ingesttimestamp, order=asc, limit=%d)", query, searchBatchLimit)
	started, err := r.client.Ngsiem.StartSearchV1(ngsiem.NewStartSearchV1Params().
		WithContext(ctx).
		WithRepository(r.config.NGSIEMSearch.Repository).
		WithBody(&models.APIQueryJobInput{
			QueryString: &query,
			IngestStart: strconv.FormatInt(r.searchCheckpoint.UnixMilli(), 10),
			IngestEnd:   strconv.FormatInt(windowEnd.UnixMilli(), 10),
		}))
	if err != nil {
		return fmt.Errorf("starting query job: %w", err)
	}
	startedPayload := started.GetPayload()
	if startedPayload == nil || startedPayload.ID == nil {
		return errors.New("starting query job: response carried no job ID")
	}
	jobID := *startedPayload.ID
	// A query job lives on server-side until it is stopped or expires; a
	// receiver that only ever starts them leaks one per tick.
	defer r.stopSearch(ctx, jobID)

	var results *models.APIQueryJobsResults
	deadline := time.Now().Add(searchMaxWait)
	for {
		status, err := r.client.Ngsiem.GetSearchStatusV1(ngsiem.NewGetSearchStatusV1Params().
			WithContext(ctx).
			WithRepository(r.config.NGSIEMSearch.Repository).
			WithID(jobID))
		if err != nil {
			return fmt.Errorf("polling query job %s: %w", jobID, err)
		}
		results = status.GetPayload()
		if results == nil {
			return fmt.Errorf("polling query job %s: response carried no payload", jobID)
		}
		if results.Done != nil && *results.Done {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("query job %s did not complete within %s", jobID, searchMaxWait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(searchStatusPollDelay(results)):
		}
	}

	if len(results.Events) > 0 {
		logs, err := convertSearchEventsToPlogLogs(results.Events)
		if err != nil {
			return fmt.Errorf("converting query job %s events: %w", jobID, err)
		}
		if err := r.nextConsumer.ConsumeLogs(ctx, *logs); err != nil {
			return fmt.Errorf("consuming query job %s events: %w", jobID, err)
		}
	}

	// A full batch means the window holds more events than searchBatchLimit:
	// resume from the newest consumed ingest timestamp so the next tick drains
	// the remainder (events sharing that millisecond may be re-fetched),
	// instead of skipping to windowEnd and silently dropping the backlog.
	if maxIngest, ok := maxIngestTimestamp(results.Events); ok && len(results.Events) == searchBatchLimit {
		resumeFrom := time.UnixMilli(maxIngest)
		if !resumeFrom.After(r.searchCheckpoint) {
			// Bulk-ingested events can share one @ingesttimestamp millisecond;
			// when more than searchBatchLimit of them do, resuming from it
			// cannot progress. Step past it and say what may have been lost.
			resumeFrom = r.searchCheckpoint.Add(time.Millisecond)
			r.logger.Warn("NG-SIEM search stuck on one ingest millisecond holding more events than the batch limit, "+
				"stepping past it; events beyond the batch limit in that millisecond are not collected",
				zap.Time("ingest_millisecond", r.searchCheckpoint),
				zap.Int("batch", len(results.Events)))
		} else {
			r.logger.Info("NG-SIEM search window truncated at batch limit, draining backlog",
				zap.Int("batch", len(results.Events)),
				zap.Time("resume_from", resumeFrom))
		}
		r.searchCheckpoint = resumeFrom
	} else {
		r.searchCheckpoint = windowEnd
	}
	return nil
}

// stopSearch releases the server-side query job. It runs on a context detached
// from the poll one so shutdown still cleans up after itself.
func (r *crowdstrikeReceiver) stopSearch(ctx context.Context, jobID string) {
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), searchStopTimeout)
	defer cancel()

	if _, err := r.client.Ngsiem.StopSearchV1(ngsiem.NewStopSearchV1Params().
		WithContext(stopCtx).
		WithRepository(r.config.NGSIEMSearch.Repository).
		WithID(jobID)); err != nil {
		r.logger.Warn("stopping NG-SIEM query job failed, it will expire server-side",
			zap.String("job_id", jobID), zap.Error(err))
	}
}

// searchStatusPollDelay returns how long to wait before the next status poll,
// preferring the server's own hint over a fixed interval.
func searchStatusPollDelay(results *models.APIQueryJobsResults) time.Duration {
	if results.MetaData == nil || results.MetaData.PollAfter == nil {
		return searchStatusPollInterval
	}
	return min(max(time.Duration(*results.MetaData.PollAfter)*time.Millisecond, searchStatusPollFloor), searchStatusPollCeiling)
}

func maxIngestTimestamp(events []models.APIQueryJobsResultsEvents) (int64, bool) {
	maxMillis := int64(0)
	found := false
	for _, event := range events {
		fields, ok := event.(map[string]any)
		if !ok {
			continue
		}
		if millis, ok := epochMillis(fields["@ingesttimestamp"]); ok && millis > maxMillis {
			maxMillis = millis
			found = true
		}
	}
	return maxMillis, found
}

// epochMillis reads an epoch-milliseconds value that may arrive as float64,
// json.Number, or string depending on the JSON decoder in use.
func epochMillis(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case json.Number:
		millis, err := n.Int64()
		return millis, err == nil
	case string:
		millis, err := strconv.ParseInt(n, 10, 64)
		return millis, err == nil
	}
	return 0, false
}

func (r *crowdstrikeReceiver) backOffOnRateLimit(ctx context.Context, limit, remaining int64) {
	if remaining >= limit/10 {
		return
	}
	r.logger.Warn("CrowdStrike API rate limit nearly exhausted, backing off",
		zap.Int64("remaining", remaining),
		zap.Int64("limit", limit),
	)
	select {
	case <-ctx.Done():
	case <-time.After(r.pollInterval * 2):
	}
}

func convertAlertToPlogLogs(alerts *alerts.GetV2OK) (*plog.Logs, error) {
	out := plog.NewLogs()
	logs := out.ResourceLogs()
	rls := logs.AppendEmpty()
	ills := rls.ScopeLogs().AppendEmpty()

	for _, alert := range alerts.Payload.Resources {
		lr := ills.LogRecords().AppendEmpty()

		jsonMap, err := json.Marshal(alert)
		if err != nil {
			return nil, err
		}

		var rawMap map[string]any
		if err := json.Unmarshal(jsonMap, &rawMap); err != nil {
			return nil, err
		}
		if err = lr.Attributes().FromRaw(rawMap); err != nil {
			return nil, err
		}
		ts := time.Now()
		if alert.Timestamp != nil {
			ts = time.Time(*alert.Timestamp)
		}
		lr.SetTimestamp(pcommon.NewTimestampFromTime(ts))
		lr.SetObservedTimestamp(pcommon.NewTimestampFromTime(time.Now()))
		if alert.SeverityName != nil {
			lr.SetSeverityText(*alert.SeverityName)
		}
		// TODO: lr.SetSeverityNumber(...)
	}

	return &out, nil
}

func convertSearchEventsToPlogLogs(events []models.APIQueryJobsResultsEvents) (*plog.Logs, error) {
	out := plog.NewLogs()
	ills := out.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty()
	observed := pcommon.NewTimestampFromTime(time.Now())

	for _, event := range events {
		lr := ills.LogRecords().AppendEmpty()
		lr.SetObservedTimestamp(observed)

		fields, ok := event.(map[string]any)
		if !ok {
			// unexpected event shape: emit its JSON so nothing is dropped
			raw, err := json.Marshal(event)
			if err != nil {
				return nil, err
			}
			lr.Body().SetStr(string(raw))
			continue
		}

		if millis, ok := epochMillis(fields[timestampField]); ok {
			lr.SetTimestamp(pcommon.NewTimestampFromTime(time.UnixMilli(millis)))
		}
		// @rawstring is the original log line as ingested — the natural body
		// for downstream parsing; fall back to the whole event as JSON.
		if raw, ok := fields["@rawstring"].(string); ok && raw != "" {
			lr.Body().SetStr(raw)
		} else {
			raw, err := json.Marshal(fields)
			if err != nil {
				return nil, err
			}
			lr.Body().SetStr(string(raw))
		}
	}

	return &out, nil
}
