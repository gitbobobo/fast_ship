package service

import (
	"context"
	"errors"
	"strings"
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
	ctx := context.Background()

	input := []api.IssuePromptItem{
		{Id: "id-1", Name: "默认", Content: "请处理此问题"},
		{Id: "id-2", Name: "详细", Content: "请仔细分析并修复此问题"},
	}
	updated, err := services.issuePromptService.UpdatePrompts(ctx, user.ID, UpdateIssuePromptsRequest{Prompts: input})
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

func TestIssuePromptServicePreferencesRoundTrip(t *testing.T) {
	services := setupTestServices(t)
	user := createTestUser(t, services.db, "user-issue-prompt-prefs")
	ctx := context.Background()

	env := createT3TestEnv(t, services.db, user.ID, "prefs-env")

	full := &api.IssuePromptPreferences{
		Environment: api.Ptr(env.ID),
		Workspace:   api.Ptr("root"),
		ModelSelection: &api.T3ModelSelection{
			InstanceId: "inst-1",
			Model:      "gpt-5",
			Options: []api.T3ModelOption{
				{Id: "reasoning", Value: "high"},
				{Id: "fast", Value: true},
			},
		},
	}
	input := []api.IssuePromptItem{
		{Id: "id-1", Name: "带偏好", Content: "正文", Preferences: full},
		{Id: "id-2", Name: "auto 环境", Content: "正文", Preferences: &api.IssuePromptPreferences{Environment: api.Ptr("auto"), Workspace: api.Ptr("worktree")}},
		{Id: "id-3", Name: "无偏好", Content: "正文"},
	}
	if _, err := services.issuePromptService.UpdatePrompts(ctx, user.ID, UpdateIssuePromptsRequest{Prompts: input}); err != nil {
		t.Fatalf("update prompts: %v", err)
	}

	got, err := services.issuePromptService.GetPrompts(user.ID)
	if err != nil {
		t.Fatalf("get prompts: %v", err)
	}
	if len(got.Prompts) != 3 {
		t.Fatalf("expected 3 prompts persisted, got %d", len(got.Prompts))
	}
	p := got.Prompts[0].Preferences
	if p == nil || api.Deref(p.Environment) != env.ID || api.Deref(p.Workspace) != "root" {
		t.Fatalf("unexpected persisted preferences: %#v", got.Prompts[0].Preferences)
	}
	if p.ModelSelection == nil || p.ModelSelection.InstanceId != "inst-1" || len(p.ModelSelection.Options) != 2 {
		t.Fatalf("unexpected persisted model_selection: %#v", p.ModelSelection)
	}
	if p.ModelSelection.Options[1].Value != true {
		t.Fatalf("expected bool option value preserved, got: %#v", p.ModelSelection.Options[1].Value)
	}
	if got.Prompts[1].Preferences == nil || api.Deref(got.Prompts[1].Preferences.Environment) != "auto" {
		t.Fatalf("unexpected auto-environment preferences: %#v", got.Prompts[1].Preferences)
	}
	if got.Prompts[2].Preferences != nil {
		t.Fatalf("expected nil preferences preserved, got: %#v", got.Prompts[2].Preferences)
	}
}

func TestIssuePromptServiceUpdateRejectsInvalidPreferences(t *testing.T) {
	services := setupTestServices(t)
	user := createTestUser(t, services.db, "user-issue-prompt-prefs-invalid")
	ctx := context.Background()

	otherUser := createTestUser(t, services.db, "user-issue-prompt-prefs-other")
	otherEnv := createT3TestEnv(t, services.db, otherUser.ID, "other-env")

	cases := []struct {
		name  string
		prefs *api.IssuePromptPreferences
	}{
		{name: "environment not exists", prefs: &api.IssuePromptPreferences{Environment: api.Ptr("env-nope")}},
		{name: "environment cross-user", prefs: &api.IssuePromptPreferences{Environment: api.Ptr(otherEnv.ID)}},
		{name: "environment empty", prefs: &api.IssuePromptPreferences{Environment: api.Ptr("")}},
		{name: "workspace invalid", prefs: &api.IssuePromptPreferences{Workspace: api.Ptr("existing_worktree")}},
		{name: "workspace empty", prefs: &api.IssuePromptPreferences{Workspace: api.Ptr("")}},
		{name: "model_selection missing instance_id", prefs: &api.IssuePromptPreferences{ModelSelection: &api.T3ModelSelection{Model: "m"}}},
		{name: "model_selection missing model", prefs: &api.IssuePromptPreferences{ModelSelection: &api.T3ModelSelection{InstanceId: "i"}}},
		{name: "model_selection blank fields", prefs: &api.IssuePromptPreferences{ModelSelection: &api.T3ModelSelection{InstanceId: " ", Model: " "}}},
		{name: "option missing id", prefs: &api.IssuePromptPreferences{ModelSelection: &api.T3ModelSelection{InstanceId: "i", Model: "m", Options: []api.T3ModelOption{{Id: "", Value: "v"}}}}},
		{name: "option numeric value", prefs: &api.IssuePromptPreferences{ModelSelection: &api.T3ModelSelection{InstanceId: "i", Model: "m", Options: []api.T3ModelOption{{Id: "x", Value: float64(1)}}}}},
		{name: "option nil value", prefs: &api.IssuePromptPreferences{ModelSelection: &api.T3ModelSelection{InstanceId: "i", Model: "m", Options: []api.T3ModelOption{{Id: "x", Value: nil}}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := services.issuePromptService.UpdatePrompts(ctx, user.ID, UpdateIssuePromptsRequest{
				Prompts: []api.IssuePromptItem{{Id: "id", Name: "n", Content: "c", Preferences: tc.prefs}},
			})
			if !errors.Is(err, errs.ErrInvalidParams) {
				t.Fatalf("expected ErrInvalidParams for %s, got: %v", tc.name, err)
			}
		})
	}
}

func TestIssuePromptServiceLegacyJSONReads(t *testing.T) {
	services := setupTestServices(t)
	user := createTestUser(t, services.db, "user-issue-prompt-legacy")

	// 旧格式 JSON：含已移除的 supports_batch 字段，反序列化自然忽略，读取不受影响。
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
	if got.Prompts[0].Id != "legacy-1" || got.Prompts[1].Id != "legacy-2" {
		t.Fatalf("unexpected legacy prompts: %#v", got.Prompts)
	}
}

func TestIssuePromptServiceUpdateIsReplaceNotAppend(t *testing.T) {
	services := setupTestServices(t)
	user := createTestUser(t, services.db, "user-issue-prompt-replace")
	ctx := context.Background()

	first := []api.IssuePromptItem{
		{Id: "a", Name: "A", Content: "正文 A"},
		{Id: "b", Name: "B", Content: "正文 B"},
		{Id: "c", Name: "C", Content: "正文 C"},
	}
	if _, err := services.issuePromptService.UpdatePrompts(ctx, user.ID, UpdateIssuePromptsRequest{Prompts: first}); err != nil {
		t.Fatalf("first update: %v", err)
	}

	second := []api.IssuePromptItem{
		{Id: "x", Name: "X", Content: "正文 X"},
	}
	if _, err := services.issuePromptService.UpdatePrompts(ctx, user.ID, UpdateIssuePromptsRequest{Prompts: second}); err != nil {
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
	ctx := context.Background()

	if _, err := services.issuePromptService.UpdatePrompts(ctx, user.ID, UpdateIssuePromptsRequest{Prompts: nil}); !errors.Is(err, errs.ErrInvalidParams) {
		t.Fatalf("expected ErrInvalidParams for nil prompts, got: %v", err)
	}
	if _, err := services.issuePromptService.UpdatePrompts(ctx, user.ID, UpdateIssuePromptsRequest{Prompts: []api.IssuePromptItem{}}); !errors.Is(err, errs.ErrInvalidParams) {
		t.Fatalf("expected ErrInvalidParams for empty prompts, got: %v", err)
	}
}

func TestIssuePromptServiceUpdateRejectsBlankFields(t *testing.T) {
	services := setupTestServices(t)
	user := createTestUser(t, services.db, "user-issue-prompt-blank")
	ctx := context.Background()

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
			_, err := services.issuePromptService.UpdatePrompts(ctx, user.ID, UpdateIssuePromptsRequest{Prompts: tc.items})
			if !errors.Is(err, errs.ErrInvalidParams) {
				t.Fatalf("expected ErrInvalidParams for %s, got: %v", tc.name, err)
			}
		})
	}

	// 校验仅用于判空、原值照存：包含前后空白的合法值应写入成功且原样保留。
	kept := []api.IssuePromptItem{{Id: " id ", Name: " 名称 ", Content: " 正文 "}}
	if _, err := services.issuePromptService.UpdatePrompts(ctx, user.ID, UpdateIssuePromptsRequest{Prompts: kept}); err != nil {
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
	ctx := context.Background()

	first := []api.IssuePromptItem{{Id: "a", Name: "A", Content: "正文 A"}}
	if _, err := services.issuePromptService.UpdatePrompts(ctx, user.ID, UpdateIssuePromptsRequest{Prompts: first}); err != nil {
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
	if _, err := services.issuePromptService.UpdatePrompts(ctx, user.ID, UpdateIssuePromptsRequest{Prompts: second}); err != nil {
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
	ctx := context.Background()

	if _, err := services.issuePromptService.UpdatePrompts(ctx, user.ID, UpdateIssuePromptsRequest{
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

	_, err := services.issuePromptService.UpdatePrompts(context.Background(), user.ID, UpdateIssuePromptsRequest{
		Prompts: []api.IssuePromptItem{{Id: "a", Name: "A", Content: "正文 A"}},
	})
	if !errors.Is(err, errs.ErrInternal) {
		t.Fatalf("expected ErrInternal on existing-read error, got: %v", err)
	}
}

// TestBuildIssuePrompt 新提示词格式（INT-89）：模板正文在前、引用块在结尾、
// 无 /fast-ship 前缀、npx 命令带 --skill fast-ship、含项目/问题 ID、
// 推荐理由仅推荐场景出现。
func TestBuildIssuePrompt(t *testing.T) {
	cases := []struct {
		name  string
		input IssuePromptInput
		want  string
	}{
		{
			name:  "normal",
			input: IssuePromptInput{ProjectID: "p1", IssueID: "i1", Content: "请处理此问题"},
			want: `请处理此问题

> 以下信息由 Fast Ship 附加。请使用 fast-ship 技能查询并处理该问题。
> 若当前环境未安装 fast-ship 技能，先征得用户同意，再执行：
> ` + "`npx skills add https://github.com/gitbobobo/fast_ship --skill fast-ship`" + `
> 项目ID：p1
> 问题ID：i1`,
		},
		{
			name:  "recommendation with reason",
			input: IssuePromptInput{ProjectID: "p1", IssueID: "i2", Content: "请处理此问题", Reason: "阻塞主链路"},
			want: `请处理此问题

> 以下信息由 Fast Ship 附加。请使用 fast-ship 技能查询并处理该问题。
> 若当前环境未安装 fast-ship 技能，先征得用户同意，再执行：
> ` + "`npx skills add https://github.com/gitbobobo/fast_ship --skill fast-ship`" + `
> 项目ID：p1
> 问题ID：i2
> 推荐理由：阻塞主链路`,
		},
		{
			name:  "multiline reason keeps blockquote",
			input: IssuePromptInput{ProjectID: "p1", IssueID: "i3", Content: "正文", Reason: "阻塞主链路\n依赖 INT-80"},
			want: `正文

> 以下信息由 Fast Ship 附加。请使用 fast-ship 技能查询并处理该问题。
> 若当前环境未安装 fast-ship 技能，先征得用户同意，再执行：
> ` + "`npx skills add https://github.com/gitbobobo/fast_ship --skill fast-ship`" + `
> 项目ID：p1
> 问题ID：i3
> 推荐理由：阻塞主链路
> 依赖 INT-80`,
		},
		{
			name:  "whitespace reason omitted",
			input: IssuePromptInput{ProjectID: "p1", IssueID: "i3", Content: "请处理此问题", Reason: "   "},
			want: `请处理此问题

> 以下信息由 Fast Ship 附加。请使用 fast-ship 技能查询并处理该问题。
> 若当前环境未安装 fast-ship 技能，先征得用户同意，再执行：
> ` + "`npx skills add https://github.com/gitbobobo/fast_ship --skill fast-ship`" + `
> 项目ID：p1
> 问题ID：i3`,
		},
		{
			name:  "trailing newlines trimmed from content",
			input: IssuePromptInput{ProjectID: "p1", IssueID: "i4", Content: "请处理此问题\n\n"},
			want: `请处理此问题

> 以下信息由 Fast Ship 附加。请使用 fast-ship 技能查询并处理该问题。
> 若当前环境未安装 fast-ship 技能，先征得用户同意，再执行：
> ` + "`npx skills add https://github.com/gitbobobo/fast_ship --skill fast-ship`" + `
> 项目ID：p1
> 问题ID：i4`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := BuildIssuePrompt(tc.input)
			if got != tc.want {
				t.Fatalf("unexpected prompt:\n%s", got)
			}
			if strings.HasPrefix(got, "/fast-ship") {
				t.Fatalf("prompt must not carry /fast-ship prefix: %q", got)
			}
			if !strings.Contains(got, "--skill fast-ship") {
				t.Fatalf("prompt must carry --skill fast-ship: %q", got)
			}
			// 附加块是末尾连续 > 行：从「以下信息由 Fast Ship 附加」行起到结尾都应以 > 开头。
			lines := strings.Split(got, "\n")
			blockStart := -1
			for i, line := range lines {
				if strings.HasPrefix(line, "> 以下信息由 Fast Ship 附加") {
					blockStart = i
					break
				}
			}
			if blockStart < 0 {
				t.Fatalf("appended block missing: %q", got)
			}
			for _, line := range lines[blockStart:] {
				if !strings.HasPrefix(line, ">") {
					t.Fatalf("appended block must be a trailing > quote block, line: %q", line)
				}
			}
			hasReason := strings.Contains(got, "推荐理由")
			if strings.TrimSpace(tc.input.Reason) == "" && hasReason {
				t.Fatalf("non-recommendation prompt must omit 推荐理由: %q", got)
			}
			if strings.TrimSpace(tc.input.Reason) != "" && !hasReason {
				t.Fatalf("recommendation prompt must include 推荐理由: %q", got)
			}
		})
	}
}
