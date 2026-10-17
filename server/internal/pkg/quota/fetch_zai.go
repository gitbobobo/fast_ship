package quota

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// 智谱 GLM Coding Plan 取数（国内 BigModel open.bigmodel.cn，也兼容 api.z.ai）。
// quota/limit 响应的 limits 数组里：TOKENS_LIMIT / CREDIT_LIMIT 是 Coding Plan 窗口，
// TIME_LIMIT 是 MCP 次数窗口；时长由 unit + number 组合决定。

// zaiFiveHourSkew 是 5 小时窗口重置时间的容忍偏移。超过「5 小时 + 1 分钟」的
// 未来重置时间视为不兼容的缓存值，直接省略（docs/zai.md 的 Parsing + mapping 规则）。
const zaiFiveHourSkew = 5*time.Hour + time.Minute

func fetchZai(ctx context.Context, client *http.Client, baseURL string, creds Credentials) (Snapshot, error) {
	key := strings.TrimSpace(creds["api_key"])
	if key == "" {
		return Snapshot{}, CredentialError("缺少智谱 API Key。", 0)
	}
	headers := map[string]string{
		"Authorization": "Bearer " + key,
		"Accept":        "application/json",
	}

	status, body, err := httpGet(ctx, client, strings.TrimRight(baseURL, "/")+"/api/monitor/usage/quota/limit", headers)
	if err != nil {
		return Snapshot{}, err
	}
	if err := requireOK("智谱", status); err != nil {
		return Snapshot{}, err
	}

	root, err := jsonRoot(body)
	if err != nil {
		return Snapshot{}, err
	}
	// 信封校验：success=false 或 code!=200 都是接口层失败。
	if success, ok := asBool(root["success"]); ok && !success {
		return Snapshot{}, ProviderError("智谱接口返回失败。", status)
	}
	if code, ok := asNumber(root["code"]); ok && code != 200 {
		return Snapshot{}, ProviderError("智谱接口返回失败。", status)
	}
	data, ok := asObject(root["data"])
	if !ok {
		return Snapshot{}, ProviderError("智谱响应缺少额度数据，接口可能已变更。", 0)
	}

	limits, ok := data["limits"].([]any)
	if !ok {
		return Snapshot{}, ProviderError("智谱响应缺少额度列表，接口可能已变更。", 0)
	}

	windows := []Window{}
	now := time.Now().UTC()
	for _, raw := range limits {
		entry, ok := asObject(raw)
		if !ok {
			continue
		}
		window, ok := zaiWindow(entry, now)
		if !ok {
			continue
		}
		windows = append(windows, window)
	}
	if len(windows) == 0 {
		return Snapshot{}, ProviderError("智谱响应里没有可识别的额度窗口，接口可能已变更。", 0)
	}
	sortWindows(windows)

	planName, _ := firstString(data, "planName", "plan", "plan_type", "packageName", "level")

	return Snapshot{PlanName: planName, Windows: windows, FetchedAt: time.Now().UTC()}, nil
}

// zaiWindow 解析单个 limit 条目。百分比优先用 usage/currentValue（或 remaining）计数推算，
// 否则回落到 percentage 字段；两者都没有的条目跳过。
func zaiWindow(entry map[string]any, now time.Time) (Window, bool) {
	entryType, ok := asString(entry["type"])
	if !ok {
		return Window{}, false
	}
	isMCP := entryType == "TIME_LIMIT"
	if !isMCP && entryType != "TOKENS_LIMIT" && entryType != "CREDIT_LIMIT" {
		return Window{}, false
	}

	percent, used, limit := zaiPercentAndCounts(entry)
	if percent == nil {
		return Window{}, false
	}

	resetAt, _ := flexibleDate(entry["nextResetTime"])

	if isMCP {
		window := percentWindow(WindowOther, "MCP 次数", *percent, resetPtr(resetAt))
		window.Unit = UnitCount
		if used != nil {
			window.Used = PercentPtr(Round1(*used))
		}
		if limit != nil {
			window.Limit = PercentPtr(Round1(*limit))
		}
		return window, true
	}

	kind, label := zaiPlanWindow(entryType, entry)
	// 5 小时 Coding Plan 窗口：过远（> 5h + 1min 时钟偏移）的未来重置时间
	// 一律省略（含不兼容的缓存重置），百分比保留；每周 / MCP 语义不变。
	reset := resetPtr(resetAt)
	if kind == WindowFiveHour && resetAt.After(now.Add(zaiFiveHourSkew)) {
		reset = nil
	}
	return percentWindow(kind, label, *percent, reset), true
}

// zaiPercentAndCounts 从计数推算百分比，推不出再读 percentage 字段。
// 返回的 used/limit 仅在计数完整时给出（用于 MCP 次数窗口）。
func zaiPercentAndCounts(entry map[string]any) (percent *float64, used, limit *float64) {
	usage, hasUsage := asNumber(entry["usage"])
	if hasUsage && usage > 0 {
		limit = &usage
		if current, ok := asNumber(entry["currentValue"]); ok {
			u := current
			p := ClampPercent(current / usage * 100)
			return &p, &u, limit
		}
		if remaining, ok := asNumber(entry["remaining"]); ok {
			u := usage - remaining
			p := ClampPercent(u / usage * 100)
			return &p, &u, limit
		}
		limit = nil
	}
	if p, ok := asNumber(entry["percentage"]); ok {
		return &p, nil, nil
	}
	return nil, nil, nil
}

// zaiPlanWindow 由 unit+number 推出 Coding Plan 窗口类型。
// 官方样例里确认的单位：1=天（30 天按月度窗口处理）、3=小时、6=周；
// 其余单位含义未确认，一律按 unknown 时长归为 other。
func zaiPlanWindow(entryType string, entry map[string]any) (kind, label string) {
	unit, _ := asNumber(entry["unit"])
	number, _ := asNumber(entry["number"])
	var minutes int
	switch unit {
	case 3:
		minutes = int(number * 60)
	case 1:
		minutes = int(number * 1440)
	case 6:
		minutes = int(number * 10080)
	default:
		minutes = 0
	}
	if minutes > 0 {
		if kind, label = windowForMinutes(minutes); label != "" {
			return kind, label
		}
	}
	if entryType == "CREDIT_LIMIT" {
		return WindowOther, "Credit 额度"
	}
	return WindowOther, "Token 额度"
}
