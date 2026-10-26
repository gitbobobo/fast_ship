package router

import (
	"net/http"
	"testing"

	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
)

// 预算 REST（INT-164）：机器人与定时任务各一组 GET / PUT / DELETE。只影响
// 告警，不改变运行行为；归属校验与既有机器人端点同语义（他人资源 40424、
// 任务不属于该机器人 40429），参数非法 40001，未认证 401。

// setupBotBudgetREST 建库并补齐预算表（预算表接线前不由 BotModels 迁移）。
func setupBotBudgetREST(t *testing.T) *e2eEnv {
	t.Helper()
	e := setupE2E(t)
	if err := e.env.db.AutoMigrate(repository.BotBudgetModels()...); err != nil {
		t.Fatalf("migrate bot budget: %v", err)
	}
	for _, ddl := range repository.BotBudgetDDL {
		if err := e.env.db.Exec(ddl).Error; err != nil {
			t.Fatalf("exec bot budget ddl: %v", err)
		}
	}
	return e
}

func TestBotBudgetRESTLifecycle(t *testing.T) {
	e := setupBotBudgetREST(t)
	botID := e.createBot(t)
	base := "/api/bots/" + botID + "/budgets"

	// PUT daily：阈值规范化去重升序，返回当前周期花费。
	code, body := e.doJSON(t, http.MethodPut, base+"/daily", map[string]interface{}{
		"amount_usd": 1.0, "thresholds": []float64{1, 0.8, 0.8},
	})
	if code != http.StatusOK {
		t.Fatalf("put daily: %d %v", code, body)
	}
	data := envelopeData(body)
	if data["period"] != "daily" {
		t.Fatalf("period 应为 daily: %v", data)
	}
	if amt, _ := data["amount_usd"].(float64); amt != 1 {
		t.Fatalf("amount_usd 应为 1: %v", data)
	}
	thresholds, _ := data["thresholds"].([]interface{})
	if len(thresholds) != 2 || thresholds[0].(float64) != 0.8 || thresholds[1].(float64) != 1 {
		t.Fatalf("thresholds 应规范化去重升序为 [0.8,1]: %v", thresholds)
	}
	if spend, ok := data["spend_usd"].(float64); !ok || spend != 0 {
		t.Fatalf("无花费时 spend_usd 应为 0: %v", data)
	}
	if key, _ := data["period_key"].(string); key == "" {
		t.Fatalf("应返回当前周期键: %v", data)
	}
	for _, key := range []string{"id", "spend_window_start", "spend_window_end", "created_at", "updated_at"} {
		if _, ok := data[key]; !ok {
			t.Fatalf("响应缺字段 %s: %v", key, data)
		}
	}

	// 同周期再 PUT 覆盖：仍只有一条，金额更新。
	code, body = e.doJSON(t, http.MethodPut, base+"/daily", map[string]interface{}{
		"amount_usd": 2.5, "thresholds": []float64{0.5},
	})
	if code != http.StatusOK {
		t.Fatalf("put daily again: %d %v", code, body)
	}
	if amt, _ := envelopeData(body)["amount_usd"].(float64); amt != 2.5 {
		t.Fatalf("覆盖后金额应为 2.5: %v", body)
	}

	// PUT monthly：列表两条。
	code, body = e.doJSON(t, http.MethodPut, base+"/monthly", map[string]interface{}{
		"amount_usd": 10, "thresholds": []float64{0.9},
	})
	if code != http.StatusOK {
		t.Fatalf("put monthly: %d %v", code, body)
	}
	code, body = e.doJSON(t, http.MethodGet, base, nil)
	if code != http.StatusOK {
		t.Fatalf("list: %d %v", code, body)
	}
	items := envelopeItems(body)
	if len(items) != 2 {
		t.Fatalf("两个周期应各一条预算: %v", items)
	}

	// DELETE daily：幂等，删完列表只剩 monthly。
	code, body = e.doJSON(t, http.MethodDelete, base+"/daily", nil)
	if code != http.StatusOK {
		t.Fatalf("delete daily: %d %v", code, body)
	}
	code, body = e.doJSON(t, http.MethodDelete, base+"/daily", nil)
	if code != http.StatusOK {
		t.Fatalf("重复 delete 应幂等成功: %d %v", code, body)
	}
	code, body = e.doJSON(t, http.MethodGet, base, nil)
	if code != http.StatusOK {
		t.Fatalf("list after delete: %d %v", code, body)
	}
	items = envelopeItems(body)
	if len(items) != 1 || items[0].(map[string]interface{})["period"] != "monthly" {
		t.Fatalf("删除后只应剩 monthly: %v", items)
	}

	// 非法周期 / 金额 / 阈值 / 未知字段 → 40001。
	for _, tc := range []struct {
		name string
		path string
		body map[string]interface{}
	}{
		{"period", base + "/weekly", map[string]interface{}{"amount_usd": 1, "thresholds": []float64{0.8}}},
		{"amount", base + "/daily", map[string]interface{}{"amount_usd": 0, "thresholds": []float64{0.8}}},
		{"thresholds-empty", base + "/daily", map[string]interface{}{"amount_usd": 1, "thresholds": []float64{}}},
		{"thresholds-range", base + "/daily", map[string]interface{}{"amount_usd": 1, "thresholds": []float64{0}}},
		{"unknown-field", base + "/daily", map[string]interface{}{"amount_usd": 1, "thresholds": []float64{0.8}, "extra": true}},
	} {
		code, body = e.doJSON(t, http.MethodPut, tc.path, tc.body)
		if code != http.StatusBadRequest {
			t.Fatalf("%s 非法应 400，实得 %d %v", tc.name, code, body)
		}
		if got, _ := body["code"].(float64); got != 40001 {
			t.Fatalf("%s 错误码应为 40001: %v", tc.name, body)
		}
	}

	// 未认证 401。
	if code, _ := e.doJSONAs(t, "", http.MethodGet, base, nil); code != http.StatusUnauthorized {
		t.Fatalf("未认证应为 401，实得 %d", code)
	}
}

func TestBotBudgetRESTTaskScope(t *testing.T) {
	e := setupBotBudgetREST(t)
	botID := e.createBot(t)
	task := seedScheduleTaskRow(t, e, botID)
	base := "/api/bots/" + botID + "/schedule-tasks/" + task.ID + "/budgets"

	code, body := e.doJSON(t, http.MethodGet, base, nil)
	if code != http.StatusOK {
		t.Fatalf("list task budgets: %d %v", code, body)
	}
	if items := envelopeItems(body); len(items) != 0 {
		t.Fatalf("未设置时任务预算列表应为空: %v", items)
	}

	code, body = e.doJSON(t, http.MethodPut, base+"/daily", map[string]interface{}{
		"amount_usd": 1, "thresholds": []float64{0.8},
	})
	if code != http.StatusOK {
		t.Fatalf("put task budget: %d %v", code, body)
	}
	if got := envelopeData(body)["period"]; got != "daily" {
		t.Fatalf("period 应为 daily: %v", body)
	}

	code, body = e.doJSON(t, http.MethodGet, base, nil)
	if code != http.StatusOK {
		t.Fatalf("list after put: %d %v", code, body)
	}
	if items := envelopeItems(body); len(items) != 1 {
		t.Fatalf("应有 1 条任务预算: %v", items)
	}

	code, body = e.doJSON(t, http.MethodDelete, base+"/daily", nil)
	if code != http.StatusOK {
		t.Fatalf("delete task budget: %d %v", code, body)
	}
	code, body = e.doJSON(t, http.MethodGet, base, nil)
	if code != http.StatusOK {
		t.Fatalf("list after delete: %d %v", code, body)
	}
	if items := envelopeItems(body); len(items) != 0 {
		t.Fatalf("删除后应为空: %v", items)
	}

	// 任务不属于该机器人 → 40429；不存在的任务同样 40429。
	otherBotID := e.createBot(t)
	code, body = e.doJSON(t, http.MethodPut,
		"/api/bots/"+otherBotID+"/schedule-tasks/"+task.ID+"/budgets/daily",
		map[string]interface{}{"amount_usd": 1, "thresholds": []float64{0.8}})
	if code != http.StatusNotFound {
		t.Fatalf("任务不属于该机器人应 404，实得 %d %v", code, body)
	}
	if got, _ := body["code"].(float64); got != 40429 {
		t.Fatalf("错误码应为 40429: %v", body)
	}
	code, body = e.doJSON(t, http.MethodGet,
		"/api/bots/"+botID+"/schedule-tasks/"+uuid.NewString()+"/budgets", nil)
	if code != http.StatusNotFound {
		t.Fatalf("不存在的任务应 404，实得 %d %v", code, body)
	}
}

func TestBotBudgetRESTOwnership(t *testing.T) {
	e := setupBotBudgetREST(t)
	botID := e.createBot(t)
	task := seedScheduleTaskRow(t, e, botID)

	other := registerAndLoginInvitedRouterUser(t, e.env.router, e.auth.Token,
		"budgetother", "budgetother@example.com", "Password123")
	otherJWT := "Bearer " + other.Token

	cases := []struct {
		name   string
		method string
		path   string
		body   map[string]interface{}
	}{
		{"list-bot", http.MethodGet, "/api/bots/" + botID + "/budgets", nil},
		{"put-bot", http.MethodPut, "/api/bots/" + botID + "/budgets/daily",
			map[string]interface{}{"amount_usd": 1, "thresholds": []float64{0.8}}},
		{"delete-bot", http.MethodDelete, "/api/bots/" + botID + "/budgets/daily", nil},
		{"list-task", http.MethodGet, "/api/bots/" + botID + "/schedule-tasks/" + task.ID + "/budgets", nil},
		{"put-task", http.MethodPut, "/api/bots/" + botID + "/schedule-tasks/" + task.ID + "/budgets/daily",
			map[string]interface{}{"amount_usd": 1, "thresholds": []float64{0.8}}},
		{"delete-task", http.MethodDelete, "/api/bots/" + botID + "/schedule-tasks/" + task.ID + "/budgets/daily", nil},
	}
	for _, tc := range cases {
		code, body := e.doJSONAs(t, otherJWT, tc.method, tc.path, tc.body)
		if code != http.StatusNotFound {
			t.Fatalf("%s 他人资源应 404，实得 %d %v", tc.name, code, body)
		}
		if got, _ := body["code"].(float64); got != 40424 {
			t.Fatalf("%s 错误码应为 40424: %v", tc.name, body)
		}
	}

	// 本人资源未被他人操作污染。
	code, body := e.doJSON(t, http.MethodGet, "/api/bots/"+botID+"/budgets", nil)
	if code != http.StatusOK {
		t.Fatalf("owner list: %d %v", code, body)
	}
	if items := envelopeItems(body); len(items) != 0 {
		t.Fatalf("他人操作不应写入预算: %v", items)
	}
}
