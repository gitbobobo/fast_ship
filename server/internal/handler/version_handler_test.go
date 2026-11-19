package handler

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/model"
)

func TestVersionHandlerShipCheck_ReturnsValidationItems(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-1")
	project := createHandlerTestProject(t, env.db, user.ID, func(p *model.Project) {
		p.GithubOwner = ""
		p.GithubRepo = ""
		p.PublicGitHubTokenID = ""
		p.CodeGitHubTokenID = ""
	})
	version := createHandlerTestVersion(t, env.db, project.ID, func(v *model.Version) {
		v.ReleaseNotes = ""
		v.TargetCommitish = ""
	})

	ctx, rec := newJSONContext(http.MethodGet, "/api/versions/"+version.ID+"/ship-check", nil)
	ctx.Params = ginParams("vid", version.ID)
	ctx.Set(middleware.ContextKeyUserID, user.ID)

	env.versionHandler.ShipCheck(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var result struct {
		CanShip bool             `json:"can_ship"`
		Items   []map[string]any `json:"items"`
	}
	envelope := decodeEnvelope(t, rec, &result)
	if envelope.Code != 0 {
		t.Fatalf("expected success code, got %d", envelope.Code)
	}
	if result.CanShip {
		t.Fatalf("expected can_ship=false")
	}
	if len(result.Items) != 4 {
		t.Fatalf("expected 4 check items, got %d", len(result.Items))
	}
}

func TestVersionHandlerUpdate_RejectsVersionNumberChangeForAPIKey(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-1")
	project := createHandlerTestProject(t, env.db, user.ID)
	version := createHandlerTestVersion(t, env.db, project.ID)

	body := []byte(`{"version_number":"v1.0.1"}`)
	ctx, rec := newJSONContext(http.MethodPut, "/api/versions/"+version.ID, body)
	ctx.Params = ginParams("vid", version.ID)
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	ctx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeApiKey)

	env.versionHandler.Update(ctx)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestVersionHandlerUpdate_AllowsJWTVersionNumberChange(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-1")
	project := createHandlerTestProject(t, env.db, user.ID)
	version := createHandlerTestVersion(t, env.db, project.ID)

	body := []byte(`{"version_number":"v1.0.1"}`)
	ctx, rec := newJSONContext(http.MethodPut, "/api/versions/"+version.ID, body)
	ctx.Params = ginParams("vid", version.ID)
	ctx.Set(middleware.ContextKeyUserID, user.ID)
	ctx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)

	env.versionHandler.Update(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var updated model.Version
	decodeEnvelope(t, rec, &updated)
	if updated.VersionNumber != "v1.0.1" {
		t.Fatalf("expected version number to update, got %q", updated.VersionNumber)
	}
	if updated.TargetCommitish != "main" {
		t.Fatalf("expected target commitish to be preserved, got %q", updated.TargetCommitish)
	}
}

func ginParams(key, value string) []gin.Param {
	return []gin.Param{{Key: key, Value: value}}
}

// INT-241 回归：非所有者更新/删除他人版本，无论目标 pending 还是已发货，
// 都得到 404（40401），不会先看到 409 冲突码（40904）。
func TestVersionHandlerUpdate_NonOwnerGetsNotFoundNotConflict(t *testing.T) {
	for _, status := range []model.VersionStatus{model.VersionStatusPending, model.VersionStatusShipped} {
		env := setupHandlerTestEnv(t)
		owner := createHandlerTestUser(t, env.db, "user-1")
		intruder := createHandlerTestUser(t, env.db, "user-2")
		project := createHandlerTestProject(t, env.db, owner.ID)
		version := createHandlerTestVersion(t, env.db, project.ID, func(v *model.Version) {
			v.Status = status
		})

		body := []byte(`{"release_notes":"intruder notes"}`)
		ctx, rec := newJSONContext(http.MethodPut, "/api/versions/"+version.ID, body)
		ctx.Params = ginParams("vid", version.ID)
		ctx.Set(middleware.ContextKeyUserID, intruder.ID)
		ctx.Set(middleware.ContextKeyAuthType, middleware.AuthTypeJWT)

		env.versionHandler.Update(ctx)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("status %q: expected 404 for non-owner, got %d: %s", status, rec.Code, rec.Body.String())
		}
	}
}

func TestVersionHandlerDelete_NonOwnerGetsNotFoundNotConflict(t *testing.T) {
	for _, status := range []model.VersionStatus{model.VersionStatusPending, model.VersionStatusShipped} {
		env := setupHandlerTestEnv(t)
		owner := createHandlerTestUser(t, env.db, "user-1")
		intruder := createHandlerTestUser(t, env.db, "user-2")
		project := createHandlerTestProject(t, env.db, owner.ID)
		version := createHandlerTestVersion(t, env.db, project.ID, func(v *model.Version) {
			v.Status = status
		})

		ctx, rec := newJSONContext(http.MethodDelete, "/api/versions/"+version.ID, nil)
		ctx.Params = ginParams("vid", version.ID)
		ctx.Set(middleware.ContextKeyUserID, intruder.ID)

		env.versionHandler.Delete(ctx)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("status %q: expected 404 for non-owner, got %d: %s", status, rec.Code, rec.Body.String())
		}
	}
}

// INT-241 回归：所有者删除非 pending 版本仍返回 409（40904）。
func TestVersionHandlerDelete_OwnerNonPendingReturnsConflict(t *testing.T) {
	env := setupHandlerTestEnv(t)
	user := createHandlerTestUser(t, env.db, "user-1")
	project := createHandlerTestProject(t, env.db, user.ID)
	version := createHandlerTestVersion(t, env.db, project.ID, func(v *model.Version) {
		v.Status = model.VersionStatusShipped
	})

	ctx, rec := newJSONContext(http.MethodDelete, "/api/versions/"+version.ID, nil)
	ctx.Params = ginParams("vid", version.ID)
	ctx.Set(middleware.ContextKeyUserID, user.ID)

	env.versionHandler.Delete(ctx)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for owner deleting shipped version, got %d: %s", rec.Code, rec.Body.String())
	}
}
