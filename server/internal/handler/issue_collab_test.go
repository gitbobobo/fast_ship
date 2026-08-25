package handler

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/model"
)

func collabParams(pairs ...gin.Param) gin.Params {
	return gin.Params(pairs)
}

type collabAreaJSON struct {
	Consensus *struct {
		Body   string `json:"body"`
		Author struct {
			Kind string `json:"kind"`
		} `json:"author"`
	} `json:"consensus"`
	Summary *struct {
		Body   string `json:"body"`
		Author struct {
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

	consensusCtx, consensusRec := newJSONContext(http.MethodPut, "/api/issues/"+issue.ID+"/collab/consensus", []byte(`{"body":"达成共识"}`))
	consensusCtx.Params = collabParams(gin.Param{Key: "iid", Value: issue.ID})
	asApiKey(consensusCtx)
	env.collabHandler.UpsertForKind(model.CollabDocumentKindConsensus)(consensusCtx)
	if consensusRec.Code != http.StatusOK {
		t.Fatalf("upsert consensus expected 200, got %d: %s", consensusRec.Code, consensusRec.Body.String())
	}

	summaryCtx, summaryRec := newJSONContext(http.MethodPut, "/api/issues/"+issue.ID+"/collab/summary", []byte(`{"body":"已新增顶部按钮","commit_ids":["abc1234"]}`))
	summaryCtx.Params = collabParams(gin.Param{Key: "iid", Value: issue.ID})
	asApiKey(summaryCtx)
	env.collabHandler.UpsertForKind(model.CollabDocumentKindSummary)(summaryCtx)
	if summaryRec.Code != http.StatusOK {
		t.Fatalf("upsert summary expected 200, got %d: %s", summaryRec.Code, summaryRec.Body.String())
	}

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
	if area.Consensus == nil || area.Consensus.Body != "达成共识" || area.Consensus.Author.Kind != "agent" {
		t.Fatalf("unexpected consensus: %+v", area.Consensus)
	}
	if area.Summary == nil || area.Summary.Body != "已新增顶部按钮" || area.Summary.Author.Kind != "agent" {
		t.Fatalf("unexpected summary: %+v", area.Summary)
	}

	delConsensusCtx, delConsensusRec := newJSONContext(http.MethodDelete, "/api/issues/"+issue.ID+"/collab/consensus", nil)
	delConsensusCtx.Params = collabParams(gin.Param{Key: "iid", Value: issue.ID})
	delConsensusCtx.Set(middleware.ContextKeyUserID, user.ID)
	delConsensusCtx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)
	env.collabHandler.DeleteForKind(model.CollabDocumentKindConsensus)(delConsensusCtx)
	if delConsensusRec.Code != http.StatusOK {
		t.Fatalf("delete consensus expected 200, got %d: %s", delConsensusRec.Code, delConsensusRec.Body.String())
	}

	getAfterConsensusCtx, getAfterConsensusRec := newJSONContext(http.MethodGet, "/api/issues/"+issue.ID+"/collab", nil)
	getAfterConsensusCtx.Params = collabParams(gin.Param{Key: "iid", Value: issue.ID})
	getAfterConsensusCtx.Set(middleware.ContextKeyUserID, user.ID)
	getAfterConsensusCtx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)
	env.collabHandler.GetArea(getAfterConsensusCtx)
	if getAfterConsensusRec.Code != http.StatusOK {
		t.Fatalf("get area after delete consensus expected 200, got %d: %s", getAfterConsensusRec.Code, getAfterConsensusRec.Body.String())
	}
	var afterConsensusArea collabAreaJSON
	decodeEnvelope(t, getAfterConsensusRec, &afterConsensusArea)
	if afterConsensusArea.Consensus != nil {
		t.Fatalf("expected consensus removed, got %+v", afterConsensusArea.Consensus)
	}
	if afterConsensusArea.Summary == nil {
		t.Fatalf("expected summary intact after consensus delete, got %+v", afterConsensusArea)
	}

	clearCtx, clearRec := newJSONContext(http.MethodDelete, "/api/issues/"+issue.ID+"/collab", nil)
	clearCtx.Params = collabParams(gin.Param{Key: "iid", Value: issue.ID})
	asApiKey(clearCtx)
	env.collabHandler.ClearArea(clearCtx)
	if clearRec.Code != http.StatusOK {
		t.Fatalf("clear area expected 200, got %d: %s", clearRec.Code, clearRec.Body.String())
	}

	getAfterCtx, getAfterRec := newJSONContext(http.MethodGet, "/api/issues/"+issue.ID+"/collab", nil)
	getAfterCtx.Params = collabParams(gin.Param{Key: "iid", Value: issue.ID})
	getAfterCtx.Set(middleware.ContextKeyUserID, user.ID)
	getAfterCtx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)
	env.collabHandler.GetArea(getAfterCtx)
	if getAfterRec.Code != http.StatusOK {
		t.Fatalf("get area after clear expected 200, got %d: %s", getAfterRec.Code, getAfterRec.Body.String())
	}
	var emptyArea collabAreaJSON
	decodeEnvelope(t, getAfterRec, &emptyArea)
	if emptyArea.Consensus != nil || emptyArea.Summary != nil {
		t.Fatalf("expected empty area after clear, got %+v", emptyArea)
	}
}

func TestIssueCollabHandler_ErrorMapping(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "collab-user-2")
	project := createHandlerTestProject(t, env.db, user.ID)
	issue := createHandlerTestIssue(t, env.db, project.ID)

	ctx, rec := newJSONContext(http.MethodPut, "/api/issues/"+issue.ID+"/collab/consensus", []byte(`{"body":"   "}`))
	ctx.Params = collabParams(gin.Param{Key: "iid", Value: issue.ID})
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	ctx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeApiKey)
	env.collabHandler.UpsertForKind(model.CollabDocumentKindConsensus)(ctx)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty body, got %d: %s", rec.Code, rec.Body.String())
	}

	missCtx, missRec := newJSONContext(http.MethodGet, "/api/issues/does-not-exist/collab", nil)
	missCtx.Params = collabParams(gin.Param{Key: "iid", Value: "does-not-exist"})
	missCtx.Set(middleware.ContextKeyUserID, user.ID)
	missCtx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)
	env.collabHandler.GetArea(missCtx)
	if missRec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing issue, got %d: %s", missRec.Code, missRec.Body.String())
	}
}

func TestIssueCollabHandler_PutWritesRequireApiKey(t *testing.T) {
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
		{"consensus", "/api/issues/" + issue.ID + "/collab/consensus", []byte(`{"body":"x"}`), env.collabHandler.UpsertForKind(model.CollabDocumentKindConsensus)},
		{"summary", "/api/issues/" + issue.ID + "/collab/summary", []byte(`{"body":"x"}`), env.collabHandler.UpsertForKind(model.CollabDocumentKindSummary)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			jwtCtx, jwtRec := newJSONContext(http.MethodPut, tc.path, tc.body)
			jwtCtx.Params = collabParams(gin.Param{Key: "iid", Value: issue.ID})
			jwtCtx.Set(middleware.ContextKeyUserID, user.ID)
			jwtCtx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)
			tc.call(jwtCtx)
			if jwtRec.Code != http.StatusForbidden {
				t.Fatalf("expected 403 for JWT write %s, got %d: %s", tc.name, jwtRec.Code, jwtRec.Body.String())
			}

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

func TestIssueCollabHandler_DeletesAllowJWT(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "collab-user-4")
	project := createHandlerTestProject(t, env.db, user.ID)
	issue := createHandlerTestIssue(t, env.db, project.ID)

	asJWT := func(c *gin.Context) {
		c.Set(middleware.ContextKeyUserID, user.ID)
		c.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)
	}
	asApiKey := func(c *gin.Context) {
		c.Set(middleware.ContextKeyUserID, user.ID)
		c.Set(middleware.ContextKeyAuthType, middleware.AuthTypeApiKey)
	}

	cases := []struct {
		name string
		path string
		call func(*gin.Context)
	}{
		{"area", "/api/issues/" + issue.ID + "/collab", env.collabHandler.ClearArea},
		{"consensus", "/api/issues/" + issue.ID + "/collab/consensus", env.collabHandler.DeleteForKind(model.CollabDocumentKindConsensus)},
		{"summary", "/api/issues/" + issue.ID + "/collab/summary", env.collabHandler.DeleteForKind(model.CollabDocumentKindSummary)},
	}
	for _, tc := range cases {
		t.Run("jwt_"+tc.name, func(t *testing.T) {
			ctx, rec := newJSONContext(http.MethodDelete, tc.path, nil)
			ctx.Params = collabParams(gin.Param{Key: "iid", Value: issue.ID})
			asJWT(ctx)
			tc.call(ctx)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200 for JWT delete %s, got %d: %s", tc.name, rec.Code, rec.Body.String())
			}
		})
		t.Run("apikey_"+tc.name, func(t *testing.T) {
			ctx, rec := newJSONContext(http.MethodDelete, tc.path, nil)
			ctx.Params = collabParams(gin.Param{Key: "iid", Value: issue.ID})
			asApiKey(ctx)
			tc.call(ctx)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200 for API key delete %s, got %d: %s", tc.name, rec.Code, rec.Body.String())
			}
		})
	}
}
