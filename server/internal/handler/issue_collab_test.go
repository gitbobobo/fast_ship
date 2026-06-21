package handler

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/middleware"
)

func collabParams(pairs ...gin.Param) gin.Params {
	return gin.Params(pairs)
}

type collabAreaJSON struct {
	Suggestions []struct {
		Body      string `json:"body"`
		SortOrder int    `json:"sort_order"`
		Author    struct {
			Kind  string `json:"kind"`
			Login string `json:"login"`
		} `json:"author"`
	} `json:"suggestions"`
	Plan *struct {
		Body   string `json:"body"`
		Author struct {
			Kind string `json:"kind"`
		} `json:"author"`
	} `json:"plan"`
	Review *struct {
		Body   string `json:"body"`
		Author struct {
			Kind string `json:"kind"`
		} `json:"author"`
	} `json:"review"`
	Summary *struct {
		Body      string   `json:"body"`
		CommitIDs []string `json:"commit_ids"`
		Author    struct {
			Kind string `json:"kind"`
		} `json:"author"`
	} `json:"summary"`
}

func TestIssueCollabHandler_FullFlow(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "collab-user")
	project := createHandlerTestProject(t, env.db, user.ID)
	issue := createHandlerTestIssue(t, env.db, project.ID)

	asApiKey := func(c *gin.Context) {
		c.Set(middleware.ContextKeyUserID, user.ID)
		c.Set(middleware.ContextKeyAuthType, middleware.AuthTypeApiKey)
	}

	// 代理（API Key）写实施建议
	sugCtx, sugRec := newJSONContext(http.MethodPut, "/api/issues/"+issue.ID+"/collab/suggestions", []byte(`{"items":[{"body":"建议一"},{"body":"建议二"}]}`))
	sugCtx.Params = collabParams(gin.Param{Key: "iid", Value: issue.ID})
	asApiKey(sugCtx)
	env.collabHandler.ReplaceSuggestions(sugCtx)
	if sugRec.Code != http.StatusOK {
		t.Fatalf("replace suggestions expected 200, got %d: %s", sugRec.Code, sugRec.Body.String())
	}

	// 代理（API Key）写计划
	planCtx, planRec := newJSONContext(http.MethodPut, "/api/issues/"+issue.ID+"/collab/plan", []byte(`{"body":"详细执行计划"}`))
	planCtx.Params = collabParams(gin.Param{Key: "iid", Value: issue.ID})
	asApiKey(planCtx)
	env.collabHandler.UpsertPlan(planCtx)
	if planRec.Code != http.StatusOK {
		t.Fatalf("upsert plan expected 200, got %d: %s", planRec.Code, planRec.Body.String())
	}

	// 代理（API Key）写审查结果
	reviewCtx, reviewRec := newJSONContext(http.MethodPut, "/api/issues/"+issue.ID+"/collab/review", []byte(`{"body":"审查通过"}`))
	reviewCtx.Params = collabParams(gin.Param{Key: "iid", Value: issue.ID})
	asApiKey(reviewCtx)
	env.collabHandler.UpsertReview(reviewCtx)
	if reviewRec.Code != http.StatusOK {
		t.Fatalf("upsert review expected 200, got %d: %s", reviewRec.Code, reviewRec.Body.String())
	}

	// 代理（API Key）写完成总结
	summaryCtx, summaryRec := newJSONContext(http.MethodPut, "/api/issues/"+issue.ID+"/collab/summary", []byte(`{"body":"已新增顶部按钮","commit_ids":["abc1234"]}`))
	summaryCtx.Params = collabParams(gin.Param{Key: "iid", Value: issue.ID})
	asApiKey(summaryCtx)
	env.collabHandler.UpsertSummary(summaryCtx)
	if summaryRec.Code != http.StatusOK {
		t.Fatalf("upsert summary expected 200, got %d: %s", summaryRec.Code, summaryRec.Body.String())
	}

	// GET 区域（JWT 可读）：四块齐全
	getCtx, getRec := newJSONContext(http.MethodGet, "/api/issues/"+issue.ID+"/collab", nil)
	getCtx.Params = collabParams(gin.Param{Key: "iid", Value: issue.ID})
	getCtx.Set(middleware.ContextKeyUserID, user.ID)
	getCtx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)
	env.collabHandler.GetArea(getCtx)
	if getRec.Code != http.StatusOK {
		t.Fatalf("get area expected 200, got %d: %s", getRec.Code, getRec.Body.String())
	}
	var area collabAreaJSON
	decodeEnvelope(t, getRec, &area)
	if len(area.Suggestions) != 2 || area.Suggestions[0].Author.Kind != "agent" {
		t.Fatalf("unexpected suggestions: %+v", area.Suggestions)
	}
	if area.Plan == nil || area.Plan.Body != "详细执行计划" {
		t.Fatalf("unexpected plan: %+v", area.Plan)
	}
	if area.Review == nil || area.Review.Body != "审查通过" {
		t.Fatalf("unexpected review: %+v", area.Review)
	}
	if area.Summary == nil || len(area.Summary.CommitIDs) != 1 || area.Summary.Author.Kind != "agent" {
		t.Fatalf("unexpected summary: %+v", area.Summary)
	}
}

func TestIssueCollabHandler_ErrorMapping(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "collab-user-2")
	project := createHandlerTestProject(t, env.db, user.ID)
	issue := createHandlerTestIssue(t, env.db, project.ID)

	// 代理写空 body 计划 → 400
	ctx, rec := newJSONContext(http.MethodPut, "/api/issues/"+issue.ID+"/collab/plan", []byte(`{"body":"   "}`))
	ctx.Params = collabParams(gin.Param{Key: "iid", Value: issue.ID})
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	ctx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeApiKey)
	env.collabHandler.UpsertPlan(ctx)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty body, got %d: %s", rec.Code, rec.Body.String())
	}

	// 不存在的 issue → 404
	missCtx, missRec := newJSONContext(http.MethodGet, "/api/issues/does-not-exist/collab", nil)
	missCtx.Params = collabParams(gin.Param{Key: "iid", Value: "does-not-exist"})
	missCtx.Set(middleware.ContextKeyUserID, user.ID)
	missCtx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)
	env.collabHandler.GetArea(missCtx)
	if missRec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing issue, got %d: %s", missRec.Code, missRec.Body.String())
	}
}

// JWT 调用四个写端点均 403(40303)；API Key 调用均 200。
func TestIssueCollabHandler_WritesRequireApiKey(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "collab-user-3")
	project := createHandlerTestProject(t, env.db, user.ID)
	issue := createHandlerTestIssue(t, env.db, project.ID)

	cases := []struct {
		name string
		path string
		body []byte
		call func(*gin.Context)
	}{
		{"suggestions", "/api/issues/" + issue.ID + "/collab/suggestions", []byte(`{"items":[]}`), env.collabHandler.ReplaceSuggestions},
		{"plan", "/api/issues/" + issue.ID + "/collab/plan", []byte(`{"body":"x"}`), env.collabHandler.UpsertPlan},
		{"review", "/api/issues/" + issue.ID + "/collab/review", []byte(`{"body":"x"}`), env.collabHandler.UpsertReview},
		{"summary", "/api/issues/" + issue.ID + "/collab/summary", []byte(`{"body":"x","commit_ids":[]}`), env.collabHandler.UpsertSummary},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// JWT 调用 → 403
			jwtCtx, jwtRec := newJSONContext(http.MethodPut, tc.path, tc.body)
			jwtCtx.Params = collabParams(gin.Param{Key: "iid", Value: issue.ID})
			jwtCtx.Set(middleware.ContextKeyUserID, user.ID)
			jwtCtx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)
			tc.call(jwtCtx)
			if jwtRec.Code != http.StatusForbidden {
				t.Fatalf("expected 403 for JWT write %s, got %d: %s", tc.name, jwtRec.Code, jwtRec.Body.String())
			}

			// API Key 调用 → 200
			apiCtx, apiRec := newJSONContext(http.MethodPut, tc.path, tc.body)
			apiCtx.Params = collabParams(gin.Param{Key: "iid", Value: issue.ID})
			apiCtx.Set(middleware.ContextKeyUserID, user.ID)
			apiCtx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeApiKey)
			tc.call(apiCtx)
			if apiRec.Code != http.StatusOK {
				t.Fatalf("expected 200 for API key write %s, got %d: %s", tc.name, apiRec.Code, apiRec.Body.String())
			}
		})
	}
}
