package accounts

import "context"

// DashboardStats 消费与管理列表相同的账号投影，单次快照聚合，不复制凭据或额度状态规则。
func (r *Repository) DashboardStats(parent context.Context) (map[string]any, error) {
	items, err := r.snapshot(parent)
	if err != nil {
		return nil, err
	}
	groups, err := loadGroups(parent, r.client)
	if err != nil {
		return nil, err
	}
	stats := map[string]any{"total": int64(len(items)), "cumulative_total": int64(len(items)), "active": int64(0), "limited": int64(0), "abnormal": int64(0), "disabled": int64(0), "total_quota": int64(0), "unlimited_quota_count": int64(0), "unknown_quota_count": int64(0), "total_success": int64(0), "total_fail": int64(0), "by_type": map[string]int64{}}
	r.mu.Lock()
	defer r.mu.Unlock()
	healthy := false
	for _, record := range items {
		item := project(record.data, groups, r.inflight[accountID(record.data)])
		if item["available"] == true && item["status_category"] == "normal" {
			healthy = true
		}
		if key := map[string]string{"normal": "active", "limited": "limited", "abnormal": "abnormal", "disabled": "disabled"}[text(item["status_category"])]; key != "" {
			stats[key] = stats[key].(int64) + 1
		}
		stats["total_quota"] = stats["total_quota"].(int64) + int64(number(item["quota_remaining"]))
		if item["quota_unlimited"] == true {
			stats["unlimited_quota_count"] = stats["unlimited_quota_count"].(int64) + 1
		} else if item["quota_unknown"] == true {
			stats["unknown_quota_count"] = stats["unknown_quota_count"].(int64) + 1
		}
		stats["total_success"] = stats["total_success"].(int64) + int64(number(item["success_count"]))
		stats["total_fail"] = stats["total_fail"].(int64) + int64(number(item["failure_count"]))
		plan := text(item["plan"])
		if plan == "" {
			plan = "unknown"
		}
		stats["by_type"].(map[string]int64)[plan]++
	}
	stats["healthy"] = healthy
	return stats, nil
}
