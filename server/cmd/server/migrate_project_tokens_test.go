package main

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/crypto"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

const migrateTestKey = "12345678901234567890123456789012"

// openMigrationTestDB 打开一个独立的内存库（与生产一致开启外键）。
func openMigrationTestDB(t *testing.T) (*gorm.DB, error) {
	t.Helper()
	return gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared&_pragma=foreign_keys(1)"), &gorm.Config{})
}

func setupLegacyDBColumns(t *testing.T, columns ...string) *gorm.DB {
	t.Helper()
	db, err := openMigrationTestDB(t)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(autoMigrateModels...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, column := range columns {
		if err := db.Exec("ALTER TABLE projects ADD COLUMN " + column + " blob").Error; err != nil {
			t.Fatalf("add legacy column: %v", err)
		}
	}
	return db
}

// setupLegacyDB 建全新库后手工补上旧密文列，模拟未迁移的存量库。
func setupLegacyDB(t *testing.T) *gorm.DB {
	t.Helper()
	return setupLegacyDBColumns(t, "github_token_encrypted", "github_pr_token_encrypted")
}

func createMigrationUser(t *testing.T, db *gorm.DB, id string) {
	t.Helper()
	now := time.Now().UTC()
	if err := db.Create(&model.User{ID: id, Username: "u_" + id, Email: id + "@example.com", PasswordHash: "x", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
}

func createLegacyProject(t *testing.T, db *gorm.DB, id, userID string, access, pr []byte, createdAt time.Time) {
	t.Helper()
	if err := db.Exec(`
		INSERT INTO projects (id, user_id, name, description, github_owner, github_repo,
		                      public_github_token_id, code_github_token_id,
		                      github_token_encrypted, github_pr_token_encrypted,
		                      created_at, updated_at)
		VALUES (?, ?, ?, '', 'owner', 'repo', '', '', ?, ?, ?, ?)
	`, id, userID, "proj-"+id, access, pr, createdAt, createdAt).Error; err != nil {
		t.Fatalf("create legacy project %s: %v", id, err)
	}
}

func legacyTokenRows(t *testing.T, db *gorm.DB) []model.UserGitHubToken {
	t.Helper()
	var tokens []model.UserGitHubToken
	if err := db.Order("created_at ASC").Find(&tokens).Error; err != nil {
		t.Fatalf("list tokens: %v", err)
	}
	return tokens
}

func projectRefs(t *testing.T, db *gorm.DB, id string) (string, string) {
	t.Helper()
	var row struct {
		Public string `gorm:"column:public_github_token_id"`
		Code   string `gorm:"column:code_github_token_id"`
	}
	if err := db.Raw(`SELECT public_github_token_id, code_github_token_id FROM projects WHERE id = ?`, id).Scan(&row).Error; err != nil {
		t.Fatalf("load project refs: %v", err)
	}
	return row.Public, row.Code
}

func encryptMigrateToken(t *testing.T, plaintext string) []byte {
	t.Helper()
	encrypted, err := crypto.Encrypt([]byte(plaintext), []byte(migrateTestKey))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	return encrypted
}

// 同一用户内明文相同的旧密文去重复用：两个项目共用同一 Access Token 只产生一条记录，
// 明文不同才另起一条；标题按产生顺序「迁移 Token N」。
func TestMigrateProjectGitHubTokens_DedupesByPlaintext(t *testing.T) {
	db := setupLegacyDB(t)
	createMigrationUser(t, db, "u1")
	base := time.Now().UTC().Add(-time.Hour)
	shared := encryptMigrateToken(t, "ghp_shared")
	createLegacyProject(t, db, "p1", "u1", shared, nil, base)
	createLegacyProject(t, db, "p2", "u1", shared, nil, base.Add(time.Minute))
	createLegacyProject(t, db, "p3", "u1", encryptMigrateToken(t, "ghp_other"), nil, base.Add(2*time.Minute))

	if err := migrateProjectGitHubTokens(db, []byte(migrateTestKey), zap.NewNop()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	tokens := legacyTokenRows(t, db)
	if len(tokens) != 2 {
		t.Fatalf("expected 2 token rows, got %d", len(tokens))
	}
	if tokens[0].Title != "迁移 Token 1" || tokens[1].Title != "迁移 Token 2" {
		t.Fatalf("unexpected titles: %q, %q", tokens[0].Title, tokens[1].Title)
	}
	if tokens[0].TokenPrefix != "ghp_shared"[:8] || tokens[1].TokenPrefix != "ghp_other"[:8] {
		t.Fatalf("unexpected prefixes: %q, %q", tokens[0].TokenPrefix, tokens[1].TokenPrefix)
	}
	for _, token := range tokens {
		if token.DetectionStatus != model.TokenDetectionUnchecked || token.DetectedAt != nil {
			t.Fatalf("migrated tokens must be unchecked: %+v", token)
		}
	}

	public1, code1 := projectRefs(t, db, "p1")
	public2, code2 := projectRefs(t, db, "p2")
	if public1 != tokens[0].ID || code1 != tokens[0].ID || public2 != tokens[0].ID || code2 != tokens[0].ID {
		t.Fatalf("shared token must be referenced by both projects")
	}
	public3, code3 := projectRefs(t, db, "p3")
	if public3 != tokens[1].ID || code3 != tokens[1].ID {
		t.Fatalf("distinct token expected for p3: %s/%s", public3, code3)
	}
}

// Access-only 项目把原 Access 显式引用到两个位置（保留旧兜底行为）；
// 有独立 PR Token 的项目 public=Access、code=PR；只有 PR Token 的项目 public 空。
func TestMigrateProjectGitHubTokens_ReferenceMapping(t *testing.T) {
	db := setupLegacyDB(t)
	createMigrationUser(t, db, "u1")
	base := time.Now().UTC().Add(-time.Hour)
	access := encryptMigrateToken(t, "ghp_access")
	pr := encryptMigrateToken(t, "ghp_pr")
	createLegacyProject(t, db, "p-access-only", "u1", access, nil, base)
	createLegacyProject(t, db, "p-both", "u1", access, pr, base.Add(time.Minute))
	createLegacyProject(t, db, "p-pr-only", "u1", nil, pr, base.Add(2*time.Minute))

	if err := migrateProjectGitHubTokens(db, []byte(migrateTestKey), zap.NewNop()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	tokens := legacyTokenRows(t, db)
	if len(tokens) != 2 {
		t.Fatalf("expected 2 token rows (access+pr), got %d", len(tokens))
	}
	var accessID, prID string
	for _, token := range tokens {
		if token.TokenPrefix == "ghp_acce" {
			accessID = token.ID
		} else {
			prID = token.ID
		}
	}

	if public, code := projectRefs(t, db, "p-access-only"); public != accessID || code != accessID {
		t.Fatalf("access-only project must dual-reference access token, got %s/%s", public, code)
	}
	if public, code := projectRefs(t, db, "p-both"); public != accessID || code != prID {
		t.Fatalf("both project: expected public=access code=pr, got %s/%s", public, code)
	}
	if public, code := projectRefs(t, db, "p-pr-only"); public != "" || code != prID {
		t.Fatalf("pr-only project: expected empty public, got %s/%s", public, code)
	}
}

// 任一旧密文解密失败即中止：返回错误，失败用户不产生 Token 行、项目引用不动。
func TestMigrateProjectGitHubTokens_DecryptFailureAborts(t *testing.T) {
	db := setupLegacyDB(t)
	createMigrationUser(t, db, "u1")
	createMigrationUser(t, db, "u2")
	base := time.Now().UTC().Add(-time.Hour)
	createLegacyProject(t, db, "p-ok", "u1", encryptMigrateToken(t, "ghp_ok"), nil, base)
	createLegacyProject(t, db, "p-bad", "u2", []byte("corrupt-ciphertext"), nil, base)

	if err := migrateProjectGitHubTokens(db, []byte(migrateTestKey), zap.NewNop()); err == nil {
		t.Fatal("expected migration to fail on undecryptable ciphertext")
	}

	var count int64
	db.Model(&model.UserGitHubToken{}).Where("user_id = ?", "u2").Count(&count)
	if count != 0 {
		t.Fatalf("failed user must produce no token rows, got %d", count)
	}
	db.Model(&model.UserGitHubToken{}).Where("user_id = ?", "u1").Count(&count)
	if count != 0 {
		t.Fatalf("successful user must roll back when any user fails, got %d token rows", count)
	}
	if public, code := projectRefs(t, db, "p-bad"); public != "" || code != "" {
		t.Fatalf("failed project refs must be untouched, got %s/%s", public, code)
	}
	if public, code := projectRefs(t, db, "p-ok"); public != "" || code != "" {
		t.Fatalf("rolled-back user refs must be untouched, got %s/%s", public, code)
	}
}

// 幂等：迁移成功后旧列被清空/删除，重复执行不再产生新行、不再改动引用。
func TestMigrateProjectGitHubTokens_Idempotent(t *testing.T) {
	db := setupLegacyDB(t)
	createMigrationUser(t, db, "u1")
	base := time.Now().UTC().Add(-time.Hour)
	createLegacyProject(t, db, "p1", "u1", encryptMigrateToken(t, "ghp_once"), encryptMigrateToken(t, "ghp_pr_once"), base)

	if err := migrateProjectGitHubTokens(db, []byte(migrateTestKey), zap.NewNop()); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	tokens := legacyTokenRows(t, db)
	public1, code1 := projectRefs(t, db, "p1")

	if err := migrateProjectGitHubTokens(db, []byte(migrateTestKey), zap.NewNop()); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	tokensAfter := legacyTokenRows(t, db)
	public2, code2 := projectRefs(t, db, "p1")

	if len(tokens) != 2 || len(tokensAfter) != 2 {
		t.Fatalf("expected 2 rows stable across runs, got %d -> %d", len(tokens), len(tokensAfter))
	}
	if public1 != public2 || code1 != code2 {
		t.Fatalf("references must be stable: %s/%s -> %s/%s", public1, code1, public2, code2)
	}
	for _, column := range []string{"github_token_encrypted", "github_pr_token_encrypted"} {
		if has, err := hasSQLiteColumn(db, "projects", column); err != nil || has {
			t.Fatalf("legacy column %s should be dropped, has=%v err=%v", column, has, err)
		}
	}
}

// 清理中断后只剩 Access 列：仍能读出并迁移，不会因引用缺失的 PR 列而启动失败。
func TestMigrateProjectGitHubTokens_OnlyAccessColumn(t *testing.T) {
	db := setupLegacyDBColumns(t, "github_token_encrypted")
	createMigrationUser(t, db, "u1")
	now := time.Now().UTC()
	access := encryptMigrateToken(t, "ghp_access_col")
	if err := db.Exec(`
		INSERT INTO projects (id, user_id, name, description, github_owner, github_repo,
		                      public_github_token_id, code_github_token_id,
		                      github_token_encrypted, created_at, updated_at)
		VALUES (?, ?, 'p', '', 'owner', 'repo', '', '', ?, ?, ?)
	`, "p1", "u1", access, now, now).Error; err != nil {
		t.Fatalf("insert: %v", err)
	}

	if err := migrateProjectGitHubTokens(db, []byte(migrateTestKey), zap.NewNop()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	tokens := legacyTokenRows(t, db)
	if len(tokens) != 1 {
		t.Fatalf("expected 1 token, got %d", len(tokens))
	}
	if public, code := projectRefs(t, db, "p1"); public != tokens[0].ID || code != tokens[0].ID {
		t.Fatalf("access-only leftover column must dual-reference, got %s/%s", public, code)
	}
	if has, err := hasSQLiteColumn(db, "projects", "github_token_encrypted"); err != nil || has {
		t.Fatalf("access column should be dropped, has=%v err=%v", has, err)
	}
}

// 清理中断后只剩 PR 列：public 空、code 指向迁移记录。
func TestMigrateProjectGitHubTokens_OnlyPRColumn(t *testing.T) {
	db := setupLegacyDBColumns(t, "github_pr_token_encrypted")
	createMigrationUser(t, db, "u1")
	now := time.Now().UTC()
	pr := encryptMigrateToken(t, "ghp_pr_col")
	if err := db.Exec(`
		INSERT INTO projects (id, user_id, name, description, github_owner, github_repo,
		                      public_github_token_id, code_github_token_id,
		                      github_pr_token_encrypted, created_at, updated_at)
		VALUES (?, ?, 'p', '', 'owner', 'repo', '', '', ?, ?, ?)
	`, "p1", "u1", pr, now, now).Error; err != nil {
		t.Fatalf("insert: %v", err)
	}

	if err := migrateProjectGitHubTokens(db, []byte(migrateTestKey), zap.NewNop()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	tokens := legacyTokenRows(t, db)
	if len(tokens) != 1 {
		t.Fatalf("expected 1 token, got %d", len(tokens))
	}
	if public, code := projectRefs(t, db, "p1"); public != "" || code != tokens[0].ID {
		t.Fatalf("pr-only leftover column: expected empty public, got %s/%s", public, code)
	}
	if has, err := hasSQLiteColumn(db, "projects", "github_pr_token_encrypted"); err != nil || has {
		t.Fatalf("pr column should be dropped, has=%v err=%v", has, err)
	}
}

// 全新库（无旧密文列）直接跳过。
func TestMigrateProjectGitHubTokens_FreshDBNoop(t *testing.T) {
	db, err := openMigrationTestDB(t)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(autoMigrateModels...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := migrateProjectGitHubTokens(db, []byte(migrateTestKey), zap.NewNop()); err != nil {
		t.Fatalf("fresh db migrate must be a no-op: %v", err)
	}
}

// 旧密文 blob 直接复用进 Token 库（同一密钥可解密出原明文）。
func TestMigrateProjectGitHubTokens_ReusesCiphertext(t *testing.T) {
	db := setupLegacyDB(t)
	createMigrationUser(t, db, "u1")
	base := time.Now().UTC().Add(-time.Hour)
	ciphertext := encryptMigrateToken(t, "ghp_reuse")
	createLegacyProject(t, db, "p1", "u1", ciphertext, nil, base)

	if err := migrateProjectGitHubTokens(db, []byte(migrateTestKey), zap.NewNop()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	tokens := legacyTokenRows(t, db)
	if len(tokens) != 1 {
		t.Fatalf("expected 1 token row, got %d", len(tokens))
	}
	// AES-GCM 密文每次不同：字节相等才能证明没有解密重加密。
	if string(tokens[0].TokenEncrypted) != string(ciphertext) {
		t.Fatal("expected legacy ciphertext reused verbatim")
	}
	plain, err := crypto.Decrypt(tokens[0].TokenEncrypted, []byte(migrateTestKey))
	if err != nil || string(plain) != "ghp_reuse" {
		t.Fatalf("reused ciphertext not decryptable: %v", err)
	}
	_ = uuid.NewString
}
