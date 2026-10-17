package quota

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// OpenCode Go 取数：API key 走公开的 usage 接口（浏览器 cookie 才能用的 Console
// 路由不在本服务范围内）。rolling/weekly/monthly 三个桶分别对应 5 小时/每周/每月。

// 与 CodexBar 的 OpenCodeWebParsing 保持一致的宽容键名。
var (
	openCodePercentKeys = []string{
		"usagePercent", "usedPercent", "percentUsed", "percent", "usage_percent",
		"used_percent", "utilization", "utilizationPercent", "utilization_percent", "usage",
	}
	openCodeResetInKeys = []string{
		"resetInSec", "resetInSeconds", "resetSeconds", "reset_sec", "reset_in_sec",
		"resetsInSec", "resetsInSeconds", "resetIn", "resetSec",
	}
	openCodeResetAtKeys = []string{
		"resetAt", "resetsAt", "reset_at", "resets_at", "nextReset", "next_reset", "renewAt", "renew_at",
	}
	openCodeUsedKeys  = []string{"used", "usage", "consumed", "count", "usedTokens", "usedMicroCents"}
	openCodeLimitKeys = []string{"limit", "total", "quota", "max", "cap", "tokenLimit", "limitMicroCents"}
)

func fetchOpenCodeGo(ctx context.Context, client *http.Client, baseURL string, creds Credentials) (Snapshot, error) {
	key := strings.TrimSpace(creds["api_key"])
	if key == "" {
		return Snapshot{}, CredentialError("缺少 OpenCode API Key。", 0)
	}
	headers := map[string]string{
		"Authorization": "Bearer " + key,
		"Accept":        "application/json",
	}

	status, body, err := httpGet(ctx, client, strings.TrimRight(baseURL, "/")+"/zen/go/v1/usage", headers)
	if err != nil {
		return Snapshot{}, err
	}
	if err := requireOK("OpenCode Go", status); err != nil {
		return Snapshot{}, err
	}

	root, err := jsonRoot(body)
	if err != nil {
		return Snapshot{}, err
	}
	usage, ok := asObject(root["usage"])
	if !ok {
		return Snapshot{}, ProviderError("OpenCode Go 响应缺少用量数据，接口可能已变更。", 0)
	}
	rolling, ok := asObject(usage["rolling"])
	if !ok {
		return Snapshot{}, ProviderError("OpenCode Go 响应缺少滚动用量窗口。", 0)
	}

	now := time.Now().UTC()
	rollingWindow, ok := openCodeWindow(rolling, now, WindowFiveHour, "5 小时")
	if !ok {
		return Snapshot{}, ProviderError("OpenCode Go 响应缺少用量字段。", 0)
	}
	windows := []Window{rollingWindow}

	// weekly 桶存在但解析失败说明接口结构变了，按解析失败处理；
	// monthly 桶解析失败则容忍（与 CodexBar 的 buildSnapshot 行为一致）。
	if weeklyObj, present := asObject(usage["weekly"]); present {
		weekly, ok := openCodeWindow(weeklyObj, now, WindowWeekly, "每周")
		if !ok {
			return Snapshot{}, ProviderError("OpenCode Go 响应缺少用量字段。", 0)
		}
		windows = append(windows, weekly)
	}
	if monthlyObj, present := asObject(usage["monthly"]); present {
		if monthly, ok := openCodeWindow(monthlyObj, now, WindowMonthly, "每月"); ok {
			windows = append(windows, monthly)
		}
	}
	sortWindows(windows)

	return Snapshot{Windows: windows, FetchedAt: time.Now().UTC()}, nil
}

// openCodeWindow 解析一个用量桶。百分比是直接的 0–100 值（官方 API 语义，
// 0.5 就是 0.5%）；重置时间优先取相对秒数，其次取绝对时间戳。
func openCodeWindow(obj map[string]any, now time.Time, kind, label string) (Window, bool) {
	var percent float64
	if p, ok := firstNumber(obj, openCodePercentKeys...); ok {
		percent = p
	} else {
		used, usedOK := firstNumber(obj, openCodeUsedKeys...)
		limit, limitOK := firstNumber(obj, openCodeLimitKeys...)
		if !usedOK || !limitOK || limit <= 0 {
			return Window{}, false
		}
		percent = used / limit * 100
	}

	var resetAt *time.Time
	if seconds, ok := firstNumber(obj, openCodeResetInKeys...); ok {
		if seconds < 0 {
			seconds = 0
		}
		resetAt = resetPtr(now.Add(time.Duration(seconds * float64(time.Second))))
	} else if t, ok := firstDate(obj, openCodeResetAtKeys...); ok {
		resetAt = resetPtr(t)
	}

	return percentWindow(kind, label, percent, resetAt), true
}
