package quota

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// OpenDesign（Design Plan）取数。OpenDesign 有两套密钥：控制面
// amr-api.open-design.ai 只认 vela_ctrl_…（本 fetcher 用的就是它），
// 模型面 amr-link.open-design.ai 用 sk-amr-…；后者查不了额度，在
// validateOpenDesignCredentials 里提前拦下。额度主数据链路：
// workspaces/current → workspaces/{id}/billing/preflight →（尽力而为）billing/summary。

// validateOpenDesignCredentials 拦下模型面密钥：网页「API Key」页给的是
// sk-amr-…，控制面拿它会 401，与其让用户看到含混的凭据失效，不如直接说明原因。
// 返回空串表示通过（Provider.Validate 的钩子语义）。
func validateOpenDesignCredentials(creds Credentials) string {
	if strings.HasPrefix(strings.TrimSpace(creds["control_key"]), "sk-amr-") {
		return "这是调模型用的密钥，查不了额度，请用浏览器授权登录获取控制面密钥（vela_ctrl_…）。"
	}
	return ""
}

func fetchOpenDesign(ctx context.Context, client *http.Client, baseURL string, creds Credentials) (Snapshot, error) {
	key := strings.TrimSpace(creds["control_key"])
	if key == "" {
		return Snapshot{}, CredentialError("缺少 OpenDesign 控制面密钥。", 0)
	}
	base := strings.TrimRight(baseURL, "/")
	headers := map[string]string{
		"Authorization": "Bearer " + key,
		"Accept":        "application/json",
	}

	// workspace 是 preflight 的路径参数；两步都是必需的鉴权判定点。
	status, body, err := httpGet(ctx, client, base+"/api/v1/workspaces/current", headers)
	if err != nil {
		return Snapshot{}, err
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return Snapshot{}, CredentialError(fmt.Sprintf(
			"OpenDesign 控制面拒绝了该密钥，请用浏览器授权重新获取（HTTP %d）。", status), status)
	}
	if err := requireOK("OpenDesign", status); err != nil {
		return Snapshot{}, err
	}
	root, err := jsonRoot(body)
	if err != nil {
		return Snapshot{}, err
	}
	workspaceID := ""
	if id, ok := asString(root["activeWorkspaceId"]); ok {
		workspaceID = id
	} else if id, ok := asString(root["workspaceId"]); ok {
		workspaceID = id
	}
	if workspaceID == "" {
		return Snapshot{}, ProviderError("OpenDesign 账号没有活动 workspace。", 0)
	}

	status, body, err = httpGet(ctx, client,
		base+"/api/v1/workspaces/"+url.PathEscape(workspaceID)+"/billing/preflight", headers)
	if err != nil {
		return Snapshot{}, err
	}
	if err := requireOK("OpenDesign", status); err != nil {
		return Snapshot{}, err
	}
	root, err = jsonRoot(body)
	if err != nil {
		return Snapshot{}, err
	}
	plan, ok := asObject(root["codingPlan"])
	if !ok {
		return Snapshot{}, ProviderError("OpenDesign 响应缺少 Design Plan 数据，接口可能已变更。", 0)
	}

	// eligible=false 说明账号当前没有 Design Plan 额度：不产生窗口，
	// 用 note 说明「按钱包计费」，其余字段（余额、套餐名）照常解析。
	windows := []Window{}
	note := ""
	eligible := true
	if b, ok := asBool(plan["eligible"]); ok {
		eligible = b
	}
	if !eligible {
		note = "无 Design Plan 额度，按钱包计费"
	} else {
		windows = openDesignWindows(plan)
	}

	// 钱包余额可数字或数字字符串；解析失败不算错误，只是没有余额展示。
	var balance *Balance
	if v, ok := asNumber(root["balanceUsd"]); ok {
		balance = &Balance{Label: "钱包余额", Amount: Round1(v), Currency: "USD"}
		// 有额度窗口也提示窗口用尽后会扣钱包，用户对计费方式有预期。
		if eligible {
			note = "窗口用尽后扣钱包"
		}
	}

	// summary 是尽力而为的富化（只补套餐名）：任何失败都忽略，不能因此判失败。
	var summary map[string]any
	if status, body, err := httpGet(ctx, client, base+"/api/v1/billing/summary", headers); err == nil && status >= 200 && status < 300 {
		summary, _ = jsonRoot(body)
	}

	return Snapshot{
		PlanName:  openDesignPlanName(plan, summary),
		Windows:   windows,
		Balance:   balance,
		Note:      note,
		FetchedAt: time.Now().UTC(),
	}, nil
}

// openDesignWindows 遍历 codingPlan.windows[]。usedCredits/limitCredits 是
// credit 数（真实响应为数字字符串），limit 缺失或非正数的窗口跳过（不算错误）。
func openDesignWindows(plan map[string]any) []Window {
	items, ok := plan["windows"].([]any)
	if !ok {
		return []Window{}
	}
	windows := make([]Window, 0, len(items))
	for _, item := range items {
		w, ok := asObject(item)
		if !ok {
			continue
		}
		used, hasUsed := asNumber(w["usedCredits"])
		limit, hasLimit := asNumber(w["limitCredits"])
		if !hasUsed || !hasLimit || limit <= 0 {
			continue
		}
		// used/limit 填的是 credit 数（与 fetch_zai 的 MCP 次数窗口同款），
		// 单位必须是 count；percent 会让前端把 16610 渲染成 16610%。
		window := Window{
			UsedPercent: Percent(used, limit),
			Used:        PercentPtr(used),
			Limit:       PercentPtr(limit),
			Unit:        UnitCount,
		}
		if seconds, ok := asNumber(w["durationSeconds"]); ok && seconds > 0 {
			minutes := int(seconds / 60)
			window.Kind, window.Label = windowForMinutes(minutes)
			if window.Kind == WindowOther {
				window.Label = openDesignWindowLabel(minutes)
			}
		} else {
			// 时长缺失时窗口仍可展示（有百分比），给确定名字而不是空 label。
			window.Kind = WindowOther
			window.Label = "额度窗口"
		}
		if t, ok := flexibleDate(w["resetsAt"]); ok {
			window.ResetAt = resetPtr(t)
		}
		windows = append(windows, window)
	}
	sortWindows(windows)
	return windows
}

// openDesignWindowLabel 由时长推导中文名：整天的按天、整小时的按小时，
// 其余按分钟——WindowOther 的 label 必须有确定名字，不能空。
func openDesignWindowLabel(minutes int) string {
	switch {
	case minutes >= 1440 && minutes%1440 == 0:
		return fmt.Sprintf("%d 天", minutes/1440)
	case minutes >= 60 && minutes%60 == 0:
		return fmt.Sprintf("%d 小时", minutes/60)
	default:
		return fmt.Sprintf("%d 分钟", minutes)
	}
}

// openDesignPlanName 套餐名：summary 的 membershipTier 优先，codingPlan.tier
// 回退；规则统一为「Design Plan 」+ 首字母大写的档位名（go → Design Plan Go）。
// summary 缺失（请求失败或响应不可解析）时只用 codingPlan.tier，两者都缺则为空串。
func openDesignPlanName(plan, summary map[string]any) string {
	tier := ""
	if summary != nil {
		if s, ok := asString(summary["membershipTier"]); ok {
			tier = s
		}
	}
	if tier == "" {
		if s, ok := asString(plan["tier"]); ok {
			tier = s
		}
	}
	runes := []rune(tier)
	if len(runes) == 0 {
		return ""
	}
	return "Design Plan " + strings.ToUpper(string(runes[0])) + string(runes[1:])
}
