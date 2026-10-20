package api

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/gowthamgts/cronpad/internal/store/historygen"
)

// retainedStats uses complete UTC day buckets when raw history no longer covers
// the requested window. Percentiles are exact across retained lag histograms.
// DECISION: the oldest partial day is included in long-window statistics.
func (j *Jobs) retainedStats(ctx context.Context, workspace, job string, window time.Duration) (map[string]int64, map[string]int64, error) {
	now := j.Clock.Now()
	from := now.Add(-window).UnixMilli()
	day := int64(0)
	if retention := j.Config.Retention.Runs.Duration(); retention > 0 && window > retention {
		day = (now.Add(-retention).UnixMilli() + 86400000 - 1) / 86400000
	}
	raw := from
	if day > 0 {
		raw = day * 86400000
	}
	p := historygen.RetainedTotalsParams{WorkspaceID: workspace, JobID: job, RawFrom: raw, FromDay: from / 86400000, RawDay: day}
	counts, err := j.Store.HistoryReader().RetainedTotals(ctx, p)
	if err != nil {
		return nil, nil, fmt.Errorf("retained totals: %w", err)
	}
	lags, err := j.Store.HistoryReader().RetainedLags(ctx, historygen.RetainedLagsParams(p))
	if err != nil {
		return nil, nil, fmt.Errorf("retained lags: %w", err)
	}
	var n int64
	for _, l := range lags {
		n += l.N
	}
	percentiles := map[string]int64{}
	for name, pct := range map[string]float64{"p50": .5, "p95": .95, "p99": .99} {
		percentiles[name] = 0
		var cumulative int64
		for _, l := range lags {
			cumulative += l.N
			if cumulative >= int64(math.Ceil(float64(n)*pct)) {
				percentiles[name] = l.LagMs.Int64
				break
			}
		}
	}
	return map[string]int64{"total": counts.Runs, "succeeded": counts.Succeeded, "failed": counts.Failed, "sla_breached": counts.SlaBreached}, percentiles, nil
}
