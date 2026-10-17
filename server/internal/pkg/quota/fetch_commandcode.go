package quota

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// Command Code 取数：API key 走 /alpha 路由（浏览器会话才用的 /internal 路由不在本服务范围内）。
// credits 接口必须成功；subscriptions 只补全套餐名与账期，失败时降级为「无套餐信息」，
// 与 CodexBar 的行为一致（订阅富化不可用时仍返回 credits 数据）。

type commandCodeRateLimit struct {
	percent float64
	resetAt *time.Time
}

type commandCodeCredits struct {
	monthlyRemaining float64
	purchased        float64
	granted          *float64
	fiveHour         *commandCodeRateLimit
	weekly           *commandCodeRateLimit
}

type commandCodeSubscription struct {
	planID      string
	periodEnd   *time.Time
	isFreeTier  bool
	unavailable bool
}

// commandCodePlans 是 Command Code 公开定价页上的套餐额度表（单位：USD/月），
// 用于在 credits 响应缺少 monthlyCreditsGranted 时推算月度窗口的分母。
var commandCodePlans = map[string]float64{
	"individual-go":     10,
	"individual-go-v1":  10,
	"individual-goat":   70,
	"individual-pro":    30,
	"individual-pro-v1": 80,
	"individual-max":    150,
	"individual-ultra":  300,
}

// commandCodeSubscriptionGrace 是订阅富化请求的独立子超时（对齐 CodexBar 的
// subscriptionGraceSeconds）：subscriptions 挂起最多多等这么久就放弃富化，
// 不把整次抓取拖长；失败仍只降级，不影响 credits 主数据。
// 包级 var 仅供本包测试缩短（本包测试不并行，生产路径只读）。
var commandCodeSubscriptionGrace = 2 * time.Second

func fetchCommandCode(ctx context.Context, client *http.Client, baseURL string, creds Credentials) (Snapshot, error) {
	key := strings.TrimSpace(creds["api_key"])
	if key == "" {
		return Snapshot{}, CredentialError("缺少 Command Code API Key。", 0)
	}
	base := strings.TrimRight(baseURL, "/")
	headers := map[string]string{
		"Authorization": "Bearer " + key,
		"Accept":        "application/json",
	}

	status, body, err := httpGet(ctx, client, base+"/alpha/billing/credits", headers)
	if err != nil {
		return Snapshot{}, err
	}
	if err := requireOK("Command Code", status); err != nil {
		return Snapshot{}, err
	}
	credits, err := parseCommandCodeCredits(body)
	if err != nil {
		return Snapshot{}, err
	}

	// 订阅查询的任何失败（网络、5xx、超时、结构变化）都不影响 credits 主数据。
	var sub *commandCodeSubscription
	subCtx, cancelSub := context.WithTimeout(ctx, commandCodeSubscriptionGrace)
	subStatus, subBody, subErr := httpGet(subCtx, client, base+"/alpha/billing/subscriptions", headers)
	cancelSub()
	if subErr == nil && subStatus >= 200 && subStatus < 300 {
		sub, subErr = parseCommandCodeSubscription(subBody)
	}
	if subErr != nil || sub == nil {
		sub = &commandCodeSubscription{unavailable: true}
	}

	return buildCommandCodeSnapshot(credits, sub), nil
}

// parseCommandCodeCredits 解析 /alpha/billing/credits 响应。
func parseCommandCodeCredits(data []byte) (*commandCodeCredits, error) {
	root, err := jsonRoot(data)
	if err != nil {
		return nil, err
	}
	creditsObj, ok := asObject(root["credits"])
	if !ok {
		return nil, ProviderError("Command Code 响应缺少 credits 数据，接口可能已变更。", 0)
	}
	monthly, ok := firstNumber(creditsObj, "monthlyCredits")
	if !ok {
		return nil, ProviderError("Command Code 响应缺少月度额度，接口可能已变更。", 0)
	}
	purchased, _ := firstNumber(creditsObj, "purchasedCredits")

	// windowLimits 可能挂在响应根部，也可能嵌在 credits 里（旧格式）。
	windowLimits, _ := asObject(root["windowLimits"])
	if windowLimits == nil {
		windowLimits, _ = asObject(creditsObj["windowLimits"])
	}

	return &commandCodeCredits{
		monthlyRemaining: monthly,
		purchased:        purchased,
		granted:          grantedNumber(creditsObj, "monthlyCreditsGranted"),
		fiveHour:         commandCodeWindow(windowLimits, "fiveHour"),
		weekly:           commandCodeWindow(windowLimits, "weekly"),
	}, nil
}

func grantedNumber(obj map[string]any, key string) *float64 {
	if n, ok := asNumber(obj[key]); ok {
		return &n
	}
	return nil
}

// commandCodeWindow 解析一个滚动窗口（cap > 0 才有效，used 缺省为 0）。
func commandCodeWindow(windowLimits map[string]any, key string) *commandCodeRateLimit {
	if windowLimits == nil {
		return nil
	}
	obj, ok := asObject(windowLimits[key])
	if !ok {
		return nil
	}
	capValue, ok := firstNumber(obj, "cap")
	if !ok || capValue <= 0 {
		return nil
	}
	used, _ := firstNumber(obj, "used")
	resetAt, _ := flexibleDate(obj["resetAt"])
	return &commandCodeRateLimit{percent: used / capValue * 100, resetAt: resetPtr(resetAt)}
}

// parseCommandCodeSubscription 解析 /alpha/billing/subscriptions 响应。
// 只有显式的 success=true + data:null 才代表免费档；其余失败一律按「富化不可用」处理。
func parseCommandCodeSubscription(data []byte) (*commandCodeSubscription, error) {
	root, err := jsonRoot(data)
	if err != nil {
		return nil, err
	}
	success, ok := asBool(root["success"])
	if !ok {
		return nil, ProviderError("Command Code 订阅响应缺少 success 标记。", 0)
	}
	if !success {
		return nil, ProviderError("Command Code 订阅响应不成功。", 0)
	}
	dataValue, present := root["data"]
	if !present {
		return nil, ProviderError("Command Code 订阅响应缺少 data。", 0)
	}
	if dataValue == nil {
		return &commandCodeSubscription{isFreeTier: true}, nil
	}
	dataObj, ok := asObject(dataValue)
	if !ok {
		return nil, ProviderError("Command Code 订阅响应格式异常。", 0)
	}
	planID, ok := asString(dataObj["planId"])
	if !ok {
		return nil, ProviderError("Command Code 订阅响应缺少 planId。", 0)
	}
	periodEnd, _ := flexibleDate(dataObj["currentPeriodEnd"])
	return &commandCodeSubscription{planID: planID, periodEnd: resetPtr(periodEnd)}, nil
}

// commandCodePlanName 把 planId 映射成展示名与月度额度；未知套餐直接展示 planId 本身。
func commandCodePlanName(planID string) (string, float64, bool) {
	normalized := strings.ToLower(planID)
	if grant, ok := commandCodePlans[normalized]; ok {
		switch normalized {
		case "individual-goat":
			return "GOAT", grant, true
		case "individual-go", "individual-go-v1":
			return "Go", grant, true
		case "individual-pro", "individual-pro-v1":
			return "Pro", grant, true
		case "individual-max":
			return "Max", grant, true
		case "individual-ultra":
			return "Ultra", grant, true
		}
	}
	return planID, 0, false
}

func buildCommandCodeSnapshot(credits *commandCodeCredits, sub *commandCodeSubscription) Snapshot {
	planName := ""
	var grantTotal *float64
	if !sub.unavailable && !sub.isFreeTier && sub.planID != "" {
		display, grant, known := commandCodePlanName(sub.planID)
		planName = display
		if known {
			g := grant
			grantTotal = &g
		}
	}

	windows := []Window{}
	if credits.fiveHour != nil {
		windows = append(windows, percentWindow(WindowFiveHour, "5 小时", credits.fiveHour.percent, credits.fiveHour.resetAt))
	}
	if credits.weekly != nil {
		windows = append(windows, percentWindow(WindowWeekly, "每周", credits.weekly.percent, credits.weekly.resetAt))
	}

	// 月度窗口：优先用 credits 自带的 grant，其次用套餐表推算；两者都没有时，
	// 只有「订阅查询成功且是免费档但仍有可用余额」才给一个 0% 的占位窗口。
	if total := commandCodeGrantTotal(credits, grantTotal); total != nil {
		used := *total - credits.monthlyRemaining
		if used < 0 {
			used = 0
		}
		if used > *total {
			used = *total
		}
		windows = append(windows, Window{
			Kind:        WindowMonthly,
			Label:       "每月",
			UsedPercent: Percent(used, *total),
			Used:        PercentPtr(Round1(used)),
			Limit:       PercentPtr(Round1(*total)),
			Unit:        UnitUSD,
			ResetAt:     sub.periodEnd,
		})
	} else if sub.isFreeTier && (credits.monthlyRemaining > 0 || credits.purchased > 0) {
		zero := 0.0
		windows = append(windows, Window{
			Kind: WindowMonthly, Label: "每月", UsedPercent: &zero, Unit: UnitUSD, ResetAt: sub.periodEnd,
		})
	}
	sortWindows(windows)

	var balance *Balance
	if credits.purchased > 0 {
		balance = &Balance{Label: "充值余额", Amount: Round1(credits.purchased), Currency: "USD"}
	}

	return Snapshot{
		PlanName:  planName,
		Windows:   windows,
		Balance:   balance,
		FetchedAt: time.Now().UTC(),
	}
}

func commandCodeGrantTotal(credits *commandCodeCredits, grantTotal *float64) *float64 {
	if credits.granted != nil && *credits.granted > 0 {
		return credits.granted
	}
	return grantTotal
}
