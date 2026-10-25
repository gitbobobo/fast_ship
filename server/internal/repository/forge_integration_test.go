package repository

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ForgeIntegrationRepository（INT-166）定向测试：CRUD 与被项目引用统计。

func forgeIntegrationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.ForgeIntegration{}, &model.Project{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestForgeIntegrationCRUD(t *testing.T) {
	db := forgeIntegrationTestDB(t)
	repo := NewForgeIntegrationRepository(db)

	row := &model.ForgeIntegration{
		ID:                  uuid.NewString(),
		Kind:                model.ForgeIntegrationKindForgejo,
		Name:                "主力 Forgejo",
		BaseURL:             "https://forge.example.com",
		APIBaseURL:          "https://forge.example.com/api/v1",
		AllowPrivateNetwork: false,
	}
	if err := repo.Create(row); err != nil {
		t.Fatalf("create: %v", err)
	}
	if row.CreatedAt.IsZero() || row.UpdatedAt.IsZero() {
		t.Fatal("timestamps should be filled on create")
	}

	got, err := repo.FindByID(row.ID)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if got.Name != row.Name || got.BaseURL != row.BaseURL {
		t.Fatalf("row mismatch: %+v", got)
	}
	if _, err := repo.FindByID("missing"); err != gorm.ErrRecordNotFound {
		t.Fatalf("missing id err = %v, want ErrRecordNotFound", err)
	}

	// List 按创建时间升序。
	second := &model.ForgeIntegration{ID: uuid.NewString(), Kind: model.ForgeIntegrationKindForgejo, Name: "second", BaseURL: "https://b.example.com"}
	if err := repo.Create(second); err != nil {
		t.Fatalf("create second: %v", err)
	}
	rows, err := repo.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 2 || rows[0].ID != row.ID || rows[1].ID != second.ID {
		t.Fatalf("list order = %+v", rows)
	}

	// UpdateColumns 只写给定列，且同步 updated_at。
	if err := repo.UpdateColumns(row.ID, map[string]any{"name": "改名"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ = repo.FindByID(row.ID)
	if got.Name != "改名" || got.BaseURL != row.BaseURL {
		t.Fatalf("after update = %+v", got)
	}
	if !got.UpdatedAt.After(row.UpdatedAt) && got.UpdatedAt.Equal(row.UpdatedAt) {
		t.Fatalf("updated_at should advance: %v -> %v", row.UpdatedAt, got.UpdatedAt)
	}
	if err := repo.UpdateColumns("missing", map[string]any{"name": "x"}); err != gorm.ErrRecordNotFound {
		t.Fatalf("update missing err = %v, want ErrRecordNotFound", err)
	}

	// 无引用行原子删除：1 行受影响，行消失。
	affected, err := repo.DeleteIfUnreferenced(second.ID)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if affected != 1 {
		t.Fatalf("unreferenced delete affected = %d, want 1", affected)
	}
	if _, err := repo.FindByID(second.ID); err != gorm.ErrRecordNotFound {
		t.Fatalf("deleted row err = %v, want ErrRecordNotFound", err)
	}
}

// TestForgeIntegrationDeleteIfUnreferenced 删除提交点原子：仍被任一项目引用
// 的行 0 行受影响且保留在库里，409 的判定由 service 层区分（列引用项目名）。
func TestForgeIntegrationDeleteIfUnreferenced(t *testing.T) {
	db := forgeIntegrationTestDB(t)
	repo := NewForgeIntegrationRepository(db)
	id := uuid.NewString()
	if err := repo.Create(&model.ForgeIntegration{ID: id, Kind: model.ForgeIntegrationKindForgejo, Name: "占用中", BaseURL: "https://keep.example.com"}); err != nil {
		t.Fatalf("create: %v", err)
	}

	user := model.User{ID: uuid.NewString(), Username: "u-" + uuid.NewString()[:8], Email: "u2@example.com", PasswordHash: "x"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := db.Create(&model.Project{
		ID: uuid.NewString(), UserID: user.ID, Name: "引用项目", PublicIntegrationID: id,
	}).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}

	affected, err := repo.DeleteIfUnreferenced(id)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if affected != 0 {
		t.Fatalf("referenced delete affected = %d, want 0", affected)
	}
	if _, err := repo.FindByID(id); err != nil {
		t.Fatalf("referenced row must survive: %v", err)
	}
}

func TestForgeIntegrationReferencingProjects(t *testing.T) {
	db := forgeIntegrationTestDB(t)
	repo := NewForgeIntegrationRepository(db)
	integrationID := uuid.NewString()

	// projects.user_id 有外键（测试库开了 foreign_keys），先建真实用户。
	user := model.User{ID: uuid.NewString(), Username: "u-" + uuid.NewString()[:8], Email: "u@example.com", PasswordHash: "x"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	mkProject := func(name string, codeRef, publicRef string) {
		t.Helper()
		p := model.Project{
			ID:                  uuid.NewString(),
			UserID:              user.ID,
			Name:                name,
			CodeIntegrationID:   codeRef,
			PublicIntegrationID: publicRef,
			CreatedAt:           time.Now().UTC(),
			UpdatedAt:           time.Now().UTC(),
		}
		if err := db.Create(&p).Error; err != nil {
			t.Fatalf("create project %s: %v", name, err)
		}
	}
	mkProject("代码引用", integrationID, "")
	mkProject("公开引用", "", integrationID)
	mkProject("无引用", "", "")

	refs, total, err := repo.ReferencingProjects(integrationID, 20)
	if err != nil {
		t.Fatalf("refs: %v", err)
	}
	if len(refs) != 2 || total != 2 {
		t.Fatalf("refs = %+v (total %d), want 2 projects", refs, total)
	}
	names := map[string]bool{}
	for _, ref := range refs {
		names[ref.Name] = true
	}
	if !names["代码引用"] || !names["公开引用"] {
		t.Fatalf("refs missing expected projects: %+v", refs)
	}

	refs, total, err = repo.ReferencingProjects(uuid.NewString(), 20)
	if err != nil || len(refs) != 0 || total != 0 {
		t.Fatalf("unreferenced refs = %+v (total %d), %v", refs, total, err)
	}
	// limit 截断：只回前 limit 条但 total 仍是全量计数。
	refs, total, err = repo.ReferencingProjects(integrationID, 1)
	if err != nil || len(refs) != 1 || total != 2 {
		t.Fatalf("limited refs = %+v (total %d), %v", refs, total, err)
	}
}
