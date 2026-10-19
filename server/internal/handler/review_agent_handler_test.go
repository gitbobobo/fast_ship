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
	"github.com/godbobo/fast_ship/server/internal/service"
	"go.uber.org/zap"
)

// stubRunTokenResolver 运行令牌解析桩：越过鉴权专注请求体上限。
type stubRunTokenResolver struct{}

func (stubRunTokenResolver) ResolveRunToken(context.Context, string, bool) (*service.RunTokenIdentity, error) {
	return &service.RunTokenIdentity{UserID: "u1", RunID: "r1"}, nil
}

// TestReviewAgentSubmitResultBodyLimit M1：/result 请求体超 2MB → 40004，
// 不进入 service（nil 依赖的 service 不会被触到）。
func TestReviewAgentSubmitResultBodyLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	review := service.NewReviewService(nil, nil, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, zap.NewNop())
	r := gin.New()
	r.POST("/api/review-agent/result",
		middleware.RequireRunToken(stubRunTokenResolver{}, false),
		NewReviewAgentHandler(review).SubmitResult)

	req := httptest.NewRequest(http.MethodPost, "/api/review-agent/result",
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
