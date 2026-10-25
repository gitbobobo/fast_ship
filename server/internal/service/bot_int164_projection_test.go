package service

import (
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
)

// INT-164 投影口径：请求级 reasoning_tokens / provider_cost 透出运行明细；
// provider_cost 列只落置信 USD 的值，币别不明的金额留在 provider_cost_raw
// 不上 API。
// 引用了 openapi 生成类型的新字段，须在 make api-types 之后编译。

func seedInt164Request(t *testing.T, env *botTestEnv, runID string, reasoning *int64, cost *float64, raw *string) {
	t.Helper()
	req := seedUsageRequest(t, env, runID, nil, "int164-"+uuid.NewString())
	updates := map[string]interface{}{
		"prompt_tokens": 10, // bot_model_requests 无 usage_complete 列，落 token 即完整
	}
	if reasoning != nil {
		updates["reasoning_tokens"] = *reasoning
	}
	if cost != nil {
		updates["provider_cost"] = *cost
	}
	if raw != nil {
		updates["provider_cost_raw"] = *raw
	}
	if err := env.db.Model(&model.BotModelRequest{}).Where("id = ?", req.ID).
		Updates(updates).Error; err != nil {
		t.Fatalf("seed int164 request: %v", err)
	}
}

func TestBotUsageRunDetailUsageFields(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int164detail")
	bot := env.createBot(t, userID)
	run := seedUsageRun(t, env, bot.ID, nil)

	rawRecord := `{"field":"cost","value":"0.25"}`
	seedInt164Request(t, env, run.ID, ptrInt64(33), ptrCost(0.75), nil)
	seedInt164Request(t, env, run.ID, nil, nil, &rawRecord) // 币别不明：只记录
	seedInt164Request(t, env, run.ID, nil, nil, nil)        // 全缺

	detail, err := env.bots.GetBotUsageRunDetail(userID, run.ID)
	if err != nil {
		t.Fatalf("run detail: %v", err)
	}
	if len(detail.Requests) != 3 {
		t.Fatalf("应有 3 条请求: %d", len(detail.Requests))
	}
	first := detail.Requests[0]
	if first.ReasoningTokens == nil || *first.ReasoningTokens != 33 {
		t.Fatalf("首条 reasoning_tokens 应为 33: %+v", first)
	}
	if first.ProviderCost == nil || *first.ProviderCost != 0.75 {
		t.Fatalf("USD 置信的 provider_cost 应透出 0.75: %+v", first.ProviderCost)
	}
	second := detail.Requests[1]
	if second.ProviderCost != nil {
		t.Fatalf("provider_cost_raw 的记录不得上 API: %+v", second.ProviderCost)
	}
	third := detail.Requests[2]
	if third.ReasoningTokens != nil || third.ProviderCost != nil {
		t.Fatalf("字段缺失应保持 null: %+v", third)
	}

	// 维护请求同口径。
	maint := seedUsageMaintenance(t, env, bot.ID, &run.ID, nil)
	var mrow model.BotMaintenanceRequest
	if err := env.db.Where("maintenance_run_id = ?", maint.ID).First(&mrow).Error; err != nil {
		t.Fatalf("读维护请求: %v", err)
	}
	if err := env.db.Model(&model.BotMaintenanceRequest{}).Where("id = ?", mrow.ID).
		Updates(map[string]interface{}{
			"usage_complete":   true,
			"reasoning_tokens": 9,
			"provider_cost":    0.5,
		}).Error; err != nil {
		t.Fatalf("seed int164 maintenance: %v", err)
	}

	detail, err = env.bots.GetBotUsageRunDetail(userID, run.ID)
	if err != nil {
		t.Fatalf("run detail 2: %v", err)
	}
	found := false
	for _, mr := range detail.MaintenanceRuns {
		for _, r := range mr.Requests {
			if r.Id != mrow.ID {
				continue
			}
			found = true
			if r.ReasoningTokens == nil || *r.ReasoningTokens != 9 ||
				r.ProviderCost == nil || *r.ProviderCost != 0.5 {
				t.Fatalf("维护请求应透出 reasoning/provider_cost: %+v", r)
			}
		}
	}
	if !found {
		t.Fatalf("未找到 seed 的维护请求")
	}
}

// INT-164 展示金额合并口径：所有展示位（总览/明细/按天汇总/任务合计/预算）
// 统一按请求级 COALESCE(provider_cost, cost_estimate) 实收优先；估算列保持
// 目录价语义不写回。覆盖三态：仅实收、估算+实收（实收赢）、全空（缺失 ≠ 0）
// 以及存量纯估算行与改动前等值。
func TestBotUsageProviderCostDisplayMerge(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int164merge")
	setUserTimezone(t, env, userID, "UTC")
	bot := env.createBot(t, userID)
	fixed := time.Now().UTC().Truncate(time.Hour)
	env.bots.nowFn = func() time.Time { return fixed }

	setProviderCost := func(reqID string, pc float64) {
		t.Helper()
		if err := env.db.Model(&model.BotModelRequest{}).Where("id = ?", reqID).
			Update("provider_cost", pc).Error; err != nil {
			t.Fatalf("set provider_cost: %v", err)
		}
	}
	yesterday := usageDayAt(fixed, 1)
	run := seedUsageRun(t, env, bot.ID, func(r *model.BotRun) {
		r.CreatedAt = yesterday
		r.CostEstimate = ptrCost(0.7) // 快照 = 0.6+0.1 目录价合计
	})
	// 估算+实收（实收赢）/ 仅实收 / 仅估算 / 全空
	r1 := seedUsageRequest(t, env, run.ID, ptrCost(0.6), "pm-1")
	setProviderCost(r1.ID, 0.5)
	r2 := seedUsageRequest(t, env, run.ID, nil, "pm-2")
	setProviderCost(r2.ID, 0.2)
	seedUsageRequest(t, env, run.ID, ptrCost(0.1), "pm-3")
	seedUsageRequest(t, env, run.ID, nil, "pm-4")
	// 关联维护：仅实收 0.4（记自己的日，同样昨天）。
	maint := seedUsageMaintenance(t, env, bot.ID, &run.ID, nil)
	if err := env.db.Model(&model.BotMaintenanceRun{}).Where("id = ?", maint.ID).
		Update("created_at", yesterday.Add(2*time.Hour)).Error; err != nil {
		t.Fatalf("move maintenance: %v", err)
	}
	if err := env.db.Model(&model.BotMaintenanceRequest{}).Where("maintenance_run_id = ?", maint.ID).
		Updates(map[string]interface{}{"provider_cost": 0.4, "usage_complete": true}).Error; err != nil {
		t.Fatalf("set maint provider_cost: %v", err)
	}
	// 存量纯估算 run（provider_cost 全空）：展示值必须与合并前一致。
	legacy := seedUsageRun(t, env, bot.ID, func(r *model.BotRun) {
		r.CreatedAt = yesterday
		r.CostEstimate = ptrCost(1.5)
	})
	seedUsageRequest(t, env, legacy.ID, ptrCost(1.5), "pm-5")

	wantOwn := 0.5 + 0.2 + 0.1      // 实收赢 0.5 + 仅实收 0.2 + 仅估算 0.1
	wantCost := wantOwn + 0.4 + 1.5 // 主线自身 + 维护 + 存量 run

	// 实时路径（昨天未汇总 → 混合读取退化明细扫描）。
	overview, err := env.bots.GetBotUsageOverview(userID, "7d", "")
	if err != nil {
		t.Fatalf("overview live: %v", err)
	}
	if len(overview.Bots) != 1 {
		t.Fatalf("应有 1 个 bot: %+v", overview.Bots)
	}
	b := overview.Bots[0]
	if b.Cost == nil || !costEq(*b.Cost, wantCost) {
		t.Fatalf("总览应按实收优先合计 %.4f: %+v", wantCost, b.Cost)
	}
	// 父 run 在窗口内：维护金额折进父的 main 桶（1.2+1.5），不记维护桶。
	if b.CostMain == nil || !costEq(*b.CostMain, wantCost) {
		t.Fatalf("主线桶应计实收优先合计 %.4f: %+v", wantCost, b.CostMain)
	}
	if b.CostMaintenance != nil {
		t.Fatalf("父在窗口内的维护不进维护桶: %+v", b.CostMaintenance)
	}
	// 全空请求 1 条为未计价；实收覆盖的不再算未计价。
	if b.UnpricedRequests != 1 {
		t.Fatalf("未计价数应只含全空请求 1 条: %+v", b)
	}
	detail, err := env.bots.GetBotUsageRunDetail(userID, run.ID)
	if err != nil {
		t.Fatalf("run detail: %v", err)
	}
	if detail.Run.DisplayCost == nil || !costEq(*detail.Run.DisplayCost, wantOwn+0.4) {
		t.Fatalf("明细显示成本应 %.4f（含关联维护）: %+v", wantOwn+0.4, detail.Run.DisplayCost)
	}
	if detail.Run.CostEstimate == nil || !costEq(*detail.Run.CostEstimate, 0.7) {
		t.Fatalf("cost_estimate 保持目录价快照 0.7: %+v", detail.Run.CostEstimate)
	}
	// 预算花费同口径。
	spend, err := repository.NewBotBudgetRepository(env.db).
		SpendInWindow([]string{bot.ID}, fixed.Add(-48*time.Hour), fixed.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("spend: %v", err)
	}
	if !costEq(spend.BotUSD[bot.ID], wantCost) {
		t.Fatalf("预算花费应 %.4f: %+v", wantCost, spend.BotUSD)
	}

	// 汇总后再读：覆盖日行须与实时完全一致（display_cost 合并口径落表）。
	env.bots.scanUsageRollupOnce()
	var daily []model.BotUsageDaily
	if err := env.db.Where("bot_id = ? AND date = ?", bot.ID,
		yesterday.In(time.UTC).Format("2006-01-02")).Find(&daily).Error; err != nil {
		t.Fatalf("list daily: %v", err)
	}
	var dailyCost float64
	var dailyHas bool
	for _, row := range daily {
		if row.DisplayCost != nil {
			dailyCost += *row.DisplayCost
			dailyHas = true
		}
	}
	if !dailyHas || !costEq(dailyCost, wantCost) {
		t.Fatalf("汇总行 display_cost 应合计 %.4f: %+v", wantCost, daily)
	}
	rolled, err := env.bots.GetBotUsageOverview(userID, "7d", "")
	if err != nil {
		t.Fatalf("overview rolled: %v", err)
	}
	if rolled.Bots[0].Cost == nil || !costEq(*rolled.Bots[0].Cost, wantCost) {
		t.Fatalf("汇总后总览应仍为 %.4f: %+v", wantCost, rolled.Bots[0].Cost)
	}
	if rolled.Bots[0].UnpricedRequests != 1 {
		t.Fatalf("汇总后未计价数仍应为 1: %+v", rolled.Bots[0])
	}
}
