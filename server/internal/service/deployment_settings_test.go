package service

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func setupDeploymentSettingsTest(t *testing.T, cfgPublicURL string) (*DeploymentSettingsService, *gorm.DB) {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared&_pragma=foreign_keys(1)"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&model.DeploymentSettings{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	cfg := &config.Config{Server: config.ServerConfig{PublicURL: cfgPublicURL}}
	return NewDeploymentSettingsService(repository.NewDeploymentSettingsRepository(db), cfg), db
}

// PublicURL 解析优先级：数据库非空值 > config > 空串；读侧 trim 去尾部 /。
func TestPublicURLPriority(t *testing.T) {
	ctx := context.Background()

	// 两处都没有 → 空串。
	svc, _ := setupDeploymentSettingsTest(t, "")
	if got := svc.PublicURL(ctx); got != "" {
		t.Fatalf("expected empty public_url, got %q", got)
	}

	// 仅 config：回落并归一（去尾部 /）。
	svc, _ = setupDeploymentSettingsTest(t, "https://fs.example.com/")
	if got := svc.PublicURL(ctx); got != "https://fs.example.com" {
		t.Fatalf("expected config fallback normalized, got %q", got)
	}

	// 数据库非空值优先于 config。
	svc, db := setupDeploymentSettingsTest(t, "https://cfg.example.com")
	if err := db.Create(&model.DeploymentSettings{
		ID: model.DeploymentSettingsID, PublicURL: "https://db.example.com/",
	}).Error; err != nil {
		t.Fatalf("seed settings row: %v", err)
	}
	if got := svc.PublicURL(ctx); got != "https://db.example.com" {
		t.Fatalf("expected database value to win, got %q", got)
	}

	// 数据库存空串 = 清空回落 config。
	svc, db = setupDeploymentSettingsTest(t, "https://cfg.example.com")
	if err := db.Create(&model.DeploymentSettings{
		ID: model.DeploymentSettingsID, PublicURL: "",
	}).Error; err != nil {
		t.Fatalf("seed settings row: %v", err)
	}
	if got := svc.PublicURL(ctx); got != "https://cfg.example.com" {
		t.Fatalf("expected config fallback for empty db value, got %q", got)
	}
}

// Update 校验规则：合法值去尾部 / 落库回读 source=database；空串清空回落；
// 非 http(s)、缺 host、带 query/fragment 一律 40001。
func TestDeploymentSettingsUpdateValidation(t *testing.T) {
	ctx := context.Background()
	svc, _ := setupDeploymentSettingsTest(t, "")

	// 合法：写入后回读，来源 database、末尾 / 已去掉。
	out, err := svc.Update(ctx, "https://fs.example.com/")
	if err != nil {
		t.Fatalf("update public_url: %v", err)
	}
	if out.PublicUrl != "https://fs.example.com" || string(out.PublicUrlSource) != "database" {
		t.Fatalf("unexpected response: %+v", out)
	}
	if out.UpdatedAt == nil {
		t.Fatalf("expected updated_at set after save")
	}

	// 空串：清空数据库值，回落 none（config 也没配）。
	out, err = svc.Update(ctx, "")
	if err != nil {
		t.Fatalf("clear public_url: %v", err)
	}
	if out.PublicUrl != "" || string(out.PublicUrlSource) != "none" {
		t.Fatalf("expected cleared settings falling back to none, got %+v", out)
	}

	// 空串 + config 兜底：来源应为 config。
	svcCfg, _ := setupDeploymentSettingsTest(t, "https://cfg.example.com")
	if _, err := svcCfg.Update(ctx, "https://tmp.example.com"); err != nil {
		t.Fatalf("update public_url: %v", err)
	}
	out, err = svcCfg.Update(ctx, "")
	if err != nil {
		t.Fatalf("clear public_url: %v", err)
	}
	if out.PublicUrl != "https://cfg.example.com" || string(out.PublicUrlSource) != "config" {
		t.Fatalf("expected config fallback after clear, got %+v", out)
	}

	for name, raw := range map[string]string{
		"ftp scheme":    "ftp://fs.example.com",
		"missing host":  "https:///path",
		"not a url":     "not a url",
		"with query":    "https://fs.example.com/?x=1",
		"with fragment": "https://fs.example.com/#anchor",
	} {
		if _, err := svc.Update(ctx, raw); err == nil {
			t.Fatalf("%s: expected 40001 for %q", name, raw)
		}
	}
}

// Get 投影：未落库时来源 none/config，落库后 database 且带 updated_at。
func TestDeploymentSettingsGetSources(t *testing.T) {
	ctx := context.Background()

	svc, _ := setupDeploymentSettingsTest(t, "")
	out, err := svc.Get(ctx)
	if err != nil {
		t.Fatalf("get settings: %v", err)
	}
	if out.PublicUrl != "" || string(out.PublicUrlSource) != "none" || out.UpdatedAt != nil {
		t.Fatalf("expected none source with no data, got %+v", out)
	}

	svcCfg, _ := setupDeploymentSettingsTest(t, "https://cfg.example.com")
	out, err = svcCfg.Get(ctx)
	if err != nil {
		t.Fatalf("get settings: %v", err)
	}
	if string(out.PublicUrlSource) != "config" || out.UpdatedAt != nil {
		t.Fatalf("expected config source without db row, got %+v", out)
	}

	if _, err := svcCfg.Update(ctx, "https://db.example.com"); err != nil {
		t.Fatalf("update public_url: %v", err)
	}
	out, err = svcCfg.Get(ctx)
	if err != nil {
		t.Fatalf("get settings: %v", err)
	}
	if out.PublicUrl != "https://db.example.com" || string(out.PublicUrlSource) != "database" || out.UpdatedAt == nil {
		t.Fatalf("expected database source after save, got %+v", out)
	}
}
