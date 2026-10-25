package router

import (
	"net/http"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
)

// 花费页 REST（INT-162）：/api/bots/usage 与 /usage/runs/:runId 只读聚合的
// 鉴权、归属与响应形状。服务层口径细节见 service/bot_usage_test.go。

// mustNullKey 断言对象里该键存在且值为 null：可空金额字段必须显式给 null，
// 不允许 omitempty 缺键（缺失 ≠ 0，F4）。
func mustNullKey(t *testing.T, obj map[string]interface{}, key string) {
	t.Helper()
	v, ok := obj[key]
	if !ok {
		t.Fatalf("键 %s 必须存在（显式 null，不允许缺键）: %v", key, obj)
	}
	if v != nil {
		t.Fatalf("键 %s 应为 null，实得 %v", key, v)
	}
}

// seedUsageBotRun 直接落一条带成本的 run（绕过运行路径，聚合只读账本）。
func seedUsageBotRun(t *testing.T, e *e2eEnv, botID string, cost float64) *model.BotRun {
	t.Helper()
	now := time.Now().UTC()
	run := &model.BotRun{
		ID: uuid.NewString(), BotID: botID,
		TriggerSource: model.BotTriggerUserMessage, TriggerKey: "usage-rest-" + uuid.NewString(),
		Status: model.BotRunStatusCompleted, SessionKind: model.BotSessionKindMain,
		ConfigSnapshotJSON: "{}", CostEstimate: &cost,
		CreatedAt: now, StartedAt: &now, FinishedAt: &now,
	}
	if err := e.env.db.Create(run).Error; err != nil {
		t.Fatalf("seed run: %v", err)
	}
	return run
}

func TestBotUsageRESTRequiresJWT(t *testing.T) {
	e := setupE2E(t)
	code, _ := e.doJSONAs(t, "", http.MethodGet, "/api/bots/usage", nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("未认证应为 401，实得 %d", code)
	}
	code, _ = e.doJSONAs(t, "", http.MethodGet, "/api/bots/usage/runs/"+uuid.NewString(), nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("未认证明细应为 401，实得 %d", code)
	}
	code, _ = e.doJSONAs(t, "", http.MethodGet, "/api/bots/usage/insights", nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("未认证优化参考应为 401，实得 %d", code)
	}
}

func TestBotUsageRESTOverviewAndOwnership(t *testing.T) {
	e := setupE2E(t)
	botID := e.createBot(t)
	run := seedUsageBotRun(t, e, botID, 0.4)

	// 合法 range：today 单日行 + totals 形状。
	code, body := e.doJSON(t, http.MethodGet, "/api/bots/usage?range=today", nil)
	if code != http.StatusOK {
		t.Fatalf("today: %d %v", code, body)
	}
	data, _ := body["data"].(map[string]interface{})
	if data == nil || data["days"] == nil || data["totals"] == nil ||
		data["bots"] == nil || data["schedule_tasks"] == nil ||
		data["range"] != "today" || data["timezone"] == nil ||
		data["start"] == nil || data["end"] == nil {
		t.Fatalf("总览响应形状不符: %v", data)
	}
	days, _ := data["days"].([]interface{})
	if len(days) != 1 {
		t.Fatalf("today 应只有 1 个日行: %v", data["days"])
	}

	// 非法 range 400。
	code, body = e.doJSON(t, http.MethodGet, "/api/bots/usage?range=4d", nil)
	if code != http.StatusBadRequest {
		t.Fatalf("非法 range 应 400，实得 %d %v", code, body)
	}

	// 限定自己：200 且包含该机器人的成本。
	code, body = e.doJSON(t, http.MethodGet, "/api/bots/usage?range=7d&bot_id="+botID, nil)
	if code != http.StatusOK {
		t.Fatalf("bot_id 过滤: %d %v", code, body)
	}
	data, _ = body["data"].(map[string]interface{})
	bots, _ := data["bots"].([]interface{})
	if len(bots) != 1 {
		t.Fatalf("应只含指定机器人: %v", data["bots"])
	}
	first, _ := bots[0].(map[string]interface{})
	if first["bot_id"] != botID {
		t.Fatalf("bot_id 不符: %v", first)
	}
	if cost, ok := first["cost"].(float64); !ok || cost != 0.4 {
		t.Fatalf("成本应为 0.4: %v", first)
	}

	// 他人 bot_id / runId 一律 404。公开注册关闭（INT-129）：第二个用户凭
	// 邀请注册（首个用户即管理员，e.auth.Token 可建邀请）。
	other := registerAndLoginInvitedRouterUser(t, e.env.router, e.auth.Token,
		"usageother", "usageother@example.com", "Password123")
	otherJWT := "Bearer " + other.Token
	code, body = e.doJSONAs(t, otherJWT, http.MethodGet, "/api/bots/usage?bot_id="+botID, nil)
	if code != http.StatusNotFound {
		t.Fatalf("他人 bot_id 应 404，实得 %d %v", code, body)
	}
	if got, _ := body["code"].(float64); got != 40424 {
		t.Fatalf("他人 bot_id 错误码应为 40424: %v", body)
	}
	code, body = e.doJSONAs(t, otherJWT, http.MethodGet, "/api/bots/usage/runs/"+run.ID, nil)
	if code != http.StatusNotFound {
		t.Fatalf("他人 runId 应 404，实得 %d %v", code, body)
	}
	if got, _ := body["code"].(float64); got != 40425 {
		t.Fatalf("他人 runId 错误码应为 40425: %v", body)
	}
}

func TestBotUsageRESTRunDetailShape(t *testing.T) {
	e := setupE2E(t)
	botID := e.createBot(t)
	run := seedUsageBotRun(t, e, botID, 0.6)

	code, body := e.doJSON(t, http.MethodGet, "/api/bots/usage/runs/"+run.ID, nil)
	if code != http.StatusOK {
		t.Fatalf("detail: %d %v", code, body)
	}
	data, _ := body["data"].(map[string]interface{})
	runData, _ := data["run"].(map[string]interface{})
	requests, _ := data["requests"].([]interface{})
	maintenance, _ := data["maintenance_runs"].([]interface{})
	if runData == nil || requests == nil || maintenance == nil {
		t.Fatalf("明细响应形状不符: %v", data)
	}
	if runData["id"] != run.ID || runData["bot_name"] == nil ||
		runData["display_cost"] == nil || runData["maintenance_cost"] != nil {
		t.Fatalf("明细 run 投影不符: %v", runData)
	}
	if cost, ok := runData["display_cost"].(float64); !ok || cost != 0.6 {
		t.Fatalf("无维护时 display_cost 应等于自身 0.6: %v", runData)
	}
}

func TestBotUsageRESTScheduleTaskCostLast7d(t *testing.T) {
	e := setupE2E(t)
	botID := e.createBot(t)
	now := time.Now().UTC().Add(time.Hour)
	task := &model.BotScheduleTask{
		ID: uuid.NewString(), BotID: botID,
		Name: "费用任务", CronExpression: "0 9 * * *", Timezone: "Asia/Shanghai",
		Instruction: "hi", Enabled: true, Confirmed: true,
		NextRunAt: &now, CreatedAt: now, UpdatedAt: now,
	}
	if err := e.env.db.Create(task).Error; err != nil {
		t.Fatalf("seed task: %v", err)
	}
	schedCost := 0.3
	taskID := task.ID
	run := &model.BotRun{
		ID: uuid.NewString(), BotID: botID,
		TriggerSource: model.BotTriggerSchedule, TriggerKey: "schedule:" + task.ID + ":" + now.Format(time.RFC3339Nano),
		Status: model.BotRunStatusCompleted, SessionKind: model.BotSessionKindSchedule,
		ScheduleTaskID: &taskID, ScheduledFor: &now, CostEstimate: &schedCost,
		ConfigSnapshotJSON: "{}", CreatedAt: now, StartedAt: &now, FinishedAt: &now,
	}
	if err := e.env.db.Create(run).Error; err != nil {
		t.Fatalf("seed schedule run: %v", err)
	}

	code, body := e.doJSON(t, http.MethodGet, "/api/bots/"+botID+"/schedule-tasks", nil)
	if code != http.StatusOK {
		t.Fatalf("list: %d %v", code, body)
	}
	items, _ := body["data"].(map[string]interface{})["items"].([]interface{})
	if len(items) != 1 {
		t.Fatalf("应有 1 个任务: %v", body)
	}
	first, _ := items[0].(map[string]interface{})
	if first["id"] != task.ID {
		t.Fatalf("任务不符: %v", first)
	}
	if cost, ok := first["cost_last_7d"].(float64); !ok || cost != 0.3 {
		t.Fatalf("cost_last_7d 应为 0.3: %v", first)
	}

	// 定时任务运行列表带显示成本（无维护时等于自身成本）。
	code, body = e.doJSON(t, http.MethodGet, "/api/bots/"+botID+"/schedule-tasks/"+task.ID+"/runs", nil)
	if code != http.StatusOK {
		t.Fatalf("runs: %d %v", code, body)
	}
	runItems, _ := body["data"].(map[string]interface{})["items"].([]interface{})
	if len(runItems) != 1 {
		t.Fatalf("应有 1 条运行: %v", body)
	}
	runItem, _ := runItems[0].(map[string]interface{})
	if cost, ok := runItem["display_cost_estimate"].(float64); !ok || cost != 0.3 {
		t.Fatalf("运行 display_cost_estimate 应为 0.3: %v", runItem)
	}
}

// 可空金额字段显式 null（F4）：全未计价时响应里这些键存在且值为 null，
// 缺键即契约违例（omitempty 会把 nil 变缺键）。
func TestBotUsageRESTNullableMoneyKeysAlwaysPresent(t *testing.T) {
	e := setupE2E(t)
	botID := e.createBot(t)
	now := time.Now().UTC()

	// 未计价主线 run + 未计价请求 + 未计价维护（挂本 run）。
	run := &model.BotRun{
		ID: uuid.NewString(), BotID: botID,
		TriggerSource: model.BotTriggerUserMessage, TriggerKey: "usage-null-" + uuid.NewString(),
		Status: model.BotRunStatusCompleted, SessionKind: model.BotSessionKindMain,
		ConfigSnapshotJSON: "{}", CreatedAt: now, StartedAt: &now, FinishedAt: &now,
	}
	if err := e.env.db.Create(run).Error; err != nil {
		t.Fatalf("seed run: %v", err)
	}
	req := &model.BotModelRequest{
		ID: uuid.NewString(), RunID: run.ID, Ordinal: 1,
		Status: model.BotModelRequestSucceeded, StartedAt: now,
		PrefixHash: "p", ToolsHash: "t", RequestHash: "h", TokenEstimator: "chars/4",
	}
	if err := e.env.db.Create(req).Error; err != nil {
		t.Fatalf("seed request: %v", err)
	}
	maint := &model.BotMaintenanceRun{
		ID: uuid.NewString(), BotID: botID,
		Kind: model.BotMaintenanceCompaction, TriggerKey: "usage-null-maint-" + uuid.NewString(),
		ParentRunID: &run.ID, Status: model.BotMaintenanceSucceeded,
		ConfigSnapshotJSON: "{}", InputSnapshotJSON: "{}", ResultJSON: "{}",
		CreatedAt: now, StartedAt: &now, FinishedAt: &now,
	}
	if err := e.env.db.Create(maint).Error; err != nil {
		t.Fatalf("seed maintenance run: %v", err)
	}
	mreq := &model.BotMaintenanceRequest{
		ID: uuid.NewString(), MaintenanceRunID: maint.ID, Ordinal: 1,
		Purpose: model.BotPurposeSummary, Model: "fake-model",
		Status:     model.BotModelRequestSucceeded,
		PrefixHash: "p", ToolsetVersion: 1, StartedAt: now,
	}
	if err := e.env.db.Create(mreq).Error; err != nil {
		t.Fatalf("seed maintenance request: %v", err)
	}
	// 未计价 schedule run：任务出现在总览聚合里，任务桶金额键全 null。
	taskNow := now.Add(time.Hour)
	task := &model.BotScheduleTask{
		ID: uuid.NewString(), BotID: botID,
		Name: "空成本任务", CronExpression: "0 9 * * *", Timezone: "UTC",
		Instruction: "hi", Enabled: true, Confirmed: true,
		NextRunAt: &taskNow, CreatedAt: now, UpdatedAt: now,
	}
	if err := e.env.db.Create(task).Error; err != nil {
		t.Fatalf("seed task: %v", err)
	}
	taskID := task.ID
	schedRun := &model.BotRun{
		ID: uuid.NewString(), BotID: botID,
		TriggerSource: model.BotTriggerSchedule, TriggerKey: "schedule:" + task.ID + ":" + now.Format(time.RFC3339Nano),
		Status: model.BotRunStatusCompleted, SessionKind: model.BotSessionKindSchedule,
		ScheduleTaskID: &taskID, ScheduledFor: &now,
		ConfigSnapshotJSON: "{}", CreatedAt: now, StartedAt: &now, FinishedAt: &now,
	}
	if err := e.env.db.Create(schedRun).Error; err != nil {
		t.Fatalf("seed schedule run: %v", err)
	}

	code, body := e.doJSON(t, http.MethodGet, "/api/bots/usage?range=7d", nil)
	if code != http.StatusOK {
		t.Fatalf("overview: %d %v", code, body)
	}
	data, _ := body["data"].(map[string]interface{})
	totals, _ := data["totals"].(map[string]interface{})
	mustNullKey(t, totals, "cost")
	mustNullKey(t, totals, "daily_average")
	bots, _ := data["bots"].([]interface{})
	if len(bots) != 1 {
		t.Fatalf("应有 1 个机器人: %v", data["bots"])
	}
	bot0, _ := bots[0].(map[string]interface{})
	for _, key := range []string{
		"cost", "cost_main", "cost_schedule", "cost_schedule_linked_approx",
		"cost_maintenance", "avg_run_cost", "max_run_cost", "cache_hit_rate",
	} {
		mustNullKey(t, bot0, key)
	}
	tasks, _ := data["schedule_tasks"].([]interface{})
	if len(tasks) != 1 {
		t.Fatalf("应有 1 个任务: %v", data["schedule_tasks"])
	}
	task0, _ := tasks[0].(map[string]interface{})
	for _, key := range []string{"cost", "avg_run_cost", "max_run_cost", "linked_main_cost", "estimated_monthly_cost"} {
		mustNullKey(t, task0, key)
	}

	code, body = e.doJSON(t, http.MethodGet, "/api/bots/usage/runs/"+run.ID, nil)
	if code != http.StatusOK {
		t.Fatalf("detail: %d %v", code, body)
	}
	data, _ = body["data"].(map[string]interface{})
	runData, _ := data["run"].(map[string]interface{})
	for _, key := range []string{"cost_estimate", "cost_source", "maintenance_cost", "display_cost"} {
		mustNullKey(t, runData, key)
	}
	requests, _ := data["requests"].([]interface{})
	if len(requests) != 1 {
		t.Fatalf("应有 1 条请求: %v", data["requests"])
	}
	req0, _ := requests[0].(map[string]interface{})
	mustNullKey(t, req0, "cost_estimate")
	mustNullKey(t, req0, "cost_source")
	maintRuns, _ := data["maintenance_runs"].([]interface{})
	if len(maintRuns) != 1 {
		t.Fatalf("应有 1 条维护运行: %v", data["maintenance_runs"])
	}
	maint0, _ := maintRuns[0].(map[string]interface{})
	mustNullKey(t, maint0, "cost_estimate")
	maintReqs, _ := maint0["requests"].([]interface{})
	if len(maintReqs) != 1 {
		t.Fatalf("维护请求应随行返回: %v", maint0)
	}
	mreq0, _ := maintReqs[0].(map[string]interface{})
	mustNullKey(t, mreq0, "cost_estimate")
	mustNullKey(t, mreq0, "cost_source")

	// 任务投影（手写响应）：cost_last_7d 与 last_run.display_cost_estimate 同样
	// 无已计价时显式 null。
	code, body = e.doJSON(t, http.MethodGet, "/api/bots/"+botID+"/schedule-tasks", nil)
	if code != http.StatusOK {
		t.Fatalf("schedule-tasks: %d %v", code, body)
	}
	items, _ := body["data"].(map[string]interface{})["items"].([]interface{})
	if len(items) != 1 {
		t.Fatalf("应有 1 个任务: %v", body)
	}
	first, _ := items[0].(map[string]interface{})
	mustNullKey(t, first, "cost_last_7d")
	lastRun, _ := first["last_run"].(map[string]interface{})
	if lastRun == nil {
		t.Fatalf("有运行的任务应带 last_run: %v", first)
	}
	mustNullKey(t, lastRun, "display_cost_estimate")
}

// 花费优化参考（INT-163）：/api/bots/usage/insights 的鉴权、参数校验、归属与
// 响应形状。服务层口径细节见 service/bot_usage_insights_test.go。
func TestBotUsageRESTInsights(t *testing.T) {
	e := setupE2E(t)
	botID := e.createBot(t)
	seedUsageBotRun(t, e, botID, 0.4)

	code, body := e.doJSON(t, http.MethodGet, "/api/bots/usage/insights?range=today", nil)
	if code != http.StatusOK {
		t.Fatalf("insights: %d %v", code, body)
	}
	data, _ := body["data"].(map[string]interface{})
	if data == nil || data["cache_hit_by_day"] == nil || data["bots"] == nil ||
		data["schedule_tasks"] == nil || data["range"] != "today" ||
		data["timezone"] == nil || data["start"] == nil || data["end"] == nil {
		t.Fatalf("优化参考响应形状不符: %v", data)
	}
	bots, _ := data["bots"].([]interface{})
	if len(bots) != 1 {
		t.Fatalf("应只含集合内机器人: %v", data["bots"])
	}
	bot0, _ := bots[0].(map[string]interface{})
	if bot0["bot_id"] != botID || bot0["name"] == nil {
		t.Fatalf("机器人行头不符: %v", bot0)
	}
	// 必填键显式存在：可空字段为 null，数组字段为 [] 而不是缺键。
	for _, key := range []string{"tools", "maintenance", "failures", "context_growth", "model_alternatives"} {
		if _, ok := bot0[key]; !ok {
			t.Fatalf("键 %s 必须存在: %v", key, bot0)
		}
	}
	tools, _ := bot0["tools"].(map[string]interface{})
	if tools == nil {
		t.Fatalf("tools 应为对象: %v", bot0)
	}
	mustNullKey(t, tools, "share")
	failures, _ := bot0["failures"].(map[string]interface{})
	if failures == nil {
		t.Fatalf("failures 应为对象: %v", bot0)
	}
	mustNullKey(t, failures, "failed_requests_cost")
	mustNullKey(t, failures, "failed_runs_cost")
	maintenance, _ := bot0["maintenance"].(map[string]interface{})
	if maintenance == nil {
		t.Fatalf("maintenance 应为对象: %v", maintenance)
	}
	mustNullKey(t, maintenance, "cost")
	mustNullKey(t, maintenance, "share")

	// 非法 range 400；bot_id 限定自己 200。
	code, body = e.doJSON(t, http.MethodGet, "/api/bots/usage/insights?range=4d", nil)
	if code != http.StatusBadRequest {
		t.Fatalf("非法 range 应 400，实得 %d %v", code, body)
	}
	code, body = e.doJSON(t, http.MethodGet, "/api/bots/usage/insights?bot_id="+botID, nil)
	if code != http.StatusOK {
		t.Fatalf("bot_id 过滤: %d %v", code, body)
	}

	// 他人 bot_id 404（公开注册关闭：第二个用户凭邀请注册）。
	other := registerAndLoginInvitedRouterUser(t, e.env.router, e.auth.Token,
		"insightother", "insightother@example.com", "Password123")
	otherJWT := "Bearer " + other.Token
	code, body = e.doJSONAs(t, otherJWT, http.MethodGet, "/api/bots/usage/insights?bot_id="+botID, nil)
	if code != http.StatusNotFound {
		t.Fatalf("他人 bot_id 应 404，实得 %d %v", code, body)
	}
	if got, _ := body["code"].(float64); got != 40424 {
		t.Fatalf("他人 bot_id 错误码应为 40424: %v", body)
	}
}
