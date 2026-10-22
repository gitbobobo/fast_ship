package router

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
)

// 定时任务（INT-103）用户侧 REST 回归：只有查看与开关，没有创建/编辑/删除
// 端点；enabled 是唯一可写字段，body 出现其它字段按 400 拒绝。

func seedScheduleTaskRow(t *testing.T, e *e2eEnv, botID string) *model.BotScheduleTask {
	t.Helper()
	now := time.Now().UTC().Add(time.Hour)
	task := &model.BotScheduleTask{
		ID: uuid.NewString(), BotID: botID,
		Name: "每日汇总", CronExpression: "0 9 * * *", Timezone: "Asia/Shanghai",
		Instruction: "汇总昨日动态", Enabled: true, NeedsReport: true,
		Confirmed: true,
		NextRunAt: &now, CreatedAt: now, UpdatedAt: now,
	}
	if err := e.env.db.Create(task).Error; err != nil {
		t.Fatalf("seed task: %v", err)
	}
	return task
}

func TestBotScheduleRESTReadOnly(t *testing.T) {
	e := setupE2E(t)
	botID := e.createBot(t)
	task := seedScheduleTaskRow(t, e, botID)

	// 列表：形状冻结（含 last_run）。
	code, body := e.doJSON(t, http.MethodGet, "/api/bots/"+botID+"/schedule-tasks", nil)
	if code != http.StatusOK {
		t.Fatalf("list: %d %v", code, body)
	}
	items, _ := body["data"].(map[string]interface{})["items"].([]interface{})
	if len(items) != 1 {
		t.Fatalf("应有 1 个任务: %v", body)
	}
	first, _ := items[0].(map[string]interface{})
	for _, key := range []string{
		"id", "bot_id", "name", "cron_expression", "timezone", "instruction",
		"model", "needs_report", "enabled", "next_run_at", "created_at", "updated_at", "last_run",
	} {
		if _, ok := first[key]; !ok {
			t.Fatalf("任务投影缺字段 %s: %v", key, first)
		}
	}

	// 详情。
	code, body = e.doJSON(t, http.MethodGet, "/api/bots/"+botID+"/schedule-tasks/"+task.ID, nil)
	if code != http.StatusOK {
		t.Fatalf("get: %d %v", code, body)
	}
	if got, _ := body["data"].(map[string]interface{})["id"].(string); got != task.ID {
		t.Fatalf("详情 ID 不符: %v", body)
	}

	// 运行记录（空）。
	code, body = e.doJSON(t, http.MethodGet, "/api/bots/"+botID+"/schedule-tasks/"+task.ID+"/runs", nil)
	if code != http.StatusOK {
		t.Fatalf("runs: %d %v", code, body)
	}
	if items, _ := body["data"].(map[string]interface{})["items"].([]interface{}); len(items) != 0 {
		t.Fatalf("应为空列表: %v", body)
	}

	// 不存在的任务 → 40429。
	code, body = e.doJSON(t, http.MethodGet, "/api/bots/"+botID+"/schedule-tasks/"+uuid.NewString(), nil)
	if code != http.StatusNotFound {
		t.Fatalf("missing task: %d %v", code, body)
	}
	if got, _ := body["code"].(float64); got != 40429 {
		t.Fatalf("missing task 错误码应为 40429: %v", body)
	}
}

func TestBotScheduleRESTEnabledToggle(t *testing.T) {
	e := setupE2E(t)
	botID := e.createBot(t)
	task := seedScheduleTaskRow(t, e, botID)
	base := "/api/bots/" + botID + "/schedule-tasks/" + task.ID

	// 停用：next_run_at 清空。
	code, body := e.doJSON(t, http.MethodPut, base+"/enabled", map[string]interface{}{"enabled": false})
	if code != http.StatusOK {
		t.Fatalf("disable: %d %v", code, body)
	}
	data, _ := body["data"].(map[string]interface{})
	if enabled, _ := data["enabled"].(bool); enabled {
		t.Fatalf("应已停用: %v", data)
	}
	if next, ok := data["next_run_at"]; next != nil || !ok {
		t.Fatalf("停用后 next_run_at 应为 null: %v", data["next_run_at"])
	}

	// 重新启用：next_run_at 重算为未来。
	code, body = e.doJSON(t, http.MethodPut, base+"/enabled", map[string]interface{}{"enabled": true})
	if code != http.StatusOK {
		t.Fatalf("enable: %d %v", code, body)
	}
	data, _ = body["data"].(map[string]interface{})
	if next, _ := data["next_run_at"].(string); next == "" {
		t.Fatalf("启用后 next_run_at 应重算: %v", data)
	}
	var row model.BotScheduleTask
	if err := e.env.db.Where("id = ?", task.ID).First(&row).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !row.Enabled || row.NextRunAt == nil || !row.NextRunAt.After(time.Now().UTC()) {
		t.Fatalf("启用后应指向未来计划: %+v", row)
	}

	// 回归：body 出现其它字段 → 400；缺 enabled → 400。
	code, _ = e.doJSON(t, http.MethodPut, base+"/enabled", map[string]interface{}{
		"enabled": true, "cron_expression": "0 10 * * *",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("额外字段必须 400，实得 %d", code)
	}
	code, _ = e.doJSON(t, http.MethodPut, base+"/enabled", map[string]interface{}{})
	if code != http.StatusBadRequest {
		t.Fatalf("缺 enabled 必须 400，实得 %d", code)
	}
	// 额外字段没有落库。
	var after model.BotScheduleTask
	e.env.db.Where("id = ?", task.ID).First(&after)
	if after.CronExpression != task.CronExpression {
		t.Fatalf("用户不得经 REST 编辑任务配置")
	}
}

// 未复核任务（confirmed=false，确认机制上线前的存量行）不得经 REST 开关
// 直接启用：指令从未展示给用户核对，一键启用等于绕过确认卡重新武装
// （INT-136 审查）。停用不受限。
func TestBotScheduleRESTEnableUnconfirmedRejected(t *testing.T) {
	e := setupE2E(t)
	botID := e.createBot(t)
	task := seedScheduleTaskRow(t, e, botID)
	if err := e.env.db.Model(&model.BotScheduleTask{}).Where("id = ?", task.ID).Update("confirmed", false).Error; err != nil {
		t.Fatalf("unconfirm: %v", err)
	}
	base := "/api/bots/" + botID + "/schedule-tasks/" + task.ID

	code, body := e.doJSON(t, http.MethodPut, base+"/enabled", map[string]interface{}{"enabled": true})
	if code != http.StatusConflict {
		t.Fatalf("未复核任务 REST 启用应 409，实得 %d %v", code, body)
	}
	if got, _ := body["code"].(float64); got != 40934 {
		t.Fatalf("错误码应为 40934: %v", body)
	}
	// 停用与「启用后复核置位」不受影响。
	code, body = e.doJSON(t, http.MethodPut, base+"/enabled", map[string]interface{}{"enabled": false})
	if code != http.StatusOK {
		t.Fatalf("未复核任务停用不应受限: %d %v", code, body)
	}
	var row model.BotScheduleTask
	if err := e.env.db.Where("id = ?", task.ID).First(&row).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if row.Enabled || row.Confirmed {
		t.Fatalf("被拒的启用不得改库: %+v", row)
	}
}

func TestBotScheduleRESTNoWriteEndpoints(t *testing.T) {
	e := setupE2E(t)
	botID := e.createBot(t)
	task := seedScheduleTaskRow(t, e, botID)
	base := "/api/bots/" + botID + "/schedule-tasks"

	// 创建/编辑/删除端点不存在：路由未注册，一律 404。
	code, _ := e.doJSON(t, http.MethodPost, base, map[string]interface{}{
		"name": "x", "cron": "0 9 * * *", "instruction": "y",
	})
	if code != http.StatusNotFound {
		t.Fatalf("POST 创建端点应不存在，实得 %d", code)
	}
	code, _ = e.doJSON(t, http.MethodPut, base+"/"+task.ID, map[string]interface{}{
		"instruction": "改写",
	})
	if code != http.StatusNotFound {
		t.Fatalf("PUT 编辑端点应不存在，实得 %d", code)
	}
	code, _ = e.doJSON(t, http.MethodDelete, base+"/"+task.ID, nil)
	if code != http.StatusNotFound {
		t.Fatalf("DELETE 端点应不存在，实得 %d", code)
	}

	// 运行记录是只读资源。
	code, _ = e.doJSON(t, http.MethodPost, base+"/"+task.ID+"/runs", nil)
	if code != http.StatusNotFound {
		t.Fatalf("POST runs 端点应不存在，实得 %d", code)
	}
}

// 运行记录投影形状（含 usage / duration_ms）与状态枚举（skipped）。
func TestBotScheduleRESTRunsProjection(t *testing.T) {
	e := setupE2E(t)
	botID := e.createBot(t)
	task := seedScheduleTaskRow(t, e, botID)

	started := time.Now().UTC().Add(-time.Minute)
	finished := started.Add(30 * time.Second)
	prompt, completion, total := int64(10), int64(5), int64(15)
	taskID := task.ID
	run := &model.BotRun{
		ID: uuid.NewString(), BotID: botID,
		TriggerSource:  model.BotTriggerSchedule,
		TriggerKey:     "schedule:" + task.ID + ":" + started.Format(time.RFC3339Nano),
		Status:         model.BotRunStatusSkipped,
		SessionKind:    model.BotSessionKindSchedule,
		ScheduleTaskID: &taskID,
		ScheduledFor:   &started,
		Summary:        "错过的计划时间，已跳过",
		PromptTokens:   &prompt, CompletionTokens: &completion, TotalTokens: &total,
		UsageComplete: true,
		CreatedAt:     started, StartedAt: &started, FinishedAt: &finished,
	}
	if err := e.env.db.Create(run).Error; err != nil {
		t.Fatalf("seed run: %v", err)
	}

	code, body := e.doJSON(t, http.MethodGet, "/api/bots/"+botID+"/schedule-tasks/"+task.ID+"/runs", nil)
	if code != http.StatusOK {
		t.Fatalf("runs: %d %v", code, body)
	}
	raw, _ := json.Marshal(body["data"])
	for _, key := range []string{
		`"task_id"`, `"status":"skipped"`, `"summary"`, `"silent"`,
		`"scheduled_for"`, `"started_at"`, `"finished_at"`, `"duration_ms"`, `"total_tokens":15`,
	} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("运行记录投影缺 %s: %s", key, raw)
		}
	}
	// 主线 /runs 列表不返回 schedule run。
	code, body = e.doJSON(t, http.MethodGet, "/api/bots/"+botID+"/runs?limit=100", nil)
	if code != http.StatusOK {
		t.Fatalf("main runs: %d", code)
	}
	items, _ := body["data"].(map[string]interface{})["items"].([]interface{})
	if len(items) != 0 {
		t.Fatalf("主线 runs 不应包含 schedule run: %v", body)
	}
}

// 定时任务小状态（INT-123）REST：任务投影带 state/state_updated_at；DELETE
// .../state 是用户对该状态唯一的操作，幂等、归属校验、返回任务投影。
func TestBotScheduleRESTStateProjectionAndClear(t *testing.T) {
	e := setupE2E(t)
	botID := e.createBot(t)
	task := seedScheduleTaskRow(t, e, botID)
	updated := time.Now().UTC().Add(-time.Minute)
	if err := e.env.db.Model(&model.BotScheduleTask{}).Where("id = ?", task.ID).
		Updates(map[string]interface{}{
			"state_json":       `{"last_id":"abc-9"}`,
			"state_updated_at": updated,
		}).Error; err != nil {
		t.Fatalf("seed state: %v", err)
	}

	// 列表与详情投影带 state（原始 JSON 文本）与 state_updated_at（RFC3339）。
	code, body := e.doJSON(t, http.MethodGet, "/api/bots/"+botID+"/schedule-tasks", nil)
	if code != http.StatusOK {
		t.Fatalf("list: %d %v", code, body)
	}
	item := envelopeItems(body)[0].(map[string]interface{})
	if item["state"] != `{"last_id":"abc-9"}` {
		t.Fatalf("列表投影应带状态原文: %v", item)
	}
	if _, ok := item["state_updated_at"].(string); !ok {
		t.Fatalf("列表投影应带 state_updated_at: %v", item)
	}
	code, body = e.doJSON(t, http.MethodGet, "/api/bots/"+botID+"/schedule-tasks/"+task.ID, nil)
	if code != http.StatusOK {
		t.Fatalf("get: %d %v", code, body)
	}
	if data := envelopeData(body); data["state"] != `{"last_id":"abc-9"}` {
		t.Fatalf("详情投影应带状态原文: %v", data)
	}

	// DELETE .../state：清空并返回清空后的任务投影。
	code, body = e.doJSON(t, http.MethodDelete, "/api/bots/"+botID+"/schedule-tasks/"+task.ID+"/state", nil)
	if code != http.StatusOK {
		t.Fatalf("delete state: %d %v", code, body)
	}
	data := envelopeData(body)
	if data["id"] != task.ID {
		t.Fatalf("应返回任务投影: %v", data)
	}
	if data["state"] != nil {
		t.Fatalf("清空后 state 应为 null: %v", data["state"])
	}
	if data["state_updated_at"] != nil {
		t.Fatalf("清空后 state_updated_at 应为 null: %v", data["state_updated_at"])
	}
	var row model.BotScheduleTask
	if err := e.env.db.Where("id = ?", task.ID).First(&row).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if row.StateJSON != "" || row.StateUpdatedAt != nil {
		t.Fatalf("库里应已清空: %+v", row)
	}

	// 幂等：无状态时再次清空同样 200。
	code, body = e.doJSON(t, http.MethodDelete, "/api/bots/"+botID+"/schedule-tasks/"+task.ID+"/state", nil)
	if code != http.StatusOK {
		t.Fatalf("repeat delete: %d %v", code, body)
	}
	if data := envelopeData(body); data["state"] != nil {
		t.Fatalf("重复清空后 state 仍应为 null: %v", data)
	}

	// 不存在的任务 → 40429。
	code, body = e.doJSON(t, http.MethodDelete, "/api/bots/"+botID+"/schedule-tasks/"+uuid.NewString()+"/state", nil)
	if code != http.StatusNotFound {
		t.Fatalf("missing task: %d %v", code, body)
	}
	if got, _ := body["code"].(float64); got != 40429 {
		t.Fatalf("missing task 错误码应为 40429: %v", body)
	}

	// 归属校验：他人机器人一律 404（机器人不存在）。公开注册关闭后（INT-129）
	// 第二个用户凭邀请注册。
	other := registerAndLoginInvitedRouterUser(t, e.env.router, e.auth.Token, "bote2e-other", "bote2e-other@example.com", "Password123")
	jwt := e.jwt
	e.jwt = "Bearer " + other.Token
	code, body = e.doJSON(t, http.MethodDelete, "/api/bots/"+botID+"/schedule-tasks/"+task.ID+"/state", nil)
	e.jwt = jwt
	if code != http.StatusNotFound {
		t.Fatalf("他人机器人应 404: %d %v", code, body)
	}
	if got, _ := body["code"].(float64); got != 40424 {
		t.Fatalf("他人机器人错误码应为 40424: %v", body)
	}
}
