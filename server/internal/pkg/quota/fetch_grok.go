package quota

import (
	"context"
	"math"
	"net/http"
	"strings"
	"time"
)

// Grok 取数：走 Grok CLI 的 billing 代理（grok.com 的 gRPC-web 端点需要浏览器持有的
// WKE 密钥，CLI 代理是 bearer token 唯一可用路径）。单一窗口：当前计费/用量周期的
// 额度百分比，外加可选的预付余额。

type grokCreditsConfig struct {
	creditPercent     *float64
	onDemandCap       *float64
	onDemandUsed      *float64
	currentPeriodEnd  *time.Time
	usesCurrentPeriod bool
	periodStartRaw    any
	billingStartRaw   any
	resetAt           *time.Time
	tier              string
	prepaidUSD        *float64
}

func fetchGrok(ctx context.Context, client *http.Client, baseURL string, creds Credentials) (Snapshot, error) {
	token := strings.TrimSpace(creds["token"])
	if token == "" {
		return Snapshot{}, CredentialError("缺少 Grok Bearer Token。", 0)
	}
	headers := map[string]string{
		"Authorization":    "Bearer " + token,
		"x-xai-token-auth": "xai-grok-cli",
		"Accept":           "application/json",
	}

	status, body, err := httpGet(ctx, client, strings.TrimRight(baseURL, "/")+"/v1/billing?format=credits", headers)
	if err != nil {
		return Snapshot{}, err
	}
	if err := requireOK("Grok", status); err != nil {
		return Snapshot{}, err
	}

	config, err := parseGrokCredits(body)
	if err != nil {
		return Snapshot{}, err
	}
	return buildGrokSnapshot(config), nil
}

// parseGrokCredits 解析 /v1/billing?format=credits 响应。config 缺失，或
// 百分比 / 周期 / 余额三者全缺，都视为接口结构变化。
func parseGrokCredits(data []byte) (*grokCreditsConfig, error) {
	root, err := jsonRoot(data)
	if err != nil {
		return nil, err
	}
	configObj, ok := asObject(root["config"])
	if !ok {
		return nil, ProviderError("Grok 响应缺少 config 数据，接口可能已变更。", 0)
	}

	config := &grokCreditsConfig{billingStartRaw: configObj["billingPeriodStart"]}
	if p, ok := firstNumber(configObj, "creditUsagePercent"); ok && !math.IsNaN(p) {
		config.creditPercent = &p
	}
	if capObj, ok := asObject(configObj["onDemandCap"]); ok {
		if v, ok := asNumber(capObj["val"]); ok && v > 0 {
			config.onDemandCap = &v
		}
	}
	if usedObj, ok := asObject(configObj["onDemandUsed"]); ok {
		if v, ok := asNumber(usedObj["val"]); ok {
			config.onDemandUsed = &v
		}
	}

	// 重置时间：currentPeriod.end 优先，缺失时回落到 billingPeriodEnd。
	// 起点必须与选中的终点来自同一个周期，不能混搭。
	if periodObj, ok := asObject(configObj["currentPeriod"]); ok {
		config.periodStartRaw = periodObj["start"]
		if end, ok := flexibleDate(periodObj["end"]); ok {
			config.currentPeriodEnd = &end
			config.resetAt = &end
			config.usesCurrentPeriod = true
		}
	}
	if config.resetAt == nil {
		if end, ok := flexibleDate(configObj["billingPeriodEnd"]); ok {
			config.resetAt = &end
		}
	}
	config.tier = grokTierName(configObj, root)

	if balanceObj, ok := asObject(configObj["prepaidBalance"]); ok {
		config.prepaidUSD = grokPrepaidUSD(balanceObj)
	}

	// 守卫与 CodexBar 一致，判断的是「解析后的百分比」而不是原始 cap 字段：
	// 只有 cap 没有 used（proto3 省略零值）时推不出百分比，不能当有效数据。
	if config.resolvedPercent() == nil && config.resetAt == nil && config.prepaidUSD == nil {
		return nil, ProviderError("Grok 响应缺少用量数据，接口可能已变更。", 0)
	}
	return config, nil
}

// resolvedPercent 按CodexBar 的优先级算最终百分比：直接百分比优先，
// 其次 cap/used 推算；两者都不可得时返回 nil。
func (c *grokCreditsConfig) resolvedPercent() *float64 {
	switch {
	case c.creditPercent != nil:
		p := ClampPercent(*c.creditPercent)
		return &p
	case c.onDemandCap != nil && c.onDemandUsed != nil:
		p := ClampPercent(*c.onDemandUsed / *c.onDemandCap * 100)
		return &p
	default:
		return nil
	}
}

// grokPrepaidUSD 解析预付余额：val 是美分（数字或字符串）；proto3 会省略零值标量，
// 空对象表示 0；负值视为无效数据忽略。
func grokPrepaidUSD(obj map[string]any) *float64 {
	var cents float64
	if v, ok := asNumber(obj["val"]); ok {
		cents = v
	} else if len(obj) == 0 {
		cents = 0
	} else {
		return nil
	}
	if cents < 0 {
		return nil
	}
	usd := cents / 100
	return &usd
}

// grokTierName 取套餐名：config 内的 subscriptionTier 优先于顶层；SuperGrok Heavy / SuperGrok
// 归一成展示名，其余原样返回。
func grokTierName(configObj map[string]any, root map[string]any) string {
	raw, _ := firstString(configObj, "subscriptionTier")
	if raw == "" {
		raw, _ = firstString(root, "subscriptionTier")
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	compact := strings.ToLower(strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			return r
		}
		return -1
	}, trimmed))
	switch compact {
	case "supergrokheavy", "heavy":
		return "SuperGrok Heavy"
	case "supergrok":
		return "SuperGrok"
	default:
		return trimmed
	}
}

func buildGrokSnapshot(config *grokCreditsConfig) Snapshot {
	percent := config.resolvedPercent()

	// 窗口长度从周期起止推算（起点与选中的终点必须同周期）；
	// 起点必须已开场、终点晚于起点才算有效，否则窗口长度未知。
	startRaw := config.billingStartRaw
	if config.usesCurrentPeriod {
		startRaw = config.periodStartRaw
	}
	minutes := 0
	if start, ok := flexibleDate(startRaw); ok && config.resetAt != nil &&
		config.resetAt.After(start) && !start.After(time.Now().UTC()) {
		minutes = int(config.resetAt.Sub(start).Minutes())
	}

	kind, label := windowForMinutes(minutes)
	if label == "" {
		kind, label = WindowOther, "账单周期"
	}

	// 用量未知但周期已知时仍保留窗口（used_percent 为空），前端可以只显示重置倒计时。
	windows := []Window{}
	if percent != nil || config.resetAt != nil {
		window := Window{Kind: kind, Label: label, UsedPercent: percent, Unit: UnitPercent, ResetAt: config.resetAt}
		windows = append(windows, window)
	}

	var balance *Balance
	if config.prepaidUSD != nil {
		balance = &Balance{Label: "预付余额", Amount: Round1(*config.prepaidUSD), Currency: "USD"}
	}

	return Snapshot{
		PlanName:  config.tier,
		Windows:   windows,
		Balance:   balance,
		FetchedAt: time.Now().UTC(),
	}
}
