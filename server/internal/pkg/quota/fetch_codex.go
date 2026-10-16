package quota

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Codex 取数：ChatGPT backend-api 的 wham/usage 接口。主窗口是 5 小时，
// 次窗口是每周；individual_limit（根级 → rate_limit → spend_control 依次取）是
// 月度额度池，按美元计。access_token 过期后本服务不做刷新，提示重新粘贴。

func fetchCodex(ctx context.Context, client *http.Client, baseURL string, creds Credentials) (Snapshot, error) {
	token := strings.TrimSpace(creds["access_token"])
	if token == "" {
		return Snapshot{}, CredentialError("缺少 Codex Access Token。", 0)
	}
	accountID := strings.TrimSpace(creds["account_id"])
	headers := map[string]string{
		"Authorization": "Bearer " + token,
		"Accept":        "application/json",
	}
	if accountID != "" {
		headers["ChatGPT-Account-Id"] = accountID
	}

	status, body, err := httpGet(ctx, client, strings.TrimRight(baseURL, "/")+"/backend-api/wham/usage", headers)
	if err != nil {
		return Snapshot{}, err
	}
	if err := requireOK("Codex", status); err != nil {
		return Snapshot{}, err
	}

	root, err := jsonRoot(body)
	if err != nil {
		return Snapshot{}, err
	}
	return buildCodexSnapshot(root)
}

func buildCodexSnapshot(root map[string]any) (Snapshot, error) {
	planName, _ := asString(root["plan_type"])

	windows := []Window{}

	// 主/次窗口：按窗口长度归位（300 分钟 = 5 小时窗口，10080 分钟 = 每周窗口），
	// 与 CodexBar 的 CodexRateWindowNormalizer 行为一致。
	var primary, secondary *Window
	if rateLimit, ok := asObject(root["rate_limit"]); ok {
		if obj, ok := asObject(rateLimit["primary_window"]); ok {
			if w, ok := codexWindow(obj); ok {
				primary = &w
			}
		}
		if obj, ok := asObject(rateLimit["secondary_window"]); ok {
			if w, ok := codexWindow(obj); ok {
				secondary = &w
			}
		}
	}
	primary, secondary = codexNormalizeWindows(primary, secondary)
	if primary != nil {
		windows = append(windows, *primary)
	}
	if secondary != nil {
		windows = append(windows, *secondary)
	}

	// 月度额度池：used/limit 以美元计，缺失 used 时用 remaining_percent 反推。
	if limitObj := codexIndividualLimit(root); limitObj != nil {
		if w, ok := codexMonthlyWindow(limitObj); ok {
			windows = append(windows, w)
		}
	}
	sortWindows(windows)

	var balance *Balance
	if credits, ok := asObject(root["credits"]); ok {
		if v, ok := codexBalance(credits); ok {
			balance = &Balance{Label: "剩余额度", Amount: Round1(v), Currency: "USD"}
		}
	}

	if len(windows) == 0 && balance == nil {
		return Snapshot{}, ProviderError("Codex 响应缺少用量数据，接口可能已变更。", 0)
	}

	return Snapshot{PlanName: planName, Windows: windows, Balance: balance, FetchedAt: time.Now().UTC()}, nil
}

// codexWindow 解析 primary/secondary 窗口（used_percent、reset_at 秒级时间戳、limit_window_seconds）。
func codexWindow(obj map[string]any) (Window, bool) {
	percent, ok := asNumber(obj["used_percent"])
	if !ok {
		return Window{}, false
	}
	resetAt, _ := flexibleDate(obj["reset_at"])
	minutes := 0
	if seconds, ok := asNumber(obj["limit_window_seconds"]); ok && seconds > 0 {
		minutes = int(seconds / 60)
	}
	kind, label := windowForMinutes(minutes)
	if label == "" {
		kind, label = WindowOther, "用量窗口"
	}
	return percentWindow(kind, label, percent, resetPtr(resetAt)), true
}

// codexNormalizeWindows 保证第一个窗口是 5 小时窗口、第二个是每周窗口。
func codexNormalizeWindows(primary, secondary *Window) (*Window, *Window) {
	switch {
	case primary != nil && secondary != nil:
		pr, sr := codexRoleOf(primary), codexRoleOf(secondary)
		switch {
		case pr == codexRoleWeekly && (sr == codexRoleSession || sr == codexRoleUnknown):
			return secondary, primary
		default:
			return primary, secondary
		}
	case primary != nil:
		if codexRoleOf(primary) == codexRoleWeekly {
			return nil, primary
		}
		return primary, nil
	case secondary != nil:
		if codexRoleOf(secondary) == codexRoleWeekly {
			return nil, secondary
		}
		return secondary, nil
	default:
		return nil, nil
	}
}

type codexRole int

const (
	codexRoleSession codexRole = iota
	codexRoleWeekly
	codexRoleUnknown
)

func codexRoleOf(w *Window) codexRole {
	switch w.Kind {
	case WindowFiveHour:
		return codexRoleSession
	case WindowWeekly:
		return codexRoleWeekly
	default:
		return codexRoleUnknown
	}
}

// codexIndividualLimit 额度池的来源优先级：响应根级 → rate_limit → spend_control。
func codexIndividualLimit(root map[string]any) map[string]any {
	if obj, ok := asObject(root["individual_limit"]); ok {
		return obj
	}
	if obj, ok := asObject(root["individualLimit"]); ok {
		return obj
	}
	if rateLimit, ok := asObject(root["rate_limit"]); ok {
		if obj, ok := asObject(rateLimit["individual_limit"]); ok {
			return obj
		}
		if obj, ok := asObject(rateLimit["individualLimit"]); ok {
			return obj
		}
	}
	for _, key := range []string{"spend_control", "spendControl"} {
		if spend, ok := asObject(root[key]); ok {
			if obj, ok := asObject(spend["individual_limit"]); ok {
				return obj
			}
			if obj, ok := asObject(spend["individualLimit"]); ok {
				return obj
			}
		}
	}
	return nil
}

func codexMonthlyWindow(obj map[string]any) (Window, bool) {
	limit, ok := asNumber(obj["limit"])
	if !ok || limit <= 0 || math.IsInf(limit, 0) {
		return Window{}, false
	}
	used := 0.0
	if u, ok := asNumber(obj["used"]); ok {
		used = u
	} else if remaining, ok := asNumber(obj["remaining_percent"]); ok {
		used = limit * clampRange(100-remaining, 0, 100) / 100
	} else if remaining, ok := asNumber(obj["remainingPercent"]); ok {
		used = limit * clampRange(100-remaining, 0, 100) / 100
	}
	// 重置键序与 SpendControlLimitSnapshot 的字段一致：camelCase 首选；
	// 回退语义是逐键尝试直到有一个可解析，而不是取第一个存在的键。
	resetAt, _ := firstDate(obj, "resetsAt", "resets_at", "reset_at")
	window := Window{
		Kind:        WindowMonthly,
		Label:       "月度额度",
		UsedPercent: Percent(used, limit),
		Used:        PercentPtr(Round1(used)),
		Limit:       PercentPtr(Round1(limit)),
		Unit:        UnitUSD,
		ResetAt:     resetPtr(resetAt),
	}
	return window, true
}

// codexBalance 读取 credits.balance（数字或数字字符串）。
func codexBalance(credits map[string]any) (float64, bool) {
	switch v := credits["balance"].(type) {
	case float64:
		if math.IsInf(v, 0) {
			return 0, false
		}
		return v, true
	case string:
		n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil || math.IsInf(n, 0) {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}

// clampRange 把数值夹到 [min, max]，不做舍入。
func clampRange(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
