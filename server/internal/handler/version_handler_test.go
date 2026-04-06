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
		p.GithubTokenEncrypted = []byte{}
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
