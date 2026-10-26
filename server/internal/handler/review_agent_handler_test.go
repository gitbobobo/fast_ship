package handler

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/service"
	"go.uber.org/zap"
)

// stubRunTokenResolver 运行令牌解析桩：复刻 resolver 的绑定语义（URL 预期
// run 与令牌所属不一致 → 40307），越过真实鉴权专注路由与请求体上限。
type stubRunTokenResolver struct {
	runID    string
	threadID string
}

func (s stubRunTokenResolver) ResolveRunToken(_ context.Context, _ string, _ bool, expectedRunID string) (*service.RunTokenIdentity, error) {
	if expectedRunID != "" && expectedRunID != s.runID {
		return nil, errs.ErrReviewRunMismatch
	}
	return &service.RunTokenIdentity{UserID: "u1", RunID: s.runID, ThreadID: s.threadID}, nil
}

// TestReviewAgentRunBinding INT-169 运行归属强校验：URL runId 与令牌所属
// 运行不一致 → 40307，读/写端点都在 handler 之前拒绝；匹配则放行。
func TestReviewAgentRunBinding(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resolver := stubRunTokenResolver{runID: "run-a", threadID: "th-a"}
	reached := false
	r := gin.New()
	ok := func(c *gin.Context) { reached = true; c.Status(http.StatusOK) }
	r.GET("/api/review-agent/runs/:runId/pull-request",
		middleware.RequireRunToken(resolver, true, "runId"), ok)
	r.GET("/api/review-agent/runs/:runId/pull-request/diff",
		middleware.RequireRunToken(resolver, true, "runId"), ok)
	r.GET("/api/review-agent/runs/:runId/history",
		middleware.RequireRunToken(resolver, true, "runId"), ok)
	r.GET("/api/review-agent/runs/:runId/screenshots",
		middleware.RequireRunToken(resolver, true, "runId"), ok)
	r.GET("/api/review-agent/runs/:runId/screenshots/:assetId",
		middleware.RequireRunToken(resolver, true, "runId"), ok)
	r.POST("/api/review-agent/runs/:runId/result",
		middleware.RequireRunToken(resolver, false, "runId"), ok)

	// 令牌是 run-a 的，目标 URL 是 run-b：INT-169 的串用场景复现。同时复验
	// 旧的无 run 作用域端点已整体移除（404，不再隐式推断运行），以及
	// 双斜杠空 runId 不能绕过绑定（Gin 会让 :runId 匹配空段——空目标
	// 一律按错配拒绝，不给仅凭令牌的旁路入口）。
	for _, tc := range []struct {
		method, path string
		wantStatus   int
		wantCode     string
	}{
		{http.MethodGet, "/api/review-agent/runs/run-b/pull-request", http.StatusForbidden, "40307"},
		{http.MethodGet, "/api/review-agent/runs/run-b/pull-request/diff", http.StatusForbidden, "40307"},
		{http.MethodGet, "/api/review-agent/runs/run-b/history", http.StatusForbidden, "40307"},
		{http.MethodGet, "/api/review-agent/runs/run-b/screenshots", http.StatusForbidden, "40307"},
		{http.MethodGet, "/api/review-agent/runs/run-b/screenshots/asset-1", http.StatusForbidden, "40307"},
		{http.MethodPost, "/api/review-agent/runs/run-b/result", http.StatusForbidden, "40307"},
		{http.MethodGet, "/api/review-agent/runs//pull-request", http.StatusForbidden, "40307"},
		{http.MethodGet, "/api/review-agent/runs//pull-request/diff", http.StatusForbidden, "40307"},
		{http.MethodGet, "/api/review-agent/runs//history", http.StatusForbidden, "40307"},
		{http.MethodGet, "/api/review-agent/runs//screenshots", http.StatusForbidden, "40307"},
		{http.MethodGet, "/api/review-agent/runs//screenshots/asset-1", http.StatusForbidden, "40307"},
		{http.MethodPost, "/api/review-agent/runs//result", http.StatusForbidden, "40307"},
		{http.MethodGet, "/api/review-agent/pull-request", http.StatusNotFound, ""},
		{http.MethodGet, "/api/review-agent/history", http.StatusNotFound, ""},
		{http.MethodPost, "/api/review-agent/result", http.StatusNotFound, ""},
	} {
		var body *strings.Reader
		if tc.method == http.MethodPost {
			body = strings.NewReader("{}")
		} else {
			body = strings.NewReader("")
		}
		req := httptest.NewRequest(tc.method, tc.path, body)
		req.Header.Set("Authorization", "Bearer fsr_x")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != tc.wantStatus {
			t.Fatalf("%s %s = %d %s, want %d", tc.method, tc.path, w.Code, w.Body.String(), tc.wantStatus)
		}
		if tc.wantCode != "" && !strings.Contains(w.Body.String(), tc.wantCode) {
			t.Fatalf("%s %s = %s, want code %s", tc.method, tc.path, w.Body.String(), tc.wantCode)
		}
	}
	if reached {
		t.Fatal("mismatched request must not reach handler")
	}

	// runId 匹配 → 放行进入 handler。
	req := httptest.NewRequest(http.MethodGet, "/api/review-agent/runs/run-a/pull-request", nil)
	req.Header.Set("Authorization", "Bearer fsr_x")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !reached {
		t.Fatalf("matching request = %d reached=%v, want 200/true", w.Code, reached)
	}
}

// TestReviewAgentSubmitResultBodyLimit M1：/result 请求体超 2MB → 40004，
// 不进入 service（nil 依赖的 service 不会被触到）。
func TestReviewAgentSubmitResultBodyLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	review := service.NewReviewService(nil, nil, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, zap.NewNop())
	r := gin.New()
	r.POST("/api/review-agent/runs/:runId/result",
		middleware.RequireRunToken(stubRunTokenResolver{runID: "r1"}, false, "runId"),
		NewReviewAgentHandler(review).SubmitResult)

	req := httptest.NewRequest(http.MethodPost, "/api/review-agent/runs/r1/result",
		bytes.NewReader(make([]byte, reviewResultMaxBodyBytes+1)))
	req.Header.Set("Authorization", "Bearer fsr_test")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "40004") {
		t.Fatalf("body = %s, want code 40004", w.Body.String())
	}
}
