package observability

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"chatgpt2api/go_backend/internal/callrecord"
)

type modelMetric struct {
	Success, DurationCount int64
	Duration               float64
}
type metricBucket struct {
	Total, Success, Failed, SwitchRequests, SwitchCount, SwitchRecovered, DurationCount int64
	Duration                                                                            float64
	Models                                                                              map[string]modelMetric
}

func cutoff(now time.Time) string {
	return now.In(beijing).AddDate(0, 0, -29).Format("2006-01-02") + "T00"
}

// prepareProjection 只在缺失或显式世代变化时重建。日志删除保留既有聚合与世代。
// 使用数据库聚合，避免把全部日志或账号凭据载入指标进程。
func prepareProjection(ctx context.Context, tx *sql.Tx, now time.Time, currentID string) (bool, error) {
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtext('dashboard_metrics'))"); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO call_record_state(id,generation) VALUES(1,'go-'||md5(random()::text||clock_timestamp()::text)) ON CONFLICT(id) DO NOTHING"); err != nil {
		return false, err
	}
	var generation, projected string
	var cursor int64
	if err := tx.QueryRowContext(ctx, "SELECT generation FROM call_record_state WHERE id=1").Scan(&generation); err != nil {
		return false, err
	}
	err := tx.QueryRowContext(ctx, "SELECT call_record_generation,last_sequence FROM dashboard_metric_state WHERE id=1").Scan(&projected, &cursor)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	rebuilt := err != nil || projected != generation
	if rebuilt {
		cursor = 0
		if _, err = tx.ExecContext(ctx, "DELETE FROM dashboard_metric_hourly"); err != nil {
			return false, err
		}
	}
	var currentSequence int64
	if currentID != "" {
		if err = tx.QueryRowContext(ctx, "SELECT sequence FROM call_records WHERE id=$1", currentID).Scan(&currentSequence); err != nil {
			return false, err
		}
	}
	// 序号之后的既有终态在数据库内增量摄取；较小序号稍后完成时由同一终态事务单独增加。
	const source = `WITH source AS (SELECT substring(replace(event_time,' ','T'),1,13) AS bucket, model, outcome, CASE WHEN COALESCE(payload::jsonb#>>'{detail,duration_ms}','') ~ '^[0-9]{1,15}(\.[0-9]{1,4})?$' THEN (payload::jsonb#>>'{detail,duration_ms}')::double precision END AS duration, CASE WHEN COALESCE(payload::jsonb#>>'{detail,switch_count}','') ~ '^[0-9]{1,6}$' THEN (payload::jsonb#>>'{detail,switch_count}')::bigint ELSE 0 END AS switches FROM call_records WHERE type='call' AND outcome NOT IN ('running','queued') AND event_day >= $1 AND sequence>$2)`
	_, err = tx.ExecContext(ctx, source+` INSERT INTO dashboard_metric_hourly(bucket_start,total,success,final_failed,switch_requests,switch_count,switch_recovered,success_duration_total_ms,success_duration_count) SELECT bucket,count(*),count(*) FILTER(WHERE outcome IN ('success','partial_success')),count(*) FILTER(WHERE outcome NOT IN ('success','partial_success','text_review')),count(*) FILTER(WHERE switches>0),COALESCE(sum(switches),0),count(*) FILTER(WHERE switches>0 AND outcome IN ('success','partial_success')),COALESCE(sum(duration) FILTER(WHERE outcome IN ('success','partial_success')),0),count(duration) FILTER(WHERE outcome IN ('success','partial_success')) FROM source GROUP BY bucket ON CONFLICT(bucket_start) DO UPDATE SET total=dashboard_metric_hourly.total+excluded.total,success=dashboard_metric_hourly.success+excluded.success,final_failed=dashboard_metric_hourly.final_failed+excluded.final_failed,switch_requests=dashboard_metric_hourly.switch_requests+excluded.switch_requests,switch_count=dashboard_metric_hourly.switch_count+excluded.switch_count,switch_recovered=dashboard_metric_hourly.switch_recovered+excluded.switch_recovered,success_duration_total_ms=dashboard_metric_hourly.success_duration_total_ms+excluded.success_duration_total_ms,success_duration_count=dashboard_metric_hourly.success_duration_count+excluded.success_duration_count`, cutoff(now)[:10], cursor)
	if err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, source+` INSERT INTO dashboard_metric_model_hourly(bucket_start,model,success,success_duration_total_ms,success_duration_count) SELECT bucket,model,count(*),COALESCE(sum(duration),0),count(duration) FROM source WHERE outcome IN ('success','partial_success') AND model<>'' GROUP BY bucket,model ON CONFLICT(bucket_start,model) DO UPDATE SET success=dashboard_metric_model_hourly.success+excluded.success,success_duration_total_ms=dashboard_metric_model_hourly.success_duration_total_ms+excluded.success_duration_total_ms,success_duration_count=dashboard_metric_model_hourly.success_duration_count+excluded.success_duration_count`, cutoff(now)[:10], cursor)
	if err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO dashboard_metric_state(id,call_record_generation,last_sequence,status,failure_reason,last_event_id,last_event_at,checkpoint_at,retention_cutoff,revision,updated_at) VALUES(1,$1,COALESCE((SELECT max(sequence) FROM call_records),0),'ready',NULL,NULL,NULL,$2,$3,1,$4) ON CONFLICT(id) DO UPDATE SET call_record_generation=excluded.call_record_generation,last_sequence=GREATEST(dashboard_metric_state.last_sequence,excluded.last_sequence),status='ready',failure_reason=NULL,checkpoint_at=excluded.checkpoint_at,retention_cutoff=excluded.retention_cutoff,revision=dashboard_metric_state.revision+1,updated_at=excluded.updated_at`, generation, stamp(now), cutoff(now), now)
	if err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM dashboard_metric_hourly WHERE bucket_start<$1", cutoff(now)); err != nil {
		return false, err
	}
	return rebuilt || currentSequence > cursor, nil
}
func persistMetric(ctx context.Context, tx *sql.Tx, record callrecord.Record) error {
	now := time.Now()
	rebuilt, err := prepareProjection(ctx, tx, now, record.ID)
	if err != nil {
		return err
	}
	ended := record.Ended
	if ended.IsZero() {
		ended = now
	}
	started := record.Started.In(beijing)
	bucket := started.Format("2006-01-02T15")
	if !rebuilt && bucket >= cutoff(now) {
		success, failed := int64(0), int64(0)
		if record.Outcome == "success" || record.Outcome == "partial_success" {
			success = 1
		} else if record.Outcome != "text_review" {
			failed = 1
		}
		duration := float64(max(0, ended.Sub(record.Started).Milliseconds())) * float64(success)
		switchRequest := int64(0)
		if record.Switches > 0 {
			switchRequest = 1
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO dashboard_metric_hourly(bucket_start,total,success,final_failed,switch_requests,switch_count,switch_recovered,success_duration_total_ms,success_duration_count) VALUES($1,1,$2,$3,$5,$6,$7,$4,$2) ON CONFLICT(bucket_start) DO UPDATE SET total=dashboard_metric_hourly.total+1,success=dashboard_metric_hourly.success+excluded.success,final_failed=dashboard_metric_hourly.final_failed+excluded.final_failed,switch_requests=dashboard_metric_hourly.switch_requests+excluded.switch_requests,switch_count=dashboard_metric_hourly.switch_count+excluded.switch_count,switch_recovered=dashboard_metric_hourly.switch_recovered+excluded.switch_recovered,success_duration_total_ms=dashboard_metric_hourly.success_duration_total_ms+excluded.success_duration_total_ms,success_duration_count=dashboard_metric_hourly.success_duration_count+excluded.success_duration_count`, bucket, success, failed, duration, switchRequest, max(0, record.Switches), switchRequest*success)
		if err != nil {
			return err
		}
		if success > 0 && record.Model != "" {
			_, err = tx.ExecContext(ctx, `INSERT INTO dashboard_metric_model_hourly(bucket_start,model,success,success_duration_total_ms,success_duration_count) VALUES($1,$2,1,$3,1) ON CONFLICT(bucket_start,model) DO UPDATE SET success=dashboard_metric_model_hourly.success+1,success_duration_total_ms=dashboard_metric_model_hourly.success_duration_total_ms+excluded.success_duration_total_ms,success_duration_count=dashboard_metric_model_hourly.success_duration_count+1`, bucket, record.Model, duration)
			if err != nil {
				return err
			}
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE dashboard_metric_state SET last_sequence=GREATEST(last_sequence,COALESCE((SELECT sequence FROM call_records WHERE id=$1),0)),status='ready',failure_reason=NULL,last_event_id=$1,last_event_at=$2,checkpoint_at=$3,revision=revision+1,updated_at=$4 WHERE id=1`, record.ID, stamp(ended), stamp(now), now)
	return err
}
func (s *Service) metricSnapshot(parent context.Context, now time.Time) (map[string]metricBucket, map[string]any, error) {
	buckets := map[string]metricBucket{}
	metrics := map[string]any{"status": "degraded", "ready": false, "stale": true, "source": "call_records_hourly", "source_revision": nil, "last_ingested_at": nil, "freshness_ms": nil, "checkpoint_at": nil, "failure_reason": "metrics_unavailable", "retention_days": 30}
	if s.db == nil {
		return buckets, metrics, callrecord.ErrStorage
	}
	tx, err := s.db.BeginTx(parent, nil)
	if err != nil {
		return buckets, metrics, err
	}
	defer tx.Rollback()
	if _, err = prepareProjection(parent, tx, now, ""); err != nil {
		return buckets, metrics, err
	}
	var generation string
	var sequence int64
	var last, checkpoint sql.NullString
	var revision int64
	if err = tx.QueryRowContext(parent, "SELECT call_record_generation,last_sequence,last_event_at,checkpoint_at,revision FROM dashboard_metric_state WHERE id=1").Scan(&generation, &sequence, &last, &checkpoint, &revision); err != nil {
		return buckets, metrics, err
	}
	rows, err := tx.QueryContext(parent, `SELECT bucket_start,total,success,final_failed,switch_requests,switch_count,switch_recovered,success_duration_total_ms,success_duration_count FROM dashboard_metric_hourly WHERE bucket_start >= $1 ORDER BY bucket_start LIMIT 744`, cutoff(now))
	if err != nil {
		return buckets, metrics, err
	}
	for rows.Next() {
		var key string
		var b metricBucket
		if err = rows.Scan(&key, &b.Total, &b.Success, &b.Failed, &b.SwitchRequests, &b.SwitchCount, &b.SwitchRecovered, &b.Duration, &b.DurationCount); err != nil {
			rows.Close()
			return buckets, metrics, err
		}
		b.Models = map[string]modelMetric{}
		buckets[key] = b
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return buckets, metrics, err
	}
	// 最多 100 个成功模型；主调用总数保持完整，限制仅用于图例和系列规模。
	rows, err = tx.QueryContext(parent, `WITH retained_models AS (SELECT model FROM dashboard_metric_model_hourly WHERE bucket_start >= $1 GROUP BY model ORDER BY sum(success) DESC,model LIMIT 100) SELECT bucket_start,model,success,success_duration_total_ms,success_duration_count FROM dashboard_metric_model_hourly WHERE bucket_start >= $1 AND model IN (SELECT model FROM retained_models) ORDER BY bucket_start,model LIMIT 74400`, cutoff(now))
	if err != nil {
		return buckets, metrics, err
	}
	for rows.Next() {
		var key, model string
		var m modelMetric
		if err = rows.Scan(&key, &model, &m.Success, &m.Duration, &m.DurationCount); err != nil {
			rows.Close()
			return buckets, metrics, err
		}
		if b, ok := buckets[key]; ok {
			b.Models[text(model)] = m
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return buckets, metrics, err
	}
	if err = tx.Commit(); err != nil {
		return buckets, metrics, err
	}
	metrics["status"] = "ready"
	metrics["ready"] = true
	metrics["stale"] = false
	metrics["failure_reason"] = nil
	metrics["source_revision"] = fmt.Sprintf("%s:%d:%d", generation, sequence, revision)
	if last.Valid {
		metrics["last_ingested_at"] = last.String
		if timestamp, err := time.Parse(time.RFC3339Nano, last.String); err == nil {
			metrics["freshness_ms"] = max(0, now.Sub(timestamp).Milliseconds())
		}
	}
	if checkpoint.Valid {
		metrics["checkpoint_at"] = checkpoint.String
	}
	return buckets, metrics, nil
}
func ratio(n, d int64) any {
	if d <= 0 {
		return nil
	}
	return math.Round(float64(n)*10000/float64(d)) / 100
}
func average(total float64, count int64) any {
	if count <= 0 {
		return nil
	}
	return math.Round(total/float64(count)*100) / 100
}
func mergeBucket(target *metricBucket, source metricBucket) {
	target.Total += source.Total
	target.Success += source.Success
	target.Failed += source.Failed
	target.SwitchRequests += source.SwitchRequests
	target.SwitchCount += source.SwitchCount
	target.SwitchRecovered += source.SwitchRecovered
	target.Duration += source.Duration
	target.DurationCount += source.DurationCount
	if target.Models == nil {
		target.Models = map[string]modelMetric{}
	}
	for name, m := range source.Models {
		previous := target.Models[name]
		previous.Success += m.Success
		previous.Duration += m.Duration
		previous.DurationCount += m.DurationCount
		target.Models[name] = previous
	}
}
func projectRange(key string, now time.Time, hourly map[string]metricBucket) map[string]any {
	now = now.In(beijing)
	count, unit := 24, "hour"
	start := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, beijing).Add(-23 * time.Hour)
	if key != "24h" {
		unit = "day"
		count = 7
		if key == "30d" {
			count = 30
		}
		start = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, beijing).AddDate(0, 0, 1-count)
	}
	labels := []any{}
	successes := []any{}
	failures := []any{}
	rates := []any{}
	switches := []any{}
	buckets := []any{}
	models := map[string][]any{}
	modelTimes := map[string][]any{}
	aggregate := metricBucket{}
	points := make([]metricBucket, count)
	for hour, metric := range hourly {
		at, err := time.ParseInLocation("2006-01-02T15", hour, beijing)
		if err != nil || at.Before(start) || at.After(now) {
			continue
		}
		index := int(at.Sub(start).Hours())
		if unit == "day" {
			index /= 24
		}
		if index >= 0 && index < count {
			mergeBucket(&points[index], metric)
		}
	}
	names := map[string]bool{}
	for _, point := range points {
		for name := range point.Models {
			names[name] = true
		}
	}
	ordered := []string{}
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	for i, point := range points {
		at, end := start.Add(time.Duration(i)*time.Hour), start.Add(time.Duration(i+1)*time.Hour)
		label := at.Format("15:04")
		if unit == "day" {
			at = start.AddDate(0, 0, i)
			end = at.AddDate(0, 0, 1)
			label = at.Format("01-02")
		}
		labels = append(labels, label)
		successes = append(successes, point.Success)
		failures = append(failures, point.Failed)
		rates = append(rates, ratio(point.Success, point.Success+point.Failed))
		switches = append(switches, point.SwitchCount)
		mergeBucket(&aggregate, point)
		buckets = append(buckets, map[string]any{"label": label, "start_at": at.Format(time.RFC3339), "end_at": end.Format(time.RFC3339), "total_calls": point.Total, "success_calls": point.Success, "final_failed_calls": point.Failed, "success_rate": ratio(point.Success, point.Success+point.Failed), "avg_success_duration_ms": average(point.Duration, point.DurationCount), "switch_count": point.SwitchCount, "switch_recovered": point.SwitchRecovered, "switch_recovery_rate": ratio(point.SwitchRecovered, point.SwitchRequests)})
		for _, name := range ordered {
			m := point.Models[name]
			models[name] = append(models[name], m.Success)
			modelTimes[name] = append(modelTimes[name], average(m.Duration, m.DurationCount))
		}
	}
	end := start.Add(time.Duration(count) * time.Hour)
	if unit == "day" {
		end = start.AddDate(0, 0, count)
	}
	return map[string]any{"time_range": key, "window": map[string]any{"requested": key, "start_at": start.Format(time.RFC3339), "end_at": end.Format(time.RFC3339), "bucket_unit": unit, "bucket_count": count}, "totals": map[string]any{"total": aggregate.Total, "success": aggregate.Success, "final_failed": aggregate.Failed, "success_rate": ratio(aggregate.Success, aggregate.Success+aggregate.Failed), "avg_success_duration_ms": average(aggregate.Duration, aggregate.DurationCount)}, "switching": map[string]any{"requests": aggregate.SwitchRequests, "count": aggregate.SwitchCount, "recovered": aggregate.SwitchRecovered, "recovery_rate": ratio(aggregate.SwitchRecovered, aggregate.SwitchRequests)}, "buckets": buckets, "trend": map[string]any{"labels": labels, "success_requests": successes, "final_failed_requests": failures, "success_rate": rates, "switch_count": switches, "model_success_requests": models, "model_avg_success_duration_ms": modelTimes}}
}

func (s *Service) Dashboard(parent context.Context) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	now := time.Now()
	hourly, metrics, metricErr := s.metricSnapshot(ctx, now)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	accountStats := map[string]any{"total": int64(0), "cumulative_total": int64(0), "active": int64(0), "limited": int64(0), "abnormal": int64(0), "disabled": int64(0), "total_quota": int64(0), "unlimited_quota_count": int64(0), "unknown_quota_count": int64(0), "total_success": int64(0), "total_fail": int64(0), "by_type": map[string]int64{}, "healthy": false}
	// 账号有效状态消费账号仓储权威投影，指标层不重新判断 AT/RT 或额度。
	if s.accounts != nil {
		var err error
		accountStats, err = s.accounts.DashboardStats(ctx)
		if err != nil {
			return nil, canceled(ctx, err)
		}
	}
	imageStorage := map[string]any{"enabled": true, "mode": "local", "status": "not_checked", "available": nil, "image_count": nil, "image_size_bytes": nil}
	if s.settings != nil {
		configuration, err := s.settings.Runtime(ctx)
		if err != nil {
			return nil, canceled(ctx, err)
		}
		image := object(configuration["image_storage"])
		imageStorage["enabled"] = image["enabled"] == true
		if mode := text(image["mode"]); mode != "" {
			imageStorage["mode"] = mode
		}
	}
	runtimeView := s.runtime.snapshot(ctx, time.Now())
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	ranges := map[string]any{}
	for _, key := range []string{"24h", "7d", "30d"} {
		ranges[key] = projectRange(key, now, hourly)
	}
	healthy := accountStats["healthy"] == true && metricErr == nil
	status := "degraded"
	if healthy {
		status = "ok"
	}
	return map[string]any{"status": status, "healthy": healthy, "version": s.options.Version, "meta": map[string]any{"schema_version": 5, "generated_at": stamp(now), "available_ranges": []string{"24h", "7d", "30d"}}, "metrics": metrics, "runtime": runtimeView, "operations": map[string]any{"active_requests": s.ActiveCount()}, "accounts": accountStats, "storage": map[string]any{"application_database": map[string]any{"type": "postgresql"}, "image_storage": imageStorage}, "ranges": ranges}, nil
}
