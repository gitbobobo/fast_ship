package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/crypto"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	ghclient "github.com/godbobo/fast_ship/server/internal/pkg/github"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

func newTestGitHubTokenService(svc *testServices, detect func(ctx context.Context, token string) ghclient.TokenDetection) *GitHubTokenService {
	service := NewGitHubTokenService(svc.tokenRepo, svc.cfg, zap.NewNop())
	service.detect = detect
	return service
}

func detectOKClassic(_ context.Context, _ string) ghclient.TokenDetection {
	return ghclient.TokenDetection{Status: ghclient.DetectStatusOK, TokenType: ghclient.DetectTypeClassic, Scopes: []string{"repo"}, Login: "octocat"}
}

func storedTokenByTitle(t *testing.T, svc *testServices, title string) *model.UserGitHubToken {
	t.Helper()
	var token model.UserGitHubToken
	if err := svc.db.First(&token, "title = ?", title).Error; err != nil {
		t.Fatalf("load token %q: %v", title, err)
	}
	return &token
}

// Create：保存密文与指纹，同步检测成功后状态落 ok、scopes 落 JSON。
func TestGitHubTokenService_Create(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-token-create")
	service := newTestGitHubTokenService(svc, func(ctx context.Context, token string) ghclient.TokenDetection {
		if token != "ghp_live_secret_value" {
			t.Fatalf("detect must receive plaintext, got %q", token)
		}
		return ghclient.TokenDetection{Status: ghclient.DetectStatusOK, TokenType: ghclient.DetectTypeClassic, Scopes: []string{"repo", "read:org"}, Login: "octocat"}
	})

	resp, err := service.Create(user.ID, &CreateGitHubTokenRequest{Title: "main", Token: "ghp_live_secret_value"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if resp.DetectionStatus != "ok" || resp.DetectionTokenType != "classic" || resp.DetectionLogin != "octocat" {
		t.Fatalf("unexpected detection: %+v", resp)
	}
	if len(resp.DetectionScopes) != 2 || resp.DetectionScopes[1] != "read:org" {
		t.Fatalf("unexpected scopes: %v", resp.DetectionScopes)
	}
	if resp.TokenPrefix != githubTokenPrefix("ghp_live") {
		t.Fatalf("unexpected prefix %q", resp.TokenPrefix)
	}
	if resp.DetectedAt == nil {
		t.Fatal("expected detected_at set")
	}

	stored := storedTokenByTitle(t, svc, "main")
	if string(stored.TokenEncrypted) == "ghp_live" {
		t.Fatal("token must be stored encrypted")
	}
	plain, err := crypto.Decrypt(stored.TokenEncrypted, []byte(svc.cfg.Encryption.Key))
	if err != nil || string(plain) != "ghp_live_secret_value" {
		t.Fatalf("stored token not decryptable: %v", err)
	}
	var scopes []string
	if err := json.Unmarshal([]byte(stored.DetectionScopes), &scopes); err != nil || len(scopes) != 2 {
		t.Fatalf("scopes column must be JSON array, got %q err=%v", stored.DetectionScopes, err)
	}

	body, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(body), "ghp_live_secret_value") {
		t.Fatalf("response leaks token plaintext: %s", body)
	}
}

// Create：明文指纹与已有记录相同则复用，不修改已有标题、不重复建行。
func TestGitHubTokenService_Create_DedupesByFingerprint(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-token-dedupe")
	service := newTestGitHubTokenService(svc, func(ctx context.Context, token string) ghclient.TokenDetection {
		return ghclient.TokenDetection{Status: ghclient.DetectStatusOK, TokenType: ghclient.DetectTypeUnknown}
	})

	first, err := service.Create(user.ID, &CreateGitHubTokenRequest{Title: "first", Token: "ghp_same"})
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	second, err := service.Create(user.ID, &CreateGitHubTokenRequest{Title: "second", Token: "ghp_same"})
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if second.Id != first.Id || second.Title != "first" {
		t.Fatalf("expected existing record reused with original title, got %+v vs %+v", first, second)
	}
	var count int64
	svc.db.Model(&model.UserGitHubToken{}).Where("user_id = ?", user.ID).Count(&count)
	if count != 1 {
		t.Fatalf("expected single row, got %d", count)
	}
}

// Create：检测失败（网络/5xx → failed）仍保存且返回 200 形状。
func TestGitHubTokenService_Create_DetectFailedStillSaved(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-token-detect-fail")
	service := newTestGitHubTokenService(svc, func(ctx context.Context, token string) ghclient.TokenDetection {
		return ghclient.TokenDetection{Status: ghclient.DetectStatusFailed, TokenType: ghclient.DetectTypeUnknown, Detail: "检测失败：connection refused"}
	})

	resp, err := service.Create(user.ID, &CreateGitHubTokenRequest{Title: "flaky", Token: "ghp_net"})
	if err != nil {
		t.Fatalf("create must succeed even when detection fails: %v", err)
	}
	if resp.DetectionStatus != "failed" || resp.DetectionDetail == "" {
		t.Fatalf("expected failed status with detail, got %+v", resp)
	}
	body, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(body), `"detection_scopes":null`) {
		t.Fatalf("detection_scopes must be an array, got %s", body)
	}
}

// Create：凭证无效（401 → invalid）同样保存，状态区分 invalid 与 failed。
func TestGitHubTokenService_Create_InvalidCredentialSaved(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-token-invalid")
	service := newTestGitHubTokenService(svc, func(ctx context.Context, token string) ghclient.TokenDetection {
		return ghclient.TokenDetection{Status: ghclient.DetectStatusInvalid, TokenType: ghclient.DetectTypeUnknown, Detail: "GitHub 拒绝了该 Token（401）"}
	})

	resp, err := service.Create(user.ID, &CreateGitHubTokenRequest{Title: "dead", Token: "ghp_dead"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if resp.DetectionStatus != "invalid" {
		t.Fatalf("expected invalid, got %q", resp.DetectionStatus)
	}
}

// Update：只改标题不重检；替换密钥重检；新明文撞同用户另一条返回 40914 并点名已有记录。
func TestGitHubTokenService_Update(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-token-update")
	detector := func(ctx context.Context, token string) ghclient.TokenDetection {
		return ghclient.TokenDetection{Status: ghclient.DetectStatusOK, TokenType: ghclient.DetectTypeUnknown, Login: token}
	}
	service := newTestGitHubTokenService(svc, detector)

	token, err := service.Create(user.ID, &CreateGitHubTokenRequest{Title: "old-title", Token: "ghp_v1"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	other, err := service.Create(user.ID, &CreateGitHubTokenRequest{Title: "other", Token: "ghp_other"})
	if err != nil {
		t.Fatalf("create other: %v", err)
	}

	// 只改标题：detected_at 不变
	retitled, err := service.Update(user.ID, token.Id, &UpdateGitHubTokenRequest{Title: apiPtr("new-title")})
	if err != nil {
		t.Fatalf("retitle: %v", err)
	}
	if retitled.Title != "new-title" || retitled.DetectionLogin != "ghp_v1" {
		t.Fatalf("expected retitle without re-detect, got %+v", retitled)
	}

	// 替换密钥：重检（login 反映新明文）
	replaced, err := service.Update(user.ID, token.Id, &UpdateGitHubTokenRequest{Token: apiPtr("ghp_v2")})
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if replaced.DetectionLogin != "ghp_v2" || replaced.TokenPrefix != githubTokenPrefix("ghp_v2") {
		t.Fatalf("expected re-detection after replace, got %+v", replaced)
	}

	// 替换撞 other 的指纹：40914，other 不被隐式合并
	_, err = service.Update(user.ID, token.Id, &UpdateGitHubTokenRequest{Token: apiPtr("ghp_other")})
	appErr, ok := err.(*errs.AppError)
	if !ok || appErr.Code != errs.ErrGitHubTokenDuplicate.Code {
		t.Fatalf("expected 40914, got %v", err)
	}
	if !strings.Contains(appErr.Message, "other") {
		t.Fatalf("expected duplicate message to name existing record, got %q", appErr.Message)
	}
	stillOther := storedTokenByTitle(t, svc, "other")
	plain, decErr := crypto.Decrypt(stillOther.TokenEncrypted, []byte(svc.cfg.Encryption.Key))
	if decErr != nil || string(plain) != "ghp_other" {
		t.Fatalf("other record must be untouched, got %q err=%v", plain, decErr)
	}
	_ = other
}

// Delete：有引用（任一用途）返回 40913，解除后可删。
func TestGitHubTokenService_Delete_Referenced(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-token-delete")
	service := newTestGitHubTokenService(svc, detectOKClassic)
	token, err := service.Create(user.ID, &CreateGitHubTokenRequest{Title: "used", Token: "ghp_used"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.CodeGitHubTokenID = token.Id
	})

	if err := service.Delete(user.ID, token.Id); !errors.Is(err, errs.ErrGitHubTokenInUse) {
		t.Fatalf("expected ErrGitHubTokenInUse, got %v", err)
	}

	var stored model.Project
	svc.db.First(&stored, "id = ?", project.ID)
	stored.CodeGitHubTokenID = ""
	if err := svc.db.Save(&stored).Error; err != nil {
		t.Fatalf("clear reference: %v", err)
	}
	if err := service.Delete(user.ID, token.Id); err != nil {
		t.Fatalf("delete after unreferencing: %v", err)
	}
	var count int64
	svc.db.Model(&model.UserGitHubToken{}).Where("id = ?", token.Id).Count(&count)
	if count != 0 {
		t.Fatal("expected row deleted")
	}
}

// Delete：不存在或他人 Token 返回 40420。
func TestGitHubTokenService_Delete_NotFound(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-token-del-404")
	other := createTestUser(t, svc.db, "user-token-del-other")
	service := newTestGitHubTokenService(svc, detectOKClassic)
	foreign, err := service.Create(other.ID, &CreateGitHubTokenRequest{Title: "foreign", Token: "ghp_foreign"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := service.Delete(user.ID, "nonexistent"); !errors.Is(err, errs.ErrGitHubTokenNotFound) {
		t.Fatalf("expected ErrGitHubTokenNotFound, got %v", err)
	}
	if err := service.Delete(user.ID, foreign.Id); !errors.Is(err, errs.ErrGitHubTokenNotFound) {
		t.Fatalf("expected foreign token invisible, got %v", err)
	}
}

// Detect：手动刷新覆盖状态与 detected_at。
func TestGitHubTokenService_Detect(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-token-detect")
	service := newTestGitHubTokenService(svc, func(ctx context.Context, token string) ghclient.TokenDetection {
		return ghclient.TokenDetection{Status: ghclient.DetectStatusOK, TokenType: ghclient.DetectTypeFineGrained, Login: "fine-bot", Detail: "Fine-grained Token 无法自动获取完整授权"}
	})
	token, err := service.Create(user.ID, &CreateGitHubTokenRequest{Title: "fine", Token: "github_pat_fine"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	service.detect = func(ctx context.Context, token string) ghclient.TokenDetection {
		return ghclient.TokenDetection{Status: ghclient.DetectStatusFailed, TokenType: ghclient.DetectTypeFineGrained, Detail: "检测失败：timeout"}
	}
	refreshed, err := service.Detect(user.ID, token.Id)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if refreshed.DetectionStatus != "failed" || refreshed.DetectedAt == nil {
		t.Fatalf("expected refreshed failed status, got %+v", refreshed)
	}
	if refreshed.DetectionTokenType != "fine_grained" {
		t.Fatalf("expected fine_grained type preserved on failure, got %q", refreshed.DetectionTokenType)
	}
}

// List：引用项目（两用途去重）随列表返回；不触发 GitHub 请求。
func TestGitHubTokenService_List_ReferencingProjects(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-token-list")
	detector := func(ctx context.Context, token string) ghclient.TokenDetection {
		return ghclient.TokenDetection{Status: model.TokenDetectionUnchecked, TokenType: ghclient.DetectTypeUnknown}
	}
	service := newTestGitHubTokenService(svc, detector)
	token, err := service.Create(user.ID, &CreateGitHubTokenRequest{Title: "shared", Token: "ghp_shared"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.Name = "alpha"
		p.PublicGitHubTokenID = token.Id
		p.CodeGitHubTokenID = ""
	})
	createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.Name = "beta"
		p.PublicGitHubTokenID = token.Id
		p.CodeGitHubTokenID = token.Id
	})
	createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.Name = "gamma"
		p.PublicGitHubTokenID = ""
		p.CodeGitHubTokenID = ""
	})

	items, err := service.List(user.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 token, got %d", len(items))
	}
	refs := items[0].ReferencedProjects
	if len(refs) != 2 {
		t.Fatalf("expected 2 referencing projects (deduped), got %+v", refs)
	}
	names := map[string]bool{}
	for _, ref := range refs {
		names[ref.Name] = true
	}
	if !names["alpha"] || !names["beta"] || names["gamma"] {
		t.Fatalf("unexpected referencing projects: %+v", refs)
	}
}

func apiPtr(s string) *string { return &s }

// 检测落库只更新检测列：密钥已被替换时，过期检测不能把旧密文写回。
func TestGitHubTokenService_StaleDetectionDoesNotRestoreCiphertext(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-token-stale-detect")
	service := newTestGitHubTokenService(svc, detectOKClassic)
	created, err := service.Create(user.ID, &CreateGitHubTokenRequest{Title: "stale", Token: "ghp_old_secret"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	stored := storedTokenByTitle(t, svc, "stale")
	oldFingerprint := stored.TokenFingerprint

	replaced, err := crypto.Encrypt([]byte("ghp_new_secret"), []byte(svc.cfg.Encryption.Key))
	if err != nil {
		t.Fatalf("encrypt replacement: %v", err)
	}
	if err := svc.db.Model(&model.UserGitHubToken{}).Where("id = ?", created.Id).Updates(map[string]interface{}{
		"token_encrypted":   replaced,
		"token_fingerprint": githubTokenFingerprint("ghp_new_secret"),
		"token_prefix":      githubTokenPrefix("ghp_new_secret"),
	}).Error; err != nil {
		t.Fatalf("simulate replace: %v", err)
	}

	stored.DetectionStatus = model.TokenDetectionOK
	stored.DetectionDetail = "stale"
	if err := svc.tokenRepo.UpdateDetection(stored, oldFingerprint); err != nil {
		t.Fatalf("stale detection write: %v", err)
	}

	var after model.UserGitHubToken
	if err := svc.db.First(&after, "id = ?", created.Id).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	plain, err := crypto.Decrypt(after.TokenEncrypted, []byte(svc.cfg.Encryption.Key))
	if err != nil || string(plain) != "ghp_new_secret" {
		t.Fatalf("replaced ciphertext must be preserved, got %q err=%v", plain, err)
	}
	if after.DetectionDetail == "stale" {
		t.Fatal("stale detection must not apply after fingerprint changed")
	}
}

func TestGitHubTokenService_Create_TrimsPlaintext(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-token-trim")
	service := newTestGitHubTokenService(svc, func(_ context.Context, token string) ghclient.TokenDetection {
		if token != "ghp_padded" {
			t.Fatalf("detect must see trimmed plaintext, got %q", token)
		}
		return detectOKClassic(context.Background(), token)
	})

	first, err := service.Create(user.ID, &CreateGitHubTokenRequest{Title: "padded", Token: "  ghp_padded  "})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	second, err := service.Create(user.ID, &CreateGitHubTokenRequest{Title: "again", Token: "ghp_padded"})
	if err != nil {
		t.Fatalf("dedupe: %v", err)
	}
	if second.Id != first.Id {
		t.Fatalf("trimmed plaintext must share fingerprint, got %s vs %s", first.Id, second.Id)
	}
}

func TestGitHubTokenService_Create_WhitespaceOnlyRejected(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-token-blank")
	service := newTestGitHubTokenService(svc, detectOKClassic)
	_, err := service.Create(user.ID, &CreateGitHubTokenRequest{Title: "blank", Token: "   "})
	if !errors.Is(err, errs.ErrInvalidParams) {
		t.Fatalf("expected ErrInvalidParams, got %v", err)
	}
}

func TestGitHubTokenService_Update_ResetsDetectionBeforeDetect(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-token-reset-detect")
	service := newTestGitHubTokenService(svc, detectOKClassic)
	created, err := service.Create(user.ID, &CreateGitHubTokenRequest{Title: "live", Token: "ghp_old"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	sawUnchecked := false
	service.detect = func(_ context.Context, token string) ghclient.TokenDetection {
		var row model.UserGitHubToken
		if err := svc.db.First(&row, "id = ?", created.Id).Error; err != nil {
			t.Fatalf("load during detect: %v", err)
		}
		if row.DetectionStatus == model.TokenDetectionUnchecked && row.DetectionLogin == "" && row.DetectedAt == nil {
			sawUnchecked = true
		}
		if token != "ghp_new" {
			t.Fatalf("detect must receive new plaintext, got %q", token)
		}
		return ghclient.TokenDetection{Status: ghclient.DetectStatusOK, TokenType: ghclient.DetectTypeUnknown, Login: "new-login"}
	}
	replaced, err := service.Update(user.ID, created.Id, &UpdateGitHubTokenRequest{Token: apiPtr(" ghp_new ")})
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if !sawUnchecked {
		t.Fatal("replace must persist unchecked detection before running detect")
	}
	if replaced.DetectionLogin != "new-login" {
		t.Fatalf("expected re-detection, got %+v", replaced)
	}
}

func TestUserGitHubTokenRepository_UpdateDoesNotResurrect(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-token-no-upsert")
	service := newTestGitHubTokenService(svc, detectOKClassic)
	created, err := service.Create(user.ID, &CreateGitHubTokenRequest{Title: "gone", Token: "ghp_gone"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	stored := storedTokenByTitle(t, svc, "gone")
	if err := svc.db.Where("id = ?", created.Id).Delete(&model.UserGitHubToken{}).Error; err != nil {
		t.Fatalf("delete: %v", err)
	}
	stored.Title = "resurrected"
	if err := svc.tokenRepo.UpdateTitle(stored.ID, stored.UserID, stored.Title); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("expected ErrRecordNotFound, got %v", err)
	}
	if err := svc.tokenRepo.ReplaceSecret(stored, stored.TokenFingerprint); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("replace after delete: expected ErrRecordNotFound, got %v", err)
	}
	var count int64
	svc.db.Model(&model.UserGitHubToken{}).Where("id = ?", created.Id).Count(&count)
	if count != 0 {
		t.Fatal("deleted token must not be inserted back")
	}
}

func TestGitHubTokenService_UpdateTitleDoesNotRevertSecret(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-token-title-no-revert")
	service := newTestGitHubTokenService(svc, detectOKClassic)
	created, err := service.Create(user.ID, &CreateGitHubTokenRequest{Title: "live", Token: "ghp_old"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	stale := storedTokenByTitle(t, svc, "live")
	if _, err := service.Update(user.ID, created.Id, &UpdateGitHubTokenRequest{Token: apiPtr("ghp_new")}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if err := svc.tokenRepo.UpdateTitle(stale.ID, stale.UserID, "renamed"); err != nil {
		t.Fatalf("stale title: %v", err)
	}
	row := storedTokenByTitle(t, svc, "renamed")
	plain, decErr := crypto.Decrypt(row.TokenEncrypted, []byte(svc.cfg.Encryption.Key))
	if decErr != nil || string(plain) != "ghp_new" {
		t.Fatalf("title update must not restore old secret, got %q err=%v", plain, decErr)
	}
	if row.TokenPrefix != githubTokenPrefix("ghp_new") {
		t.Fatalf("prefix must stay on new secret, got %q", row.TokenPrefix)
	}
}

func TestGitHubTokenService_ReplaceSecretConflictKeepsWinner(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-token-replace-cas")
	service := newTestGitHubTokenService(svc, detectOKClassic)
	created, err := service.Create(user.ID, &CreateGitHubTokenRequest{Title: "live", Token: "ghp_old"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	stale := storedTokenByTitle(t, svc, "live")
	if _, err := service.Update(user.ID, created.Id, &UpdateGitHubTokenRequest{Token: apiPtr("ghp_winner")}); err != nil {
		t.Fatalf("first replace: %v", err)
	}
	stale.TokenEncrypted = []byte("stale-cipher")
	stale.TokenFingerprint = githubTokenFingerprint("ghp_loser")
	stale.TokenPrefix = githubTokenPrefix("ghp_loser")
	resetDetection(stale)
	if err := svc.tokenRepo.ReplaceSecret(stale, githubTokenFingerprint("ghp_old")); !errors.Is(err, repository.ErrGitHubTokenConflict) {
		t.Fatalf("expected ErrGitHubTokenConflict, got %v", err)
	}
	row := storedTokenByTitle(t, svc, "live")
	plain, decErr := crypto.Decrypt(row.TokenEncrypted, []byte(svc.cfg.Encryption.Key))
	if decErr != nil || string(plain) != "ghp_winner" {
		t.Fatalf("conflicting replace must not overwrite winner, got %q err=%v", plain, decErr)
	}
}
