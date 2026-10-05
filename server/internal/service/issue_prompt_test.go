package service

import (
	"errors"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
)

func TestIssuePromptServiceGetPromptsNotConfigured(t *testing.T) {
	services := setupTestServices(t)
	user := createTestUser(t, services.db, "user-issue-prompt-empty")

	resp, err := services.issuePromptService.GetPrompts(user.ID)
	if err != nil {
		t.Fatalf("expected nil error for missing config, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response for missing config")
	}
	if resp.Prompts != nil {
		t.Fatalf("expected nil prompts for missing config, got: %#v", resp.Prompts)
	}
}

func TestIssuePromptServiceUpdateAndGetRoundTrip(t *testing.T) {
	services := setupTestServices(t)
	user := createTestUser(t, services.db, "user-issue-prompt-roundtrip")

	input := []api.IssuePromptItem{
		{Id: "id-1", Name: "默认", Content: "请处理此问题"},
		{Id: "id-2", Name: "详细", Content: "请仔细分析并修复此问题"},
	}
	updated, err := services.issuePromptService.UpdatePrompts(user.ID, UpdateIssuePromptsRequest{Prompts: input})
	if err != nil {
		t.Fatalf("update prompts: %v", err)
	}
	if len(updated.Prompts) != 2 {
		t.Fatalf("expected 2 prompts returned, got %d", len(updated.Prompts))
	}
	if updated.Prompts[0].Id != "id-1" || updated.Prompts[1].Content != "请仔细分析并修复此问题" {
		t.Fatalf("unexpected prompts payload: %#v", updated.Prompts)
	}

	got, err := services.issuePromptService.GetPrompts(user.ID)
	if err != nil {
		t.Fatalf("get prompts: %v", err)
	}
	if len(got.Prompts) != 2 {
		t.Fatalf("expected 2 prompts persisted, got %d", len(got.Prompts))
	}
	if got.Prompts[0].Name != "默认" || got.Prompts[1].Id != "id-2" {
		t.Fatalf("unexpected persisted prompts: %#v", got.Prompts)
	}
}

func TestIssuePromptServiceSupportsBatchRoundTrip(t *testing.T) {
	services := setupTestServices(t)
	user := createTestUser(t, services.db, "user-issue-prompt-batch-roundtrip")

	input := []api.IssuePromptItem{
		{Id: "id-1", Name: "批量", Content: "批量正文", SupportsBatch: api.Ptr(true)},
		{Id: "id-2", Name: "单发", Content: "单发正文", SupportsBatch: api.Ptr(false)},
	}
	updated, err := services.issuePromptService.UpdatePrompts(user.ID, UpdateIssuePromptsRequest{Prompts: input})
	if err != nil {
		t.Fatalf("update prompts: %v", err)
	}
	if len(updated.Prompts) != 2 {
		t.Fatalf("expected 2 prompts returned, got %d", len(updated.Prompts))
	}
	if !api.Deref(updated.Prompts[0].SupportsBatch) || api.Deref(updated.Prompts[1].SupportsBatch) {
		t.Fatalf("unexpected supports_batch in update response: %#v", updated.Prompts)
	}

	got, err := services.issuePromptService.GetPrompts(user.ID)
	if err != nil {
		t.Fatalf("get prompts: %v", err)
	}
	if len(got.Prompts) != 2 {
		t.Fatalf("expected 2 prompts persisted, got %d", len(got.Prompts))
	}
	if !api.Deref(got.Prompts[0].SupportsBatch) || api.Deref(got.Prompts[1].SupportsBatch) {
		t.Fatalf("unexpected supports_batch persisted: %#v", got.Prompts)
	}
}

func TestIssuePromptServiceLegacyJSONWithoutSupportsBatch(t *testing.T) {
	services := setupTestServices(t)
	user := createTestUser(t, services.db, "user-issue-prompt-legacy")

	// 旧格式 JSON：没有 supports_batch 字段，直接写入 prompts 文本列。
	legacy := `[{"id":"legacy-1","name":"旧格式","content":"旧正文"},{"id":"legacy-2","name":"旧格式二","content":"旧正文二","supports_batch":true}]`
	now := time.Now()
	if err := services.db.Exec(
		"INSERT INTO user_issue_prompt_settings (user_id, prompts, created_at, updated_at) VALUES (?, ?, ?, ?)",
		user.ID, legacy, now, now,
	).Error; err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	got, err := services.issuePromptService.GetPrompts(user.ID)
	if err != nil {
		t.Fatalf("get prompts: %v", err)
	}
	if len(got.Prompts) != 2 {
		t.Fatalf("expected 2 prompts persisted, got %d: %#v", len(got.Prompts), got.Prompts)
	}
	if api.Deref(got.Prompts[0].SupportsBatch) {
		t.Fatalf("expected missing supports_batch to unmarshal as false, got: %#v", got.Prompts[0])
	}
	if !api.Deref(got.Prompts[1].SupportsBatch) {
		t.Fatalf("expected explicit supports_batch true to be preserved, got: %#v", got.Prompts[1])
	}
}

func TestIssuePromptServiceUpdateIsReplaceNotAppend(t *testing.T) {
	services := setupTestServices(t)
	user := createTestUser(t, services.db, "user-issue-prompt-replace")

	first := []api.IssuePromptItem{
		{Id: "a", Name: "A", Content: "正文 A"},
		{Id: "b", Name: "B", Content: "正文 B"},
		{Id: "c", Name: "C", Content: "正文 C"},
	}
	if _, err := services.issuePromptService.UpdatePrompts(user.ID, UpdateIssuePromptsRequest{Prompts: first}); err != nil {
		t.Fatalf("first update: %v", err)
	}

	second := []api.IssuePromptItem{
		{Id: "x", Name: "X", Content: "正文 X"},
	}
	if _, err := services.issuePromptService.UpdatePrompts(user.ID, UpdateIssuePromptsRequest{Prompts: second}); err != nil {
		t.Fatalf("second update: %v", err)
	}

	got, err := services.issuePromptService.GetPrompts(user.ID)
	if err != nil {
		t.Fatalf("get prompts: %v", err)
	}
	if len(got.Prompts) != 1 {
		t.Fatalf("expected replace to 1 item, got %d: %#v", len(got.Prompts), got.Prompts)
	}
	if got.Prompts[0].Id != "x" {
		t.Fatalf("expected replaced item id x, got %q", got.Prompts[0].Id)
	}
}

func TestIssuePromptServiceUpdateRejectsEmptyList(t *testing.T) {
	services := setupTestServices(t)
	user := createTestUser(t, services.db, "user-issue-prompt-empty-list")

	if _, err := services.issuePromptService.UpdatePrompts(user.ID, UpdateIssuePromptsRequest{Prompts: nil}); !errors.Is(err, errs.ErrInvalidParams) {
		t.Fatalf("expected ErrInvalidParams for nil prompts, got: %v", err)
	}
	if _, err := services.issuePromptService.UpdatePrompts(user.ID, UpdateIssuePromptsRequest{Prompts: []api.IssuePromptItem{}}); !errors.Is(err, errs.ErrInvalidParams) {
		t.Fatalf("expected ErrInvalidParams for empty prompts, got: %v", err)
	}
}

func TestIssuePromptServiceUpdateRejectsBlankFields(t *testing.T) {
	services := setupTestServices(t)
	user := createTestUser(t, services.db, "user-issue-prompt-blank")

	cases := []struct {
		name  string
		items []api.IssuePromptItem
	}{
		{
			name:  "blank id",
			items: []api.IssuePromptItem{{Id: "  ", Name: "默认", Content: "正文"}},
		},
		{
			name:  "blank name",
			items: []api.IssuePromptItem{{Id: "id", Name: "  ", Content: "正文"}},
		},
		{
			name:  "blank content",
			items: []api.IssuePromptItem{{Id: "id", Name: "默认", Content: "  "}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := services.issuePromptService.UpdatePrompts(user.ID, UpdateIssuePromptsRequest{Prompts: tc.items})
			if !errors.Is(err, errs.ErrInvalidParams) {
				t.Fatalf("expected ErrInvalidParams for %s, got: %v", tc.name, err)
			}
		})
	}

	// 校验仅用于判空、原值照存：包含前后空白的合法值应写入成功且原样保留。
	kept := []api.IssuePromptItem{{Id: " id ", Name: " 名称 ", Content: " 正文 "}}
	if _, err := services.issuePromptService.UpdatePrompts(user.ID, UpdateIssuePromptsRequest{Prompts: kept}); err != nil {
		t.Fatalf("expected trim-only validation to accept whitespace-bearing values: %v", err)
	}
	got, err := services.issuePromptService.GetPrompts(user.ID)
	if err != nil {
		t.Fatalf("get prompts: %v", err)
	}
	if got.Prompts[0].Id != " id " || got.Prompts[0].Name != " 名称 " || got.Prompts[0].Content != " 正文 " {
		t.Fatalf("expected values preserved verbatim, got: %#v", got.Prompts[0])
	}
}

func TestIssuePromptServiceCreatedAtStableAcrossUpdates(t *testing.T) {
	services := setupTestServices(t)
	user := createTestUser(t, services.db, "user-issue-prompt-createdat")

	first := []api.IssuePromptItem{{Id: "a", Name: "A", Content: "正文 A"}}
	if _, err := services.issuePromptService.UpdatePrompts(user.ID, UpdateIssuePromptsRequest{Prompts: first}); err != nil {
		t.Fatalf("first update: %v", err)
	}

	before, err := services.userIssuePromptRepo.Get(user.ID)
	if err != nil {
		t.Fatalf("get before: %v", err)
	}
	firstCreated := before.CreatedAt
	firstUpdated := before.UpdatedAt
	if firstCreated.IsZero() {
		t.Fatalf("expected non-zero created_at after first update")
	}

	// 等待时钟推进，确保 updated_at 可区分。
	time.Sleep(10 * time.Millisecond)

	second := []api.IssuePromptItem{{Id: "b", Name: "B", Content: "正文 B"}}
	if _, err := services.issuePromptService.UpdatePrompts(user.ID, UpdateIssuePromptsRequest{Prompts: second}); err != nil {
		t.Fatalf("second update: %v", err)
	}

	after, err := services.userIssuePromptRepo.Get(user.ID)
	if err != nil {
		t.Fatalf("get after: %v", err)
	}
	if !after.CreatedAt.Equal(firstCreated) {
		t.Fatalf("expected created_at unchanged, before=%s after=%s", firstCreated, after.CreatedAt)
	}
	if !after.UpdatedAt.After(firstUpdated) {
		t.Fatalf("expected updated_at to advance, before=%s after=%s", firstUpdated, after.UpdatedAt)
	}
}

func TestIssuePromptServiceGetPromptsRepoError(t *testing.T) {
	// 关闭数据库连接模拟底层错误（非 ErrRecordNotFound）→ ErrInternal。
	services := setupTestServices(t)
	user := createTestUser(t, services.db, "user-issue-prompt-repo-err")

	if _, err := services.issuePromptService.UpdatePrompts(user.ID, UpdateIssuePromptsRequest{
		Prompts: []api.IssuePromptItem{{Id: "a", Name: "A", Content: "正文 A"}},
	}); err != nil {
		t.Fatalf("seed setting: %v", err)
	}

	if err := services.db.Where("user_id = ?", user.ID).Delete(&model.UserIssuePromptSetting{}).Error; err != nil {
		t.Fatalf("delete setting row: %v", err)
	}
	// 让仓库指向已关闭的 sql.DB 触发非 ErrRecordNotFound 错误。
	sqlDB, _ := services.db.DB()
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	if _, err := services.issuePromptService.GetPrompts(user.ID); !errors.Is(err, errs.ErrInternal) {
		t.Fatalf("expected ErrInternal on repo error, got: %v", err)
	}
}

func TestIssuePromptServiceUpdatePromptsExistingReadError(t *testing.T) {
	services := setupTestServices(t)
	user := createTestUser(t, services.db, "user-issue-prompt-update-read-err")

	sqlDB, _ := services.db.DB()
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	_, err := services.issuePromptService.UpdatePrompts(user.ID, UpdateIssuePromptsRequest{
		Prompts: []api.IssuePromptItem{{Id: "a", Name: "A", Content: "正文 A"}},
	})
	if !errors.Is(err, errs.ErrInternal) {
		t.Fatalf("expected ErrInternal on existing-read error, got: %v", err)
	}
}
