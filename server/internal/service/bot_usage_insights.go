package service

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/catalog"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
)

// 花费页第二期（INT-163）：成本优化参考。与总览共用 loadBotUsageLedger 的
// 同一次账本扫描（bot_usage.go：窗口/机器人集合/统一请求聚合/三桶累加/任务
// 行都在共享层完成），这里只补本端点特有的请求级查询（首请求、重试计数、
// 失败聚合、工具调用三元组、run 冻结快照）再投影。insights 不触发总览的月
// 推算等投影；维护占比的分子（维护明细）与分母（共享累加器的三桶之和）来
// 自同一扫描快照，中间落账不会出现 share > 100%。

// usageContextGrowthMaxPoints 每机器人 context_growth 保留的最新点数上限
// （超出截掉最早点并置 context_growth_truncated）。
const usageContextGrowthMaxPoints = 200

// usageModelCandidatesMax 换模型差价每组保留的最便宜候选数。
const usageModelCandidatesMax = 3

// scheduleIdleRunIDs 返回参与空转判定的 run ID（工具调用三元组查询与 taskAccs
// 分母同口径）：SessionKind == schedule 且 ScheduleTaskID 非空；主线 run 不进
// 空转判定，也不进三元组的连接查询。
func scheduleIdleRunIDs(runRows []repository.UsageRunRow) []string {
	ids := make([]string, 0, len(runRows))
	for i := range runRows {
		row := &runRows[i]
		if row.SessionKind == model.BotSessionKindSchedule && row.ScheduleTaskID != nil {
			ids = append(ids, row.ID)
		}
	}
	return ids
}

// usageRunCatalogModelOf 聚合扫描行的 (catalog_id, model)：两个字段都取 run
// 冻结快照值。快照缺 catalog_id（INT-133 之前的旧快照）或 model 时 ok=false，
// 调用方跳过换模型差价分组——模型名跨目录重名（同名模型在多家目录各有现价），
// 回退机器人当前目录会让旧目录的 actual_cost 对上新目录的现价候选，差价没有
// 意义；带齐 (catalog_id, model) 的快照不受影响（进投影再过目录双重校验）。
func usageRunCatalogModelOf(snapJSON string) (catalogID, modelID string, ok bool) {
	var snap botConfigSnapshot
	if err := json.Unmarshal([]byte(snapJSON), &snap); err != nil {
		return "", "", false
	}
	if snap.CatalogID == "" || snap.Model == "" {
		return "", "", false
	}
	return snap.CatalogID, snap.Model, true
}

// botUsageCandidateCost 按目录单价对同一批 token 合计重算总价（换模型差价）。
// botCostEstimate 对 token 数是线性函数（Σf(x) = f(Σx)），可对组内 token 合计
// 一次求价，不必逐请求重算；cached 钳 prompt 的异常按合计口径处理。调用方须
// 保证 m.Cost.Input / m.Cost.Output 非空。
func botUsageCandidateCost(m catalog.Model, prompt, cached, completion int64) float64 {
	if cached > prompt {
		cached = prompt
	}
	uncached := prompt - cached
	read := *m.Cost.Input
	if m.Cost.CacheRead != nil {
		read = *m.Cost.CacheRead
	}
	return (float64(uncached)**m.Cost.Input + float64(cached)*read + float64(completion)**m.Cost.Output) / 1e6
}

// GetBotUsageInsights 花费优化参考（INT-163）。
func (s *BotService) GetBotUsageInsights(userID, rangeParam, botID string) (*api.BotUsageInsights, error) {
	// 共享加载先行：range 校验、bot_id 校验 404、机器人集合、窗口与三桶
	// 累加全部来自同一次账本扫描（不再调 GetBotUsageOverview）；维护请求
	// 明细行只有本端点拉取（用途拆分、失败统计、按天命中都吃它）。
	led, err := s.loadBotUsageLedger(userID, rangeParam, botID, true)
	if err != nil {
		return nil, err
	}

	repo := repository.NewBotUsageRepository(s.db)
	runIDs := make([]string, 0, len(led.runRows))
	runIdx := make(map[string]int, len(led.runRows))
	for i := range led.runRows {
		runIDs = append(runIDs, led.runRows[i].ID)
		runIdx[led.runRows[i].ID] = i
	}
	// 工具调用三元组只服务定时任务空转判定：runIDs 单独收窄为 schedule 且挂
	// 了任务的 run（scheduleIdleRunIDs，与 taskAccs/空转分母同口径），主线
	// run 不进该连接查询。
	scheduleRunIDs := scheduleIdleRunIDs(led.runRows)
	maintIdx := make(map[string]int, len(led.maintRows))
	for i := range led.maintRows {
		maintIdx[led.maintRows[i].ID] = i
	}

	firstReq, err := repo.FirstRequestInputByRun(runIDs)
	if err != nil {
		return nil, errs.ErrInternal
	}
	retries, err := repo.RetryCountsByRun(runIDs)
	if err != nil {
		return nil, errs.ErrInternal
	}
	failedAggs, err := repo.FailedRequestAggsByRun(runIDs)
	if err != nil {
		return nil, errs.ErrInternal
	}
	callTriples, err := repo.RunToolCallTriples(scheduleRunIDs)
	if err != nil {
		return nil, errs.ErrInternal
	}
	// 换模型差价分组的 run 冻结快照单独补查：快照列单行可达数 KB，总览不
	// 消费它，不随共享账本扫描回读（30 天窗口会把整列读回内存）。再按
	// runAgg.Priced>0 过滤——无已计价请求的 run 不产生分组输入，连读都不必。
	pricedRunIDs := make([]string, 0, len(runIDs))
	for _, id := range runIDs {
		if led.runAgg[id].Priced > 0 {
			pricedRunIDs = append(pricedRunIDs, id)
		}
	}
	snapshots, err := repo.RunConfigSnapshotsByIDs(pricedRunIDs)
	if err != nil {
		return nil, errs.ErrInternal
	}
	// 读/写判定的事实源是服务层注册表：命中 botReadOnlyActions 为读，未列出
	// 的（含未知动作）按写处理。
	toolCallRuns := make(map[string]bool, len(scheduleRunIDs))
	writeCallRuns := make(map[string]bool, len(scheduleRunIDs))
	for _, t := range callTriples {
		toolCallRuns[t.RunID] = true
		// 「有写」要求调用实际/可能落效：被拒、取消、未执行或失败的写
		// 调用没有产生写入，该 run 仍算白跑（EffectiveWrite 见仓库层）。
		if t.EffectiveWrite && !botReadOnlyActions[t.ToolName+"/"+t.Action] {
			writeCallRuns[t.RunID] = true
		}
	}

	// ---------- 按天缓存命中：请求记所属 run / 维护运行自己的当地日 ----------
	type dayCacheAcc struct {
		cached, prompt int64
	}
	dayAccs := make([]*dayCacheAcc, len(led.dates))
	dayIndex := make(map[string]int, len(led.dates))
	for i, d := range led.dates {
		dayAccs[i] = &dayCacheAcc{}
		dayIndex[d] = i
	}
	addDayCache := func(t time.Time, cached, prompt int64) {
		if idx, ok := dayIndex[t.In(led.loc).Format("2006-01-02")]; ok {
			dayAccs[idx].cached += cached
			dayAccs[idx].prompt += prompt
		}
	}
	// 主线/定时请求的合计来自共享的统一聚合（cached 已逐行钳制，与机器人级
	// /总览同一分子）。
	for i := range led.runRows {
		agg := led.runAgg[led.runRows[i].ID]
		addDayCache(led.runRows[i].CreatedAt, agg.CachedSum, agg.PromptSum)
	}
	// 维护请求逐行钳制后按维护运行自己的 created_at 记日（与总览记日一致）。
	for _, row := range led.maintReqRows {
		if !row.UsageComplete {
			continue
		}
		mi, ok := maintIdx[row.MaintenanceRunID]
		if !ok {
			continue
		}
		var prompt, cached int64
		if row.PromptTokens != nil {
			prompt = *row.PromptTokens
		}
		if row.CachedTokens != nil {
			cached = *row.CachedTokens
		}
		if cached > prompt {
			cached = prompt
		}
		addDayCache(led.maintRows[mi].CreatedAt, cached, prompt)
	}

	// ---------- 每机器人累加器 ----------
	type modelGroupKey struct {
		catalogID, model string
	}
	type modelGroupAcc struct {
		requests                   int64
		actualCost                 float64
		prompt, cached, completion int64
	}
	type botInsightsAcc struct {
		toolsPrompt, toolsTokens int64
		maintCost                *float64
		maintPurposes            map[string]*float64
		failedRequests           int64
		failedRequestsCost       *float64
		failedRuns               int64
		failedRunsCost           *float64
		retriedRequests          int64
		byError                  map[failureErrorKey]*float64Acc
		contextGrowth            []api.BotUsageContextPoint
		modelGroups              map[modelGroupKey]*modelGroupAcc
	}
	botAccs := make(map[string]*botInsightsAcc, len(led.botAccs))
	for botID := range led.botAccs {
		botAccs[botID] = &botInsightsAcc{
			maintPurposes: map[string]*float64{},
			byError:       map[failureErrorKey]*float64Acc{},
			modelGroups:   map[modelGroupKey]*modelGroupAcc{},
		}
	}

	// ---------- 主线 / 定时 run ----------
	for i := range led.runRows {
		row := &led.runRows[i]
		acc := botAccs[row.BotID]
		if acc == nil {
			continue
		}
		agg := led.runAgg[row.ID]
		acc.toolsPrompt += agg.PromptSum
		acc.toolsTokens += agg.ToolsSum
		acc.retriedRequests += retries[row.ID]

		// 失败 run：计数与显示成本（自身 + 关联维护，实收优先）。
		if model.BotRunStatus(row.Status) == model.BotRunStatusFailed {
			acc.failedRuns++
			acc.failedRunsCost = botSumOptional(acc.failedRunsCost,
				botSumOptional(runDisplayOwn(row.CostEstimate, agg), led.maintByParent[row.ID]))
		}

		// context_growth：有请求的 run 各取一个点，runRows 已按时间升序。
		if fr, ok := firstReq[row.ID]; ok {
			acc.contextGrowth = append(acc.contextGrowth, api.BotUsageContextPoint{
				RunId:       row.ID,
				At:          row.CreatedAt.UTC().Format(time.RFC3339Nano),
				InputTokens: fr.InputTokens,
				Estimated:   fr.Estimated,
				SessionKind: row.SessionKind,
			})
		}

		// 换模型差价分组：run 冻结快照的 (catalog_id, model)。快照缺 catalog_id
		// 的 run 不参与（理由见 usageRunCatalogModelOf），不用机器人当前目录
		// 填洞；custom 目录不计候选。
		catalogID, modelID, ok := usageRunCatalogModelOf(snapshots[row.ID])
		if !ok {
			continue
		}
		key := modelGroupKey{catalogID: catalogID, model: modelID}
		group := acc.modelGroups[key]
		if group == nil {
			group = &modelGroupAcc{}
			acc.modelGroups[key] = group
		}
		group.requests += agg.Priced
		if agg.CostSum != nil {
			group.actualCost += *agg.CostSum
		}
		group.prompt += agg.PricedPromptSum
		group.cached += agg.PricedCachedSum
		group.completion += agg.PricedCompletionSum
	}

	// 失败请求（主线侧）按 (run, error_code) 聚合行归并到机器人。
	for _, f := range failedAggs {
		ri, ok := runIdx[f.RunID]
		if !ok {
			continue
		}
		acc := botAccs[led.runRows[ri].BotID]
		if acc == nil {
			continue
		}
		acc.failedRequests += f.Requests
		acc.failedRequestsCost = botSumOptional(acc.failedRequestsCost, f.CostSum)
		key := failureErrorKeyOf(f.ErrorCode)
		merged := acc.byError[key]
		if merged == nil {
			merged = &float64Acc{}
			acc.byError[key] = merged
		}
		merged.requests += f.Requests
		merged.cost = botSumOptional(merged.cost, f.CostSum)
	}

	// ---------- 维护请求（明细按维护运行归并） ----------
	for _, row := range led.maintReqRows {
		mi, ok := maintIdx[row.MaintenanceRunID]
		if !ok {
			continue
		}
		acc := botAccs[led.maintRows[mi].BotID]
		if acc == nil {
			continue
		}
		displayCost := requestDisplayCost(row.ProviderCost, row.CostEstimate)
		if displayCost != nil {
			acc.maintCost = botSumOptional(acc.maintCost, displayCost)
			acc.maintPurposes[row.Purpose] = botSumOptional(acc.maintPurposes[row.Purpose], displayCost)
		} else if _, seen := acc.maintPurposes[row.Purpose]; !seen {
			// 用途在窗口内出现过即列出（全部未计价时 cost 为 null）。
			acc.maintPurposes[row.Purpose] = nil
		}
		if model.BotModelRequestStatus(row.Status) == model.BotModelRequestFailed {
			acc.failedRequests++
			acc.failedRequestsCost = botSumOptional(acc.failedRequestsCost, displayCost)
			key := failureErrorKeyOf(row.ErrorCode)
			merged := acc.byError[key]
			if merged == nil {
				merged = &float64Acc{}
				acc.byError[key] = merged
			}
			merged.requests++
			merged.cost = botSumOptional(merged.cost, displayCost)
		}
	}

	// ---------- 定时任务空转 ----------
	type taskInsightsAcc struct {
		botID                                    string
		evaluated, silent, noTool, noWrite, idle int64
		idleCost                                 *float64
		cached, prompt                           int64
	}
	taskAccs := map[string]*taskInsightsAcc{}
	for i := range led.runRows {
		row := &led.runRows[i]
		if row.SessionKind != model.BotSessionKindSchedule || row.ScheduleTaskID == nil {
			continue
		}
		acc := taskAccs[*row.ScheduleTaskID]
		if acc == nil {
			acc = &taskInsightsAcc{botID: row.BotID}
			taskAccs[*row.ScheduleTaskID] = acc
		}
		agg := led.runAgg[row.ID]
		acc.cached += agg.CachedSum
		acc.prompt += agg.PromptSum
		switch model.BotRunStatus(row.Status) {
		case model.BotRunStatusCompleted, model.BotRunStatusFailed, model.BotRunStatusStopped:
			// 空转分母只含非 skipped 的终态 run；queued/running 不计。
			acc.evaluated++
			var idle bool
			if row.Silent {
				acc.silent++
				idle = true
			}
			if !toolCallRuns[row.ID] {
				acc.noTool++
				idle = true
			}
			if !writeCallRuns[row.ID] {
				acc.noWrite++
				idle = true
			}
			if idle {
				acc.idle++
				acc.idleCost = botSumOptional(acc.idleCost,
					botSumOptional(runDisplayOwn(row.CostEstimate, agg), led.maintByParent[row.ID]))
			}
		}
	}

	// ---------- 投影 ----------
	insights := &api.BotUsageInsights{
		Range:         api.BotUsageInsightsRange(led.rangeParam),
		Timezone:      led.tzName,
		Start:         led.start.UTC().Format(time.RFC3339Nano),
		End:           led.end.UTC().Format(time.RFC3339Nano),
		CacheHitByDay: make([]api.BotUsageDayCacheHit, 0, len(dayAccs)),
		Bots:          make([]api.BotUsageInsightsBot, 0, len(botAccs)),
		ScheduleTasks: make([]api.BotUsageInsightsTask, 0, len(taskAccs)),
	}
	for i, acc := range dayAccs {
		var rate *float64
		if acc.prompt > 0 {
			rate = ptrFloat64(float64(acc.cached) / float64(acc.prompt))
		}
		insights.CacheHitByDay = append(insights.CacheHitByDay, api.BotUsageDayCacheHit{Date: led.dates[i], Rate: rate})
	}

	// 机器人行排序与总览一致：三桶之和降序、bot_id 升序并列。总额直接取
	// 共享累加器——它同时也是维护占比的分母来源。
	botOrder := make([]string, 0, len(led.botAccs))
	botTotalCost := make(map[string]*float64, len(led.botAccs))
	for botID, acc := range led.botAccs {
		botOrder = append(botOrder, botID)
		botTotalCost[botID] = acc.totalCost()
	}
	sort.Slice(botOrder, func(i, j int) bool {
		ci, cj := botTotalCost[botOrder[i]], botTotalCost[botOrder[j]]
		if botUsageCostLess(ci, cj) {
			return true
		}
		if botUsageCostLess(cj, ci) {
			return false
		}
		return botOrder[i] < botOrder[j]
	})
	for _, botID := range botOrder {
		acc := botAccs[botID]
		shared := led.botAccs[botID]
		// 缓存命中率与按天/按任务同一分子（共享统一聚合，cached 已逐行钳制）。
		var cacheHit *float64
		if shared.promptSum > 0 {
			cacheHit = ptrFloat64(float64(shared.cachedSum) / float64(shared.promptSum))
		}

		tools := api.BotUsageToolsShare{
			PromptTokens:        acc.toolsPrompt,
			ToolsTokensEstimate: acc.toolsTokens,
		}
		if acc.toolsPrompt > 0 {
			tools.Share = ptrFloat64(float64(acc.toolsTokens) / float64(acc.toolsPrompt))
		}

		// 维护占比：分母 = 共享累加器的三桶之和（同一扫描快照，分子分母同
		// 源），任一边未计价或分母为 0 时 null（缺失 ≠ 0）。
		purposes := make([]api.BotUsageMaintenancePurpose, 0, len(acc.maintPurposes))
		for purpose, cost := range acc.maintPurposes {
			purposes = append(purposes, api.BotUsageMaintenancePurpose{Purpose: purpose, Cost: cost})
		}
		sort.Slice(purposes, func(i, j int) bool {
			if botUsageCostLess(purposes[i].Cost, purposes[j].Cost) {
				return true
			}
			if botUsageCostLess(purposes[j].Cost, purposes[i].Cost) {
				return false
			}
			return purposes[i].Purpose < purposes[j].Purpose
		})
		maintenance := api.BotUsageMaintenanceShare{Cost: acc.maintCost, ByPurpose: purposes}
		if total := botTotalCost[botID]; acc.maintCost != nil && total != nil && *total > 0 {
			maintenance.Share = ptrFloat64(*acc.maintCost / *total)
		}

		byError := make([]api.BotUsageFailureError, 0, len(acc.byError))
		for key, merged := range acc.byError {
			var code *string
			if key.present {
				c := key.code
				code = &c
			}
			byError = append(byError, api.BotUsageFailureError{ErrorCode: code, Requests: merged.requests, Cost: merged.cost})
		}
		sort.Slice(byError, func(i, j int) bool {
			// 契约「null 组排最后」恒定优先：先分出 null 组，其余再按 cost
			// 降序、requests 降序、码升序并列——null 组不参与后面的比较，
			// cost 再高也垫底。
			if (byError[i].ErrorCode == nil) != (byError[j].ErrorCode == nil) {
				return byError[j].ErrorCode == nil
			}
			if botUsageCostLess(byError[i].Cost, byError[j].Cost) {
				return true
			}
			if botUsageCostLess(byError[j].Cost, byError[i].Cost) {
				return false
			}
			if byError[i].Requests != byError[j].Requests {
				return byError[i].Requests > byError[j].Requests
			}
			return failureErrorKeyLess(byError[i].ErrorCode, byError[j].ErrorCode)
		})
		failures := api.BotUsageFailureStats{
			FailedRequests:     acc.failedRequests,
			FailedRequestsCost: acc.failedRequestsCost,
			FailedRuns:         acc.failedRuns,
			FailedRunsCost:     acc.failedRunsCost,
			RetriedRequests:    acc.retriedRequests,
			ByError:            byError,
		}

		// context_growth 只保留最新 200 点，超出置 truncated。
		growth := acc.contextGrowth
		growthTruncated := false
		if len(growth) > usageContextGrowthMaxPoints {
			growth = growth[len(growth)-usageContextGrowthMaxPoints:]
			growthTruncated = true
		}
		if growth == nil {
			growth = []api.BotUsageContextPoint{}
		}

		// 换模型差价：custom、无已计价请求、目录双重校验不过、候选为空（目录
		// 里没有其他 Input/Output 单价完整的模型）的组都不出现。分组只收带齐
		// (catalog_id, model) 的快照，旧快照已在累加侧跳过。
		groups := make([]api.BotUsageModelGroup, 0, len(acc.modelGroups))
		for key, ga := range acc.modelGroups {
			if key.catalogID == catalog.CustomCatalogID || ga.requests == 0 {
				continue
			}
			// 目录双重校验：快照的 (catalog, model) 必须都真实存在（FindModel
			// 先精确后 EqualFold，与候选跳过同语义）。目录查无（如目录快照更新
			// 后下线）的组不出现——actual_cost 是历史落库的 cost_estimate 合计，
			// 跟另一套目录的现价候选相减没有意义。
			provider, ok := catalog.FindProvider(key.catalogID)
			if !ok {
				continue
			}
			if _, ok := catalog.FindModel(key.catalogID, key.model); !ok {
				continue
			}
			group := api.BotUsageModelGroup{
				CatalogId:  key.catalogID,
				Model:      key.model,
				Requests:   ga.requests,
				ActualCost: ga.actualCost,
				Candidates: []api.BotUsageModelCandidate{},
			}
			for _, m := range provider.Models {
				if strings.EqualFold(m.ID, key.model) {
					continue
				}
				if m.Cost == nil || m.Cost.Input == nil || m.Cost.Output == nil {
					continue
				}
				cost := botUsageCandidateCost(m, ga.prompt, ga.cached, ga.completion)
				group.Candidates = append(group.Candidates, api.BotUsageModelCandidate{
					Model:  m.ID,
					Cost:   cost,
					Saving: ga.actualCost - cost,
				})
			}
			sort.Slice(group.Candidates, func(i, j int) bool {
				if group.Candidates[i].Cost != group.Candidates[j].Cost {
					return group.Candidates[i].Cost < group.Candidates[j].Cost
				}
				return group.Candidates[i].Model < group.Candidates[j].Model
			})
			if len(group.Candidates) > usageModelCandidatesMax {
				group.Candidates = group.Candidates[:usageModelCandidatesMax]
			}
			if len(group.Candidates) == 0 {
				// 没有可比较的候选：空卡不上前端。
				continue
			}
			groups = append(groups, group)
		}
		sort.Slice(groups, func(i, j int) bool {
			if groups[i].ActualCost != groups[j].ActualCost {
				return groups[i].ActualCost > groups[j].ActualCost
			}
			if groups[i].CatalogId != groups[j].CatalogId {
				return groups[i].CatalogId < groups[j].CatalogId
			}
			return groups[i].Model < groups[j].Model
		})

		insights.Bots = append(insights.Bots, api.BotUsageInsightsBot{
			BotId:                  botID,
			Name:                   shared.name,
			CacheHitRate:           cacheHit,
			Tools:                  tools,
			Maintenance:            maintenance,
			Failures:               failures,
			ContextGrowth:          growth,
			ContextGrowthTruncated: growthTruncated,
			ModelAlternatives:      groups,
		})
	}

	for taskID, acc := range taskAccs {
		// 任务行信息与总览共用共享层的 taskRows（已删除任务取不到行，按契约
		// deleted=true、名称空串、enabled=false），不再经总览投影转手。
		var idleRate *float64
		if acc.evaluated > 0 {
			idleRate = ptrFloat64(float64(acc.idle) / float64(acc.evaluated))
		}
		var cacheHit *float64
		if acc.prompt > 0 {
			cacheHit = ptrFloat64(float64(acc.cached) / float64(acc.prompt))
		}
		item := api.BotUsageInsightsTask{
			TaskId:          taskID,
			BotId:           acc.botID,
			BotName:         led.botNames[acc.botID],
			Deleted:         true,
			CacheHitRate:    cacheHit,
			EvaluatedRuns:   acc.evaluated,
			IdleRuns:        acc.idle,
			IdleRate:        idleRate,
			IdleCost:        acc.idleCost,
			SilentRuns:      acc.silent,
			NoToolCallRuns:  acc.noTool,
			NoWriteCallRuns: acc.noWrite,
		}
		if task, ok := led.taskRows[taskID]; ok {
			item.Deleted = false
			item.Enabled = task.Enabled
			item.Name = task.Name
		}
		insights.ScheduleTasks = append(insights.ScheduleTasks, item)
	}
	sort.Slice(insights.ScheduleTasks, func(i, j int) bool {
		return insights.ScheduleTasks[i].TaskId < insights.ScheduleTasks[j].TaskId
	})
	return insights, nil
}

// failureErrorKey 是失败请求 error_code 分组的键：NULL 与空串（含纯空白）
// 都归 null 组（present=false，「无错误码」语义，前端渲染占位文案而非空白
// 单元格），非空串才进 present 组。
type failureErrorKey struct {
	present bool
	code    string
}

// failureErrorKeyOf 把失败行的 error_code 归组：nil / 空串 / 纯空白都归 null
// 组；非空串保持原码进 present 组（码本身不做 trim）。
func failureErrorKeyOf(code *string) failureErrorKey {
	if code == nil || strings.TrimSpace(*code) == "" {
		return failureErrorKey{}
	}
	return failureErrorKey{present: true, code: *code}
}

// failureErrorKeyLess 错误码组的确定性并列排序：null 组排后、其余按码升序。
func failureErrorKeyLess(a, b *string) bool {
	switch {
	case a == nil && b == nil:
		return false
	case a == nil:
		return false
	case b == nil:
		return true
	}
	return *a < *b
}

// float64Acc 是失败错误码分组的累加器：requests 计数 + cost 空值合并
// （全组未计价保持 null）。
type float64Acc struct {
	requests int64
	cost     *float64
}
