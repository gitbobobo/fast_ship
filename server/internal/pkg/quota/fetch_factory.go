package quota

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Factory Droid 取数：先 /api/app/auth/me 拿组织与套餐信息（也用于鉴权校验），
// 优先尝试新的 token-rate-limits 计费接口（/api/billing/limits）；
// 该接口不可用（404 / 未灰度 / 结构不符）时回退到旧版
// /api/organization/subscription/usage 拿标准/高级额度。

const factoryClientHeader = "web-app"

func fetchFactory(ctx context.Context, client *http.Client, baseURL string, creds Credentials) (Snapshot, error) {
	key := strings.TrimSpace(creds["api_key"])
	if key == "" {
		return Snapshot{}, CredentialError("缺少 Factory API Key。", 0)
	}
	base := strings.TrimRight(baseURL, "/")
	headers := factoryHeaders("Bearer " + key)

	// auth/me 是必需的：提供套餐名，同时是鉴权失败的判定点。
	status, body, err := httpGet(ctx, client, base+"/api/app/auth/me", headers)
	if err != nil {
		return Snapshot{}, err
	}
	if err := requireOK("Factory", status); err != nil {
		return Snapshot{}, err
	}
	authInfo, err := jsonRoot(body)
	if err != nil {
		return Snapshot{}, ProviderError("Factory 账号信息无法解析，接口可能已变更。", 0)
	}

	if snapshot, ok := fetchFactoryTokenRateLimits(ctx, client, base, headers, authInfo); ok {
		return snapshot, nil
	}
	return fetchFactoryLegacyUsage(ctx, client, base, headers, authInfo, key)
}

// factoryHeaders 是 Factory 请求的公共头。x-factory-client 标识网页端客户端，
// 缺失时接口会拒绝。
func factoryHeaders(bearer string) map[string]string {
	return map[string]string{
		"Authorization":    bearer,
		"x-factory-client": factoryClientHeader,
		"Accept":           "application/json",
		"Content-Type":     "application/json",
		"Origin":           "https://app.factory.ai",
		"Referer":          "https://app.factory.ai/",
	}
}

// fetchFactoryTokenRateLimits 尝试新的计费接口。ok=false 表示接口不可用（任何失败都
// 不致命，与 CodexBar 一致回退旧流程——真正的鉴权失败已在 auth/me 处暴露）。
func fetchFactoryTokenRateLimits(ctx context.Context, client *http.Client, base string, headers map[string]string, authInfo map[string]any) (Snapshot, bool) {
	status, body, err := httpGet(ctx, client, base+"/api/billing/limits", headers)
	if err != nil || status != http.StatusOK {
		return Snapshot{}, false
	}
	root, err := jsonRoot(body)
	if err != nil {
		return Snapshot{}, false
	}
	if enabled, ok := asBool(root["usesTokenRateLimitsBilling"]); !ok || !enabled {
		return Snapshot{}, false
	}
	limits, ok := asObject(root["limits"])
	if !ok {
		return Snapshot{}, false
	}
	standard, ok := asObject(limits["standard"])
	if !ok {
		return Snapshot{}, false
	}

	snapshot, err := buildFactoryRateLimitSnapshot(root, limits, standard, authInfo)
	if err != nil {
		return Snapshot{}, false
	}
	return snapshot, true
}

func buildFactoryRateLimitSnapshot(root, limits, standard, authInfo map[string]any) (Snapshot, error) {
	now := time.Now().UTC()
	var windows []Window

	if w := factoryPoolWindow(standard["fiveHour"], WindowFiveHour, "5 小时", now); w != nil {
		windows = append(windows, *w)
	}
	if w := factoryPoolWindow(standard["weekly"], WindowWeekly, "每周", now); w != nil {
		windows = append(windows, *w)
	}
	if w := factoryPoolWindow(standard["monthly"], WindowMonthly, "每月", now); w != nil {
		windows = append(windows, *w)
	}
	if len(windows) == 0 {
		return Snapshot{}, ProviderError("Factory 响应缺少用量窗口。", 0)
	}

	// core 池是叠加在标准池上的独立额度，只在有数据时展示。
	if core, ok := asObject(limits["core"]); ok && factoryPoolHasData(core) {
		coreWindows := []Window{}
		if w := factoryPoolWindow(core["fiveHour"], WindowFiveHour, "Core 5 小时", now); w != nil {
			coreWindows = append(coreWindows, *w)
		}
		if w := factoryPoolWindow(core["weekly"], WindowWeekly, "Core 每周", now); w != nil {
			coreWindows = append(coreWindows, *w)
		}
		if w := factoryPoolWindow(core["monthly"], WindowMonthly, "Core 每月", now); w != nil {
			coreWindows = append(coreWindows, *w)
		}
		windows = append(windows, coreWindows...)
	}
	sortWindows(windows)

	return Snapshot{
		PlanName:  factoryPlanName(authInfo),
		Windows:   windows,
		Balance:   factoryBalance(root),
		FetchedAt: now,
	}, nil
}

// factoryPoolWindow 解析一个窗口池条目。usedPercent 为必需字段；
// 重置时间取 secondsRemaining（相对秒），否则取 windowEnd（仅在未来时有效）。
// 滚动窗口已过期（有 windowEnd 但没有任何剩余秒数）时按已重置处理，显示 0%。
func factoryPoolWindow(value any, kind, label string, now time.Time) *Window {
	obj, ok := asObject(value)
	if !ok {
		return nil
	}
	percent, ok := asNumber(obj["usedPercent"])
	if !ok {
		return nil
	}
	var windowEnd *time.Time
	if end, ok := flexibleDate(obj["windowEnd"]); ok {
		windowEnd = &end
	}
	secondsRemaining, hasSeconds := asNumber(obj["secondsRemaining"])

	var resetAt *time.Time
	if hasSeconds && secondsRemaining > 0 {
		t := now.Add(time.Duration(secondsRemaining * float64(time.Second)))
		resetAt = &t
	} else if windowEnd != nil && windowEnd.After(now) {
		resetAt = windowEnd
	}

	usedPercent := ClampPercent(percent)
	if resetAt == nil && windowEnd != nil && !hasSeconds {
		usedPercent = 0
	}
	w := percentWindow(kind, label, usedPercent, resetAt)
	return &w
}

// factoryPoolHasData 判断 core 池是否报出了数据：任一窗口报出相关字段即算有数据，
// 0 值也算。CodexBar 的 usedPercent 是非可选字段（缺省即 0），只能用「> 0」近似
// 「确实报过值」；这里从原始 JSON 取值，能分辨「键存在且为 0」与「键不存在」，
// 所以与 secondsRemaining 一样按存在判断——不会把已上报的 0% 核心池整池丢掉。
func factoryPoolHasData(pool map[string]any) bool {
	for _, key := range []string{"fiveHour", "weekly", "monthly"} {
		obj, ok := asObject(pool[key])
		if !ok {
			continue
		}
		if _, ok := asNumber(obj["usedPercent"]); ok {
			return true
		}
		if _, ok := flexibleDate(obj["windowEnd"]); ok {
			return true
		}
		if _, ok := asNumber(obj["secondsRemaining"]); ok {
			return true
		}
	}
	return false
}

// fetchFactoryLegacyUsage 旧版流程：usage 接口提供标准/高级额度，userId 来自
// auth/me 或 API key 的 JWT sub 声明。userId 可能含 | : / + 等字符，必须转义。
func fetchFactoryLegacyUsage(ctx context.Context, client *http.Client, base string, headers map[string]string, authInfo map[string]any, key string) (Snapshot, error) {
	usageURL := base + "/api/organization/subscription/usage?useCache=true"
	if userID := factoryUserID(authInfo, key); userID != "" {
		usageURL += "&userId=" + url.QueryEscape(userID)
	}
	status, body, err := httpGet(ctx, client, usageURL, headers)
	if err != nil {
		return Snapshot{}, err
	}
	if err := requireOK("Factory", status); err != nil {
		return Snapshot{}, err
	}
	usageRoot, err := jsonRoot(body)
	if err != nil {
		return Snapshot{}, ProviderError("Factory 用量数据无法解析，接口可能已变更。", 0)
	}
	return buildFactoryLegacySnapshot(authInfo, usageRoot), nil
}

func buildFactoryLegacySnapshot(authInfo, usageRoot map[string]any) Snapshot {
	usage, _ := asObject(usageRoot["usage"])

	var periodEnd *time.Time
	windows := []Window{}
	if usage != nil {
		if end, ok := flexibleDate(usage["endDate"]); ok {
			periodEnd = &end
		}
		if w := factoryLegacyWindow(usage["standard"], "标准额度", periodEnd); w != nil {
			windows = append(windows, *w)
		}
		if w := factoryLegacyWindow(usage["premium"], "高级额度", periodEnd); w != nil {
			windows = append(windows, *w)
		}
	}

	return Snapshot{
		PlanName:  factoryPlanName(authInfo),
		Windows:   windows,
		Balance:   factoryBalance(usageRoot),
		FetchedAt: time.Now().UTC(),
	}
}

// factoryLegacyWindow 解析旧版 token 用量池。usedRatio（0–1，异常时可能是 0–100）优先；
// allowance 超过 1 万亿视为不限量，按 1 亿 token 参照值折算百分比。
func factoryLegacyWindow(value any, label string, periodEnd *time.Time) *Window {
	obj, ok := asObject(value)
	if !ok {
		return nil
	}
	used, _ := asNumber(obj["userTokens"])
	allowance, _ := asNumber(obj["totalAllowance"])
	ratio, hasRatio := asNumber(obj["usedRatio"])

	// ratio==0 但 used>0 说明比值是占位哨兵，忽略后按 used/allowance 计算。
	ratioIsSentinel := hasRatio && ratio == 0 && used > 0 && allowance > 0 && allowance <= 1e12
	percent := math.NaN()
	if hasRatio && !ratioIsSentinel {
		if ratio >= -0.001 && ratio <= 1.001 {
			percent = clampRange(ratio*100, 0, 100)
		} else if factoryRatioUsable(ratio, allowance) {
			percent = clampRange(ratio, 0, 100)
		}
	}
	if math.IsNaN(percent) {
		switch {
		case allowance > 1e12:
			percent = clampRange(used/1e8*100, 0, 100)
		case allowance > 0:
			percent = clampRange(used/allowance*100, 0, 100)
		default:
			percent = 0
		}
	}

	w := percentWindow(WindowOther, label, percent, periodEnd)
	return &w
}

// factoryRatioUsable 判断 usedRatio 是否可信：allowance 缺失或异常大时，
// 0–100 区间的比值按百分比解释；allowance 正常时只有 0–1 区间可信。
func factoryRatioUsable(ratio, allowance float64) bool {
	if ratio >= -0.001 && ratio <= 1.001 {
		return true
	}
	allowanceReliable := allowance > 0 && allowance <= 1e12
	return !allowanceReliable && ratio >= -0.1 && ratio <= 100.1
}

// factoryUserID 优先取 auth/me 里的用户 ID，否则从 API key 的 JWT sub 声明里解析。
func factoryUserID(authInfo map[string]any, key string) string {
	if profile, ok := asObject(authInfo["userProfile"]); ok {
		if id, ok := asString(profile["id"]); ok {
			return id
		}
	}
	return jwtSubject(key)
}

// jwtSubject 从 JWT 的 payload 里取 sub 声明（仅用于补 userId 查询参数）。
func jwtSubject(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	return claims.Sub
}

// factoryPlanName 套餐名：orbSubscription.plan.name 优先，其次 factoryTier。
func factoryPlanName(authInfo map[string]any) string {
	if org, ok := asObject(authInfo["organization"]); ok {
		if sub, ok := asObject(org["subscription"]); ok {
			if orb, ok := asObject(sub["orbSubscription"]); ok {
				if name, ok := asString(orb["plan"]); ok {
					return name
				}
				if plan, ok := asObject(orb["plan"]); ok {
					if name, ok := asString(plan["name"]); ok {
						return name
					}
				}
			}
			if tier, ok := asString(sub["factoryTier"]); ok {
				return tier
			}
		}
	}
	return ""
}

// factoryBalance 读取 extraUsageBalanceCents（美分）形式的余额，仅在有字段且非负时给出。
func factoryBalance(root map[string]any) *Balance {
	value, present := root["extraUsageBalanceCents"]
	if !present {
		return nil
	}
	cents, ok := asNumber(value)
	if !ok || cents < 0 {
		return nil
	}
	return &Balance{Label: "Extra 余额", Amount: Round1(cents / 100), Currency: "USD"}
}
