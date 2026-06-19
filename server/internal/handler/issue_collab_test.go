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

type collabQuestionJSON struct {
	ID     string `json:"id"`
	Body   string `json:"body"`
	Author struct {
		Kind  string `json:"kind"`
		Login string `json:"login"`
	} `json:"author"`
	Answer *struct {
		Value  string `json:"value"`
		Author struct {
			Kind string `json:"kind"`
		} `json:"author"`
	} `json:"answer"`
}

type collabAreaJSON struct {
	Notes []struct {
		Body   string `json:"body"`
		Author struct {
			Kind  string `json:"kind"`
			Login string `json:"login"`
		} `json:"author"`
	} `json:"notes"`
	Questions []collabQuestionJSON `json:"questions"`
	Summary   *struct {
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

	// 代理（API Key）批量创建问题
	createBody := []byte(`{"items":[{"body":"按钮放哪里？","options":["顶部","侧边"]},{"body":"补充说明？"}]}`)
	ctx, rec := newJSONContext(http.MethodPost, "/api/issues/"+issue.ID+"/collab/questions", createBody)
	ctx.Params = collabParams(gin.Param{Key: "iid", Value: issue.ID})
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	ctx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeApiKey)
	env.collabHandler.CreateQuestions(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("create questions expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var created []collabQuestionJSON
	decodeEnvelope(t, rec, &created)
	if len(created) != 2 {
		t.Fatalf("expected 2 questions, got %d", len(created))
	}
	if created[0].Author.Kind != "agent" || created[0].Author.Login != "代理" {
		t.Fatalf("expected agent actor, got %+v", created[0].Author)
	}
	firstQuestionID := created[0].ID

	// 用户（JWT）作答
	answerCtx, answerRec := newJSONContext(http.MethodPut, "/api/issues/"+issue.ID+"/collab/questions/"+firstQuestionID+"/answer", []byte(`{"answer":"顶部"}`))
	answerCtx.Params = collabParams(
		gin.Param{Key: "iid", Value: issue.ID},
		gin.Param{Key: "qid", Value: firstQuestionID},
	)
	answerCtx.Set(middleware.ContextKeyUserID, user.ID)
	answerCtx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)
	env.collabHandler.AnswerQuestion(answerCtx)
	if answerRec.Code != http.StatusOK {
		t.Fatalf("answer expected 200, got %d: %s", answerRec.Code, answerRec.Body.String())
	}
	var answered collabQuestionJSON
	decodeEnvelope(t, answerRec, &answered)
	if answered.Answer == nil || answered.Answer.Value != "顶部" || answered.Answer.Author.Kind != "user" {
		t.Fatalf("unexpected answer: %+v", answered.Answer)
	}

	// 用户（JWT）补充背景
	noteCtx, noteRec := newJSONContext(http.MethodPost, "/api/issues/"+issue.ID+"/collab/notes", []byte(`{"body":"这个按钮主要给运营用"}`))
	noteCtx.Params = collabParams(gin.Param{Key: "iid", Value: issue.ID})
	noteCtx.Set(middleware.ContextKeyUserID, user.ID)
	noteCtx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)
	env.collabHandler.CreateNote(noteCtx)
	if noteRec.Code != http.StatusOK {
		t.Fatalf("create note expected 200, got %d: %s", noteRec.Code, noteRec.Body.String())
	}

	// 代理（API Key）写完成总结
	summaryBody := []byte(`{"body":"已新增顶部按钮","commit_ids":["abc1234"]}`)
	summaryCtx, summaryRec := newJSONContext(http.MethodPut, "/api/issues/"+issue.ID+"/collab/summary", summaryBody)
	summaryCtx.Params = collabParams(gin.Param{Key: "iid", Value: issue.ID})
	summaryCtx.Set(middleware.ContextKeyUserID, user.ID)
	summaryCtx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeApiKey)
	env.collabHandler.UpsertSummary(summaryCtx)
	if summaryRec.Code != http.StatusOK {
		t.Fatalf("upsert summary expected 200, got %d: %s", summaryRec.Code, summaryRec.Body.String())
	}

	// GET 区域：三块齐全
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
	if len(area.Notes) != 1 || area.Notes[0].Author.Kind != "user" {
		t.Fatalf("unexpected notes: %+v", area.Notes)
	}
	if len(area.Questions) != 2 || area.Questions[0].Answer == nil {
		t.Fatalf("unexpected questions: %+v", area.Questions)
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

	// 非法 body → 400
	ctx, rec := newJSONContext(http.MethodPost, "/api/issues/"+issue.ID+"/collab/notes", []byte(`{"body":"   "}`))
	ctx.Params = collabParams(gin.Param{Key: "iid", Value: issue.ID})
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	ctx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)
	env.collabHandler.CreateNote(ctx)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty body, got %d: %s", rec.Code, rec.Body.String())
	}

	// 不存在的 note → 404
	updCtx, updRec := newJSONContext(http.MethodPut, "/api/issues/"+issue.ID+"/collab/notes/missing", []byte(`{"body":"x"}`))
	updCtx.Params = collabParams(
		gin.Param{Key: "iid", Value: issue.ID},
		gin.Param{Key: "nid", Value: "missing"},
	)
	updCtx.Set(middleware.ContextKeyUserID, user.ID)
	updCtx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)
	env.collabHandler.UpdateNote(updCtx)
	if updRec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing note, got %d: %s", updRec.Code, updRec.Body.String())
	}

	// 不存在的问题 → 404
	missCtx, missRec := newJSONContext(http.MethodGet, "/api/issues/does-not-exist/collab", nil)
	missCtx.Params = collabParams(gin.Param{Key: "iid", Value: "does-not-exist"})
	missCtx.Set(middleware.ContextKeyUserID, user.ID)
	missCtx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)
	env.collabHandler.GetArea(missCtx)
	if missRec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing issue, got %d: %s", missRec.Code, missRec.Body.String())
	}
}

func TestIssueCollabHandler_UserActionsRequireJWT(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "collab-user-3")
	project := createHandlerTestProject(t, env.db, user.ID)
	issue := createHandlerTestIssue(t, env.db, project.ID)

	// 代理（API Key）创建一个问题
	createCtx, createRec := newJSONContext(http.MethodPost, "/api/issues/"+issue.ID+"/collab/questions", []byte(`{"items":[{"body":"Q","options":[]}]}`))
	createCtx.Params = collabParams(gin.Param{Key: "iid", Value: issue.ID})
	createCtx.Set(middleware.ContextKeyUserID, user.ID)
	createCtx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeApiKey)
	env.collabHandler.CreateQuestions(createCtx)
	if createRec.Code != http.StatusOK {
		t.Fatalf("create questions expected 200, got %d: %s", createRec.Code, createRec.Body.String())
	}
	var created []collabQuestionJSON
	decodeEnvelope(t, createRec, &created)
	questionID := created[0].ID

	// 代理（API Key）作答 → 403（作答仅限用户/JWT）
	answerCtx, answerRec := newJSONContext(http.MethodPut, "/api/issues/"+issue.ID+"/collab/questions/"+questionID+"/answer", []byte(`{"answer":"x"}`))
	answerCtx.Params = collabParams(
		gin.Param{Key: "iid", Value: issue.ID},
		gin.Param{Key: "qid", Value: questionID},
	)
	answerCtx.Set(middleware.ContextKeyUserID, user.ID)
	answerCtx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeApiKey)
	env.collabHandler.AnswerQuestion(answerCtx)
	if answerRec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when API key answers, got %d: %s", answerRec.Code, answerRec.Body.String())
	}

	// 代理（API Key）补背景 → 403
	noteCtx, noteRec := newJSONContext(http.MethodPost, "/api/issues/"+issue.ID+"/collab/notes", []byte(`{"body":"背景"}`))
	noteCtx.Params = collabParams(gin.Param{Key: "iid", Value: issue.ID})
	noteCtx.Set(middleware.ContextKeyUserID, user.ID)
	noteCtx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeApiKey)
	env.collabHandler.CreateNote(noteCtx)
	if noteRec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when API key creates note, got %d: %s", noteRec.Code, noteRec.Body.String())
	}
}
