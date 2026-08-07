// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package crowdstrikereceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/crowdstrikereceiver"

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/crowdstrike/gofalcon/falcon/models"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/receiver/receiverhelper"
	"go.uber.org/zap"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/crowdstrikereceiver/internal/metadata"
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
	id           component.ID
	logger       *zap.Logger
	nextConsumer consumer.Logs
	config       *Config
	api          falconAPI
	checkpoints  *checkpointStore
	obsrecv      *receiverhelper.ObsReport

	// alertCheckpoint is the highest updated_timestamp consumed so far;
	// searchCheckpoint is the ingest-time lower bound of the next search
	// window. Both are only advanced after a successful ConsumeLogs, so a
	// failed delivery is retried on the next tick.
	alertCheckpoint  time.Time
	searchCheckpoint time.Time

	// alertBoundaryIDs holds the composite_id of the alerts already consumed
	// at alertCheckpoint's millisecond, searchBoundaryIDs the @id of the
	// events already consumed at searchCheckpoint's. Both filters are
	// inclusive at their lower bound, so those are returned again by the next
	// query.
	alertBoundaryIDs  map[string]struct{}
	searchBoundaryIDs map[string]struct{}
}

// Shutdown stops the pollers and waits for the in-flight poll — including its
// ConsumeLogs call — to finish, so no records reach a torn-down pipeline and
// the checkpoints are closed only once nothing can still write them.
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
		return r.checkpoints.client.Close(ctx)
	case <-ctx.Done():
		return fmt.Errorf("waiting for CrowdStrike pollers to stop: %w", ctx.Err())
	}
}

func (r *crowdstrikeReceiver) Start(ctx context.Context, host component.Host) error {
	client, err := getStorageClient(ctx, host, r.config.StorageID, r.id)
	if err != nil {
		return err
	}
	r.checkpoints = newCheckpointStore(client, r.logger)

	fallback := time.Now().Add(-r.config.InitialLookback)
	r.alertCheckpoint = r.checkpoints.load(ctx, alertCheckpointKey, fallback)
	r.searchCheckpoint = r.checkpoints.load(ctx, searchCheckpointKey(r.config.NGSIEMSearch.Repository), fallback)

	pollCtx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel

	if !r.config.DisableAlerts {
		r.wg.Go(func() { r.poll(pollCtx, "alerts", r.config.PollInterval, r.pollAlertsOnce) })
	}
	if r.config.NGSIEMSearch.Repository != "" {
		interval := r.config.NGSIEMSearch.PollInterval
		if interval == 0 {
			interval = r.config.PollInterval
		}
		r.wg.Go(func() { r.poll(pollCtx, "ngsiem_search", interval, r.pollSearchOnce) })
	}
	return nil
}

func (r *crowdstrikeReceiver) poll(ctx context.Context, name string, interval time.Duration, once func(context.Context) error) {
	ticker := time.NewTicker(interval)
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

// consume delivers a batch and reports it through the standard receiver
// observability, so accepted and refused record counts show up in the
// collector's own metrics like every other receiver's.
func (r *crowdstrikeReceiver) consume(ctx context.Context, logs plog.Logs) error {
	obsCtx := r.obsrecv.StartLogsOp(ctx)
	err := r.nextConsumer.ConsumeLogs(obsCtx, logs)
	r.obsrecv.EndLogsOp(obsCtx, metadata.Type.String(), logs.LogRecordCount(), err)
	return err
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
		limit := min(alertPageSize+len(r.alertBoundaryIDs), alertQueryMaxLimit)
		page, err := r.api.fetchAlerts(ctx, pageStart, limit)
		if err != nil {
			return err
		}
		// resources[] can carry a JSON null. It says nothing about an alert,
		// and every read below would dereference it — in a poll goroutine,
		// which takes the whole collector with it.
		page = slices.DeleteFunc(page, func(alert *models.DetectsAlert) bool { return alert == nil })
		if len(page) == 0 {
			return nil
		}

		fresh := withoutSeenAlerts(page, r.alertBoundaryIDs)
		if len(fresh) > 0 {
			logs, err := convertAlertToPlogLogs(fresh)
			if err != nil {
				return fmt.Errorf("converting alerts: %w", err)
			}
			if err := r.consume(ctx, *logs); err != nil {
				return fmt.Errorf("consuming alerts: %w", err)
			}
		}

		newest := pageStart
		for _, alert := range page {
			if alert.UpdatedTimestamp != nil && time.Time(*alert.UpdatedTimestamp).After(newest) {
				newest = time.Time(*alert.UpdatedTimestamp)
			}
		}
		boundary := alertIDsAt(page, newest.UnixMilli())

		if len(page) >= limit && !newest.After(pageStart) && len(boundary) <= len(r.alertBoundaryIDs) {
			// A full page that brought neither a newer updated_timestamp nor
			// an alert not already seen at this millisecond cannot be
			// drained: more than alertQueryMaxLimit of them share it, or the
			// page carries no usable updated_timestamp at all. Either way it
			// is requeried unchanged forever, so step past it and say what
			// may have been lost.
			newest = pageStart.Add(time.Millisecond)
			boundary = nil
			r.logger.Warn("alert page did not advance the update checkpoint, stepping past it; "+
				"alerts beyond the page limit in that millisecond are not collected",
				zap.Time("updated_millisecond", pageStart),
				zap.Int("page", len(page)))
		}

		r.alertCheckpoint = newest
		r.alertBoundaryIDs = boundary
		r.checkpoints.save(ctx, alertCheckpointKey, newest)

		if len(page) < limit {
			return nil
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

	events, err := r.api.runSearch(ctx, r.searchCheckpoint, windowEnd)
	if err != nil {
		return err
	}

	fresh := events
	if len(r.searchBoundaryIDs) > 0 {
		fresh = withoutSeen(events, r.searchBoundaryIDs)
		if dropped := len(events) - len(fresh); dropped > 0 {
			r.logger.Debug("dropped NG-SIEM events already consumed at the window boundary",
				zap.Int("dropped", dropped))
		}
	}

	if len(fresh) > 0 {
		logs, err := convertSearchEventsToPlogLogs(fresh)
		if err != nil {
			return fmt.Errorf("converting query job events: %w", err)
		}
		if err := r.consume(ctx, *logs); err != nil {
			return fmt.Errorf("consuming query job events: %w", err)
		}
	}

	// A full batch means the window holds more events than searchBatchLimit:
	// resume from the newest consumed ingest timestamp so the next tick drains
	// the remainder (events sharing that millisecond may be re-fetched),
	// instead of skipping to windowEnd and silently dropping the backlog.
	newestIngest, newestIDs, hasIngest := ingestBoundary(events)

	resumeFrom := windowEnd
	if len(events) == searchBatchLimit {
		switch {
		case !hasIngest:
			// Without an @ingesttimestamp there is nothing to resume from, so
			// the rest of the window is skipped and the boundary duplicates of
			// the next one go unrecognized. Only the query can cause this.
			r.logger.Warn("NG-SIEM search filled the batch limit with events carrying no readable @ingesttimestamp, "+
				"skipping to the window end; the backlog behind it is not collected and boundary duplicates are not "+
				"recognized — keep @ingesttimestamp on the events query_string selects",
				zap.Int("batch", len(events)))
		case !time.UnixMilli(newestIngest).After(r.searchCheckpoint):
			// Bulk-ingested events can share one @ingesttimestamp millisecond;
			// when more than searchBatchLimit of them do, resuming from it
			// cannot progress. Step past it and say what may have been lost.
			resumeFrom = r.searchCheckpoint.Add(time.Millisecond)
			r.logger.Warn("NG-SIEM search stuck on one ingest millisecond holding more events than the batch limit, "+
				"stepping past it; events beyond the batch limit in that millisecond are not collected",
				zap.Time("ingest_millisecond", r.searchCheckpoint),
				zap.Int("batch", len(events)))
		default:
			resumeFrom = time.UnixMilli(newestIngest)
			r.logger.Info("NG-SIEM search window truncated at batch limit, draining backlog",
				zap.Int("batch", len(events)),
				zap.Time("resume_from", resumeFrom))
		}
	}

	r.searchCheckpoint = resumeFrom
	// The next window is inclusive at its lower bound, so it returns the events
	// ingested in the resume millisecond again — but only when the poll resumed
	// from the newest millisecond it saw. Stepping past that millisecond or
	// skipping to the window end leaves nothing to recognize.
	r.searchBoundaryIDs = nil
	if hasIngest && resumeFrom.UnixMilli() == newestIngest {
		r.searchBoundaryIDs = newestIDs
	}
	r.checkpoints.save(ctx, searchCheckpointKey(r.config.NGSIEMSearch.Repository), resumeFrom)
	return nil
}

// withoutSeen drops the events whose @id is in seen. Events without an @id are
// kept: dropping them would be a guess.
func withoutSeen(events []models.APIQueryJobsResultsEvents, seen map[string]struct{}) []models.APIQueryJobsResultsEvents {
	fresh := make([]models.APIQueryJobsResultsEvents, 0, len(events))
	for _, event := range events {
		if id, ok := eventID(event); ok {
			if _, dup := seen[id]; dup {
				continue
			}
		}
		fresh = append(fresh, event)
	}
	return fresh
}

func eventID(event models.APIQueryJobsResultsEvents) (string, bool) {
	fields, ok := event.(map[string]any)
	if !ok {
		return "", false
	}
	id, ok := fields[eventIDField].(string)
	return id, ok && id != ""
}

// ingestBoundary walks a batch once for the two things the checkpoint needs
// from it: the newest @ingesttimestamp any event carries, and the @id of every
// event sharing that millisecond. It reports whether any event carried one at
// all.
func ingestBoundary(events []models.APIQueryJobsResultsEvents) (int64, map[string]struct{}, bool) {
	var (
		newest int64
		ids    map[string]struct{}
	)
	for _, event := range events {
		fields, isMap := event.(map[string]any)
		if !isMap {
			continue
		}
		millis, found := epochMillis(fields[ingestTimestampField])
		if !found || millis < newest {
			continue
		}
		if millis > newest {
			newest, ids = millis, nil
		}
		if id, hasID := eventID(event); hasID {
			if ids == nil {
				ids = make(map[string]struct{})
			}
			ids[id] = struct{}{}
		}
	}
	return newest, ids, newest > 0
}

// epochMillis reads an epoch-milliseconds field of an event. The swagger
// runtime decodes response bodies with UseNumber, so every JSON number in one
// arrives as a json.Number.
func epochMillis(v any) (int64, bool) {
	number, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	millis, err := number.Int64()
	return millis, err == nil
}

// falconSeverityNumbers maps severity_name — the API's own bucketing of the
// 1..100 severity integer, observed as Informational 10, Low 30, Medium 50,
// High 60..75, Critical 90 — onto the OTel severity scale.
var falconSeverityNumbers = map[string]plog.SeverityNumber{
	"informational": plog.SeverityNumberInfo,
	"low":           plog.SeverityNumberWarn,
	"medium":        plog.SeverityNumberWarn3,
	"high":          plog.SeverityNumberError,
	"critical":      plog.SeverityNumberFatal,
}

func convertAlertToPlogLogs(alerts []*models.DetectsAlert) (*plog.Logs, error) {
	out := plog.NewLogs()
	logs := out.ResourceLogs()
	rls := logs.AppendEmpty()
	ills := rls.ScopeLogs().AppendEmpty()

	for _, alert := range alerts {
		lr := ills.LogRecords().AppendEmpty()

		encoded, err := json.Marshal(alert)
		if err != nil {
			return nil, err
		}

		var rawMap map[string]any
		err = json.Unmarshal(encoded, &rawMap)
		if err != nil {
			return nil, err
		}
		// The alert is the payload, so it belongs in the body — the same place
		// the NG-SIEM poller puts its events, and what downstream parsers read.
		err = lr.Body().SetEmptyMap().FromRaw(rawMap)
		if err != nil {
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
			if number, ok := falconSeverityNumbers[strings.ToLower(*alert.SeverityName)]; ok {
				lr.SetSeverityNumber(number)
			}
		}
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
		if raw, ok := fields[rawStringField].(string); ok && raw != "" {
			lr.Body().SetStr(raw)
		} else {
			raw, err := json.Marshal(fields)
			if err != nil {
				return nil, err
			}
			lr.Body().SetStr(string(raw))
		}

		setSearchAttributes(lr.Attributes(), fields)
	}

	return &out, nil
}

// rawStringField holds the original log line as ingested. It is the log record
// body, so it is the one field not repeated in the attributes.
const rawStringField = "@rawstring"

// setSearchAttributes carries the whole event next to the body: the
// "#"-prefixed fields NG-SIEM computes (#Vendor, #event.dataset, #repo, …), the
// ECS and CPS schema fields, and anything CrowdStrike adds later — routing
// signals CrowdStrike already derived that cannot be recovered from
// @rawstring. There is no allowlist: whatever the query job returns is emitted.
//
// Keys are kept verbatim and nested objects and arrays stay nested, as OTLP
// kvlists and slices, so no key can be shadowed by a flattened one.
func setSearchAttributes(attrs pcommon.Map, fields map[string]any) {
	attrs.EnsureCapacity(len(fields))
	for key, value := range fields {
		if key == rawStringField {
			continue
		}
		putAttribute(attrs.PutEmpty(key), value)
	}
}

// putAttribute walks maps and slices into pdata itself rather than handing them
// to FromRaw. The swagger runtime decodes with UseNumber — which is what keeps
// large integer ids exact — so every number of an event is a json.Number at
// whatever depth it sits, and FromRaw rejects one outright: it would fail on the
// whole object holding a nested number, not just on the number.
//
// It cannot fail: a value pdata does not model is stringified instead of failing
// the conversion, which would hold the poller on its checkpoint and re-query the
// same batch forever.
func putAttribute(dst pcommon.Value, value any) {
	switch typed := value.(type) {
	case json.Number:
		if integer, err := typed.Int64(); err == nil {
			dst.SetInt(integer)
			return
		}
		if float, err := typed.Float64(); err == nil {
			dst.SetDouble(float)
			return
		}
	case map[string]any:
		nested := dst.SetEmptyMap()
		nested.EnsureCapacity(len(typed))
		for key, element := range typed {
			putAttribute(nested.PutEmpty(key), element)
		}
		return
	case []any:
		nested := dst.SetEmptySlice()
		nested.EnsureCapacity(len(typed))
		for _, element := range typed {
			putAttribute(nested.AppendEmpty(), element)
		}
		return
	}
	if err := dst.FromRaw(value); err != nil {
		dst.SetStr(fmt.Sprintf("%v", value))
	}
}
