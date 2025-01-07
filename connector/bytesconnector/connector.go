// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package bytesconnector // import import "github.com/open-telemetry/opentelemetry-collector-contrib/connector/bytesconnector"

import (
	"context"
	"errors"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"

	"github.com/open-telemetry/opentelemetry-collector-contrib/connector/bytesconnector/internal/metadata"
	"github.com/open-telemetry/opentelemetry-collector-contrib/pkg/ottl/contexts/ottllog"
)

// count can count spans, span event, metrics, data points, or log records
// and emit the counts onto a metrics pipeline.
type count struct {
	metricsConsumer consumer.Metrics
	component.StartFunc
	component.ShutdownFunc

	logsMetricDefs map[string]metricDef[ottllog.TransformContext]
}

func (c *count) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: false}
}

func (c *count) ConsumeLogs(ctx context.Context, ld plog.Logs) error {
	var multiError error
	countMetrics := pmetric.NewMetrics()
	logSizer := &plog.ProtoMarshaler{}
	bytesSize := uint64(logSizer.LogsSize(ld))
	countMetrics.ResourceMetrics().EnsureCapacity(ld.ResourceLogs().Len())
	for i := 0; i < ld.ResourceLogs().Len(); i++ {
		resourceLog := ld.ResourceLogs().At(i)
		counter := newCounter[ottllog.TransformContext](c.logsMetricDefs)

		for j := 0; j < resourceLog.ScopeLogs().Len(); j++ {
			scopeLogs := resourceLog.ScopeLogs().At(j)

			for k := 0; k < scopeLogs.LogRecords().Len(); k++ {
				logRecord := scopeLogs.LogRecords().At(k)
				multiError = errors.Join(multiError, counter.update(ctx, logRecord.Attributes(), bytesSize))
			}
		}

		if len(counter.counts) == 0 {
			continue // don't add an empty resource
		}

		countResource := countMetrics.ResourceMetrics().AppendEmpty()
		resourceLog.Resource().Attributes().CopyTo(countResource.Resource().Attributes())

		countResource.ScopeMetrics().EnsureCapacity(resourceLog.ScopeLogs().Len())
		countScope := countResource.ScopeMetrics().AppendEmpty()
		countScope.Scope().SetName(metadata.ScopeName)

		counter.appendMetricsTo(countScope.Metrics())
	}
	if multiError != nil {
		return multiError
	}
	return c.metricsConsumer.ConsumeMetrics(ctx, countMetrics)
}
