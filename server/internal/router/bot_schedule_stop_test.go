package router

import (
	"net/http"
	"testing"
)

// INT-137 定时任务运行停止端点的路由层鉴权与归属隔离。

func TestBotScheduleRunStopRequiresJWT(t *testing.T) {
	e := setupE2E(t)
	botID := e.createBot(t)
	code, body := e.doJSONAs(t, "", http.MethodPost,
		"/api/bots/"+botID+"/schedule-tasks/task-x/runs/run-x/stop", nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %v", code, body)
	}
}

func TestBotScheduleRunStopRejectsNonOwner(t *testing.T) {
	e := setupE2E(t)
	botID := e.createBot(t)
	// 公开注册关闭（INT-129）：第二个用户凭邀请注册。
	other := registerAndLoginInvitedRouterUser(t, e.env.router, e.auth.Token,
		"botstopother", "botstopother@example.com", "Password123")
	code, body := e.doJSONAs(t, "Bearer "+other.Token, http.MethodPost,
		"/api/bots/"+botID+"/schedule-tasks/task-x/runs/run-x/stop", nil)
	if code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %v", code, body)
	}
}
