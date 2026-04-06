package service

import (
	"testing"

	"github.com/godbobo/fast_ship/server/internal/model"
)

func TestShipServiceCheck_ReturnsDetailedMissingItems(t *testing.T) {
	svc := setupTestServices(t)
	createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, "user-1", func(p *model.Project) {
		p.GithubOwner = ""
		p.GithubRepo = ""
		p.GithubTokenEncrypted = []byte{}
	})
	version := createTestVersion(t, svc.db, project.ID, func(v *model.Version) {
		v.ReleaseNotes = ""
		v.TargetCommitish = ""
	})

	check, err := svc.shipService.Check(version.ID, project.UserID)
	if err != nil {
		t.Fatalf("ship check: %v", err)
	}

	if check.CanShip {
		t.Fatalf("expected ship check to fail")
	}
	if len(check.Items) != 4 {
		t.Fatalf("expected 4 check items, got %d", len(check.Items))
	}

	assertCheckItem := func(key string, ok bool) {
		t.Helper()
		for _, item := range check.Items {
			if item.Key == key {
				if item.OK != ok {
					t.Fatalf("expected %s ok=%v, got %v", key, ok, item.OK)
				}
				return
			}
		}
		t.Fatalf("missing check item %s", key)
	}

	assertCheckItem("release_notes", false)
	assertCheckItem("artifacts", false)
	assertCheckItem("target_commitish", false)
	assertCheckItem("github_config", false)
}
