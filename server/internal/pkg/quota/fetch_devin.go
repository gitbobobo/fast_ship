package quota

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// Devin 取数：网页版 billing/quota/usage 接口，凭据是浏览器请求里的
// Authorization bearer token + x-cog-org-id 组织 ID。组织 ID 有多种拼写形式
// （org- 内部 ID、org/ 前缀 slug、organizations/ 前缀），按 CodexBar 的候选顺序逐个尝试。

const devinNoOrgDetail = "No organizations found for auth1 user"

func fetchDevin(ctx context.Context, client *http.Client, baseURL string, creds Credentials) (Snapshot, error) {
	token := devinBearerToken(creds["token"])
	if token == "" {
		return Snapshot{}, CredentialError("缺少 Devin Bearer Token。", 0)
	}
	orgID := strings.TrimSpace(creds["org_id"])
	if orgID == "" {
		return Snapshot{}, CredentialError("缺少 Devin Organization ID。", 0)
	}
	normalized := devinNormalizeOrg(orgID)

	base := strings.TrimRight(baseURL, "/")
	var lastErr error
	for _, path := range devinCandidatePaths(normalized) {
		headers := map[string]string{
			"Authorization":   "Bearer " + token,
			"Accept":          "application/json",
			"Accept-Language": "en-US,en;q=0.9",
		}
		if internalID, ok := devinInternalOrgID(normalized); ok {
			headers["x-cog-org-id"] = internalID
		}
		status, body, err := httpGet(ctx, client, base+"/api/"+path, headers)
		if err != nil {
			lastErr = err
			continue
		}
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			if strings.Contains(string(body), devinNoOrgDetail) {
				return Snapshot{}, CredentialError("Devin 组织 ID 无效，请确认 x-cog-org-id 的值。", status)
			}
			return Snapshot{}, CredentialError("Devin 会话令牌无效或已过期，请重新粘贴。", status)
		}
		if err := requireOK("Devin", status); err != nil {
			lastErr = err
			continue
		}
		snapshot, err := parseDevinUsage(body)
		if err != nil {
			return Snapshot{}, err
		}
		return snapshot, nil
	}
	if lastErr == nil {
		lastErr = ProviderError("Devin 接口不可用。", 0)
	}
	return Snapshot{}, lastErr
}

// devinBearerToken 兼容直接粘贴的 token、带 Bearer 前缀、或整行 Authorization 头。
func devinBearerToken(raw string) string {
	token := strings.TrimSpace(raw)
	lower := strings.ToLower(token)
	if strings.HasPrefix(lower, "authorization:") {
		token = strings.TrimSpace(token[len("authorization:"):])
		lower = strings.ToLower(token)
	}
	if strings.HasPrefix(lower, "bearer ") {
		token = strings.TrimSpace(token[len("bearer "):])
	}
	return token
}

// devinNormalizeOrg 把组织 ID 归一成 org/<slug> 或 organizations/<内部 ID> 两种形式。
func devinNormalizeOrg(raw string) string {
	value := strings.Trim(strings.TrimSpace(raw), "/")
	if strings.HasPrefix(value, "org/") || strings.HasPrefix(value, "organizations/") {
		return value
	}
	if devinIsInternalOrgID(value) {
		return "organizations/" + value
	}
	return "org/" + value
}

func devinIsInternalOrgID(value string) bool {
	return strings.HasPrefix(value, "org-") || strings.HasPrefix(value, "org_")
}

// devinInternalOrgID 当归一形式指向内部 ID（organizations/…）时返回该 ID。
func devinInternalOrgID(normalized string) (string, bool) {
	if rest, ok := strings.CutPrefix(normalized, "organizations/"); ok && rest != "" {
		return rest, true
	}
	return "", false
}

// devinCandidatePaths 与 CodexBar 的候选顺序一致：内部 ID 路径优先，逐个尝试直到成功。
func devinCandidatePaths(normalized string) []string {
	var paths []string
	seen := map[string]bool{}
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	if internalID, ok := devinInternalOrgID(normalized); ok {
		add(internalID + "/billing/quota/usage")
	}
	add(normalized + "/billing/quota/usage")
	if rest, ok := strings.CutPrefix(normalized, "org/"); ok {
		add(rest + "/billing/quota/usage")
	}
	if internalID, ok := devinInternalOrgID(normalized); ok {
		add("organizations/" + internalID + "/billing/quota/usage")
	}
	return paths
}

// parseDevinUsage 解析 usage 响应：顶层 daily_percentage/weekly_percentage 是当前格式，
// 其余嵌套结构（quota_usage.daily_quota 等）按键名递归搜索兜底。
func parseDevinUsage(data []byte) (Snapshot, error) {
	root, err := jsonRoot(data)
	if err != nil {
		return Snapshot{}, err
	}

	hideDaily := false
	if v, ok := root["hide_daily_quota"].(bool); ok {
		hideDaily = v
	}

	var daily, weekly *Window
	if !hideDaily {
		daily = devinWindow(root, "daily_percentage", "daily_reset_at")
		if daily == nil {
			daily = devinFindWindow(root, devinIsDailyKey)
		}
	}
	weekly = devinWindow(root, "weekly_percentage", "weekly_reset_at")
	if weekly == nil {
		weekly = devinFindWindow(root, devinIsWeeklyKey)
	}
	if daily == nil && weekly == nil {
		return Snapshot{}, ProviderError("Devin 响应缺少用量窗口，接口可能已变更。", 0)
	}

	windows := []Window{}
	if daily != nil {
		windows = append(windows, *daily)
	}
	if weekly != nil {
		windows = append(windows, *weekly)
	}
	sortWindows(windows)

	planName := devinFindPlanName(root)
	balance := devinOverageBalance(root)

	return Snapshot{PlanName: planName, Windows: windows, Balance: balance, FetchedAt: time.Now().UTC()}, nil
}

// devinWindow 解析顶层 percentage + reset_at 字段；小于等于 1 的百分比按小数处理。
func devinWindow(obj map[string]any, percentKey, resetKey string) *Window {
	percent, ok := asNumber(obj[percentKey])
	if !ok {
		return nil
	}
	if percent <= 1 {
		percent *= 100
	}
	resetAt, _ := flexibleDate(obj[resetKey])
	w := percentWindow("", "", percent, resetPtr(resetAt))
	w.Kind, w.Label = devinKindFor(percentKey)
	return &w
}

func devinKindFor(percentKey string) (kind, label string) {
	if strings.Contains(percentKey, "weekly") {
		return WindowWeekly, "每周"
	}
	return WindowDaily, "每日"
}

func devinIsDailyKey(key string) bool {
	lower := strings.ToLower(key)
	return !strings.Contains(lower, "hide") &&
		(strings.Contains(lower, "daily") || strings.Contains(lower, "day"))
}

func devinIsWeeklyKey(key string) bool {
	lower := strings.ToLower(key)
	return !strings.Contains(lower, "hide") &&
		(strings.Contains(lower, "weekly") || strings.Contains(lower, "week"))
}

// devinFindWindow 递归搜索键名匹配的用量窗口（兼容嵌套的旧格式响应）。
// 窗口的 daily/weekly 归类由命中的键名决定，而不是窗口对象自身的内容。
func devinFindWindow(value any, keyMatches func(string) bool) *Window {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if keyMatches(key) {
				if w := devinWindowFromValue(child, key); w != nil {
					return w
				}
			}
		}
		for _, child := range v {
			if w := devinFindWindow(child, keyMatches); w != nil {
				return w
			}
		}
	case []any:
		for _, child := range v {
			if w := devinFindWindow(child, keyMatches); w != nil {
				return w
			}
		}
	}
	return nil
}

// devinWindowFromValue 把命中的值解析成窗口：可以是直接的百分比数字，也可以是
// 含百分比/remaining/used-limit 字段的对象；对象里任意含 reset 的键都当作重置时间。
func devinWindowFromValue(value any, matchedKey string) *Window {
	kind, label := WindowDaily, "每日"
	if devinIsWeeklyKey(matchedKey) {
		kind, label = WindowWeekly, "每周"
	}

	obj, isObj := asObject(value)
	if !isObj {
		percent, ok := asNumber(value)
		if !ok {
			return nil
		}
		if percent <= 1 {
			percent *= 100
		}
		w := percentWindow(kind, label, percent, nil)
		return &w
	}

	percent, ok := devinPercent(obj)
	if !ok {
		// 值本身不是窗口时，在其内部继续按同类型键搜索一层。
		for _, child := range obj {
			if w := devinWindowFromValue(child, matchedKey); w != nil {
				return w
			}
		}
		return nil
	}
	var resetAt *time.Time
	for key, child := range obj {
		if strings.Contains(strings.ToLower(key), "reset") {
			if t, ok := flexibleDate(child); ok {
				resetAt = &t
				break
			}
		}
	}
	w := percentWindow(kind, label, percent, resetAt)
	return &w
}

// devinPercent 解析百分比：直接百分比键 → remaining 键（取补）→ used/limit → (limit-remaining)/limit。
func devinPercent(obj map[string]any) (float64, bool) {
	scaled := func(v float64) float64 {
		if v <= 1 {
			return v * 100
		}
		return v
	}
	if v, ok := firstNumber(obj, "used_percent", "usedPercent", "usage_percent", "usagePercent",
		"percent_used", "percentUsed", "percent"); ok {
		return scaled(v), true
	}
	if v, ok := firstNumber(obj, "remaining_percent", "remainingPercent", "percent_remaining", "percentRemaining"); ok {
		return 100 - scaled(v), true
	}
	// used 键与 limit/remaining 键严格区分：available 是「剩余」，绝不能当 used，
	// 否则用量会被反向计算（CodexBar 的 percent(from:) 同样只在这里用 remaining/limit 列表）。
	used, usedOK := firstNumber(obj, "used", "usage", "used_count", "usedCount", "consumed")
	limit, limitOK := firstNumber(obj, "limit", "quota", "total", "max", "available")
	if usedOK && limitOK && limit > 0 {
		return used / limit * 100, true
	}
	remaining, remainingOK := firstNumber(obj, "remaining", "left", "available")
	if remainingOK && limitOK && limit > 0 {
		return (limit - remaining) / limit * 100, true
	}
	return 0, false
}

// devinFindPlanName 递归搜索套餐名字段并整理成展示形式（snake/hyphen 分词后首字母大写）。
func devinFindPlanName(value any) string {
	switch v := value.(type) {
	case map[string]any:
		if raw, ok := firstString(v, "plan_name", "planName", "plan", "tier",
			"subscription_tier", "subscriptionTier"); ok {
			return devinCleanPlanName(raw)
		}
		for _, child := range v {
			if name := devinFindPlanName(child); name != "" {
				return name
			}
		}
	case []any:
		for _, child := range v {
			if name := devinFindPlanName(child); name != "" {
				return name
			}
		}
	}
	return ""
}

func devinCleanPlanName(raw string) string {
	parts := strings.FieldsFunc(raw, func(r rune) bool { return r == '_' || r == '-' || r == ' ' })
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, " ")
}

// devinOverageBalance 读取超额余额（美元，或美分字段换算）。
func devinOverageBalance(root map[string]any) *Balance {
	if v, ok := firstNumber(root, "overage_balance"); ok && v >= 0 {
		return &Balance{Label: "超额余额", Amount: Round1(v), Currency: "USD"}
	}
	if cents, ok := firstNumber(root, "overage_balance_cents"); ok && cents >= 0 {
		return &Balance{Label: "超额余额", Amount: Round1(cents / 100), Currency: "USD"}
	}
	return nil
}
