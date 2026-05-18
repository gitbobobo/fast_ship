package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
)

func TestAIServiceUpdateAndGetSettings(t *testing.T) {
	services := setupTestServices(t)
	user := createTestUser(t, services.db, "user-ai-settings")

	updated, err := services.aiService.UpdateSettings(user.ID, UpdateAISettingsRequest{
		APIHost: "https://api.minimaxi.com",
		APIKey:  "sk-api-test",
		Model:   "MiniMax-M2.5",
	})
	if err != nil {
		t.Fatalf("update settings: %v", err)
	}
	if !updated.Configured {
		t.Fatalf("expected configured=true")
	}

	current, err := services.aiService.GetSettings(user.ID)
	if err != nil {
		t.Fatalf("get settings: %v", err)
	}
	if current.APIHost != "https://api.minimaxi.com" {
		t.Fatalf("unexpected api_host: %q", current.APIHost)
	}
	if current.Model != "MiniMax-M2.5" {
		t.Fatalf("unexpected model: %q", current.Model)
	}
	if !current.Configured {
		t.Fatalf("expected configured=true from get settings")
	}
}

func TestAIServiceSuggestIssueChecklist(t *testing.T) {
	services := setupTestServices(t)
	user := createTestUser(t, services.db, "user-ai-suggest")
	project := createTestProject(t, services.db, user.ID)
	issue := createTestIssue(t, services.db, project.ID, func(item *model.Issue) {
		item.Title = "启动时闪退"
		item.Body = "打开应用后立即闪退，偶现"
	})

	commentNow := time.Now().UTC()
	if err := services.db.Create(&model.IssueComment{
		ID:              uuid.NewString(),
		IssueID:         issue.ID,
		Source:          model.IssueSourceGitHub,
		GitHubCommentID: 9001,
		Body:            "需要明确复现步骤和影响版本",
		AuthorLogin:     "alice",
		GitHubCreatedAt: commentNow,
		GitHubUpdatedAt: commentNow,
	}).Error; err != nil {
		t.Fatalf("create comment: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/text/chatcompletion_v2" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-api-test" {
			t.Fatalf("unexpected authorization header: %q", got)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"base_resp": { "status_code": 0, "status_msg": "success" },
			"choices": [
				{ "message": { "role": "assistant", "content": "{\"items\":[\"补充复现步骤\",\"确认影响版本\"]}" } }
			]
		}`))
	}))
	defer server.Close()

	services.aiService.httpClient = server.Client()
	if _, err := services.aiService.UpdateSettings(user.ID, UpdateAISettingsRequest{
		APIHost: server.URL,
		APIKey:  "sk-api-test",
		Model:   "MiniMax-M2.5",
	}); err != nil {
		t.Fatalf("update settings: %v", err)
	}

	result, err := services.aiService.SuggestIssueChecklist(context.Background(), issue.ID, user.ID)
	if err != nil {
		t.Fatalf("suggest checklist: %v", err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("expected 2 suggestion items, got %d", len(result.Items))
	}
	if result.Items[0].Title != "补充复现步骤" {
		t.Fatalf("unexpected first suggestion: %q", result.Items[0].Title)
	}
	if result.Items[1].Title != "确认影响版本" {
		t.Fatalf("unexpected second suggestion: %q", result.Items[1].Title)
	}
}

func TestAIServiceGenerateTitle(t *testing.T) {
	services := setupTestServices(t)
	user := createTestUser(t, services.db, "user-ai-gen-title")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"base_resp": { "status_code": 0, "status_msg": "success" },
			"choices": [
				{ "message": { "role": "assistant", "content": "修复登录页面白屏问题\n登录后无法跳转\n登录页面显示异常" } }
			]
		}`))
	}))
	defer server.Close()

	services.aiService.httpClient = server.Client()
	if _, err := services.aiService.UpdateSettings(user.ID, UpdateAISettingsRequest{
		APIHost: server.URL,
		APIKey:  "sk-api-test",
		Model:   "MiniMax-M2.5",
	}); err != nil {
		t.Fatalf("update settings: %v", err)
	}

	result, err := services.aiService.GenerateTitle(context.Background(), "打开应用后登录页面白屏，输入账号密码后无法跳转", user.ID)
	if err != nil {
		t.Fatalf("generate title: %v", err)
	}
	if len(result.Titles) != 3 {
		t.Fatalf("expected 3 titles, got %d", len(result.Titles))
	}
	if result.Titles[0] != "修复登录页面白屏问题" {
		t.Fatalf("unexpected first title: %q", result.Titles[0])
	}
}

func TestAIServiceGenerateTitleSettingsNotFound(t *testing.T) {
	services := setupTestServices(t)
	user := createTestUser(t, services.db, "user-ai-no-settings")

	_, err := services.aiService.GenerateTitle(context.Background(), "这是一段正文内容用于测试", user.ID)
	if err == nil {
		t.Fatalf("expected error for missing settings")
	}
}

func TestAIServiceGenerateTitleAPIError(t *testing.T) {
	services := setupTestServices(t)
	user := createTestUser(t, services.db, "user-ai-gen-title-err")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error": "internal"}`))
	}))
	defer server.Close()

	services.aiService.httpClient = server.Client()
	if _, err := services.aiService.UpdateSettings(user.ID, UpdateAISettingsRequest{
		APIHost: server.URL,
		APIKey:  "sk-api-test",
		Model:   "MiniMax-M2.5",
	}); err != nil {
		t.Fatalf("update settings: %v", err)
	}

	_, err := services.aiService.GenerateTitle(context.Background(), "这是一段足够长的正文内容用于测试标题生成功能", user.ID)
	if err == nil {
		t.Fatalf("expected error for API failure")
	}
}

func TestAIServiceGenerateTitleEmptyContent(t *testing.T) {
	services := setupTestServices(t)
	user := createTestUser(t, services.db, "user-ai-gen-title-empty")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"base_resp": { "status_code": 0, "status_msg": "success" },
			"choices": [
				{ "message": { "role": "assistant", "content": "" } }
			]
		}`))
	}))
	defer server.Close()

	services.aiService.httpClient = server.Client()
	if _, err := services.aiService.UpdateSettings(user.ID, UpdateAISettingsRequest{
		APIHost: server.URL,
		APIKey:  "sk-api-test",
		Model:   "MiniMax-M2.5",
	}); err != nil {
		t.Fatalf("update settings: %v", err)
	}

	_, err := services.aiService.GenerateTitle(context.Background(), "这是一段足够长的正文内容用于测试空内容返回", user.ID)
	if err == nil {
		t.Fatalf("expected error for empty content")
	}
}

func TestAIServiceGenerateTitleOnlyQuotes(t *testing.T) {
	services := setupTestServices(t)
	user := createTestUser(t, services.db, "user-ai-gen-title-quotes")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"base_resp": { "status_code": 0, "status_msg": "success" },
			"choices": [
				{ "message": { "role": "assistant", "content": "\"\"\"\"\"\"" } }
			]
		}`))
	}))
	defer server.Close()

	services.aiService.httpClient = server.Client()
	if _, err := services.aiService.UpdateSettings(user.ID, UpdateAISettingsRequest{
		APIHost: server.URL,
		APIKey:  "sk-api-test",
		Model:   "MiniMax-M2.5",
	}); err != nil {
		t.Fatalf("update settings: %v", err)
	}

	_, err := services.aiService.GenerateTitle(context.Background(), "这是一段足够长的正文内容用于测试纯引号返回", user.ID)
	if err == nil {
		t.Fatalf("expected error for quotes-only content")
	}
}

func TestAIServiceGenerateTitleQuotedTitle(t *testing.T) {
	services := setupTestServices(t)
	user := createTestUser(t, services.db, "user-ai-gen-title-quoted-svc")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"base_resp": { "status_code": 0, "status_msg": "success" },
			"choices": [
				{ "message": { "role": "assistant", "content": "\"修复登录白屏问题\"" } }
			]
		}`))
	}))
	defer server.Close()

	services.aiService.httpClient = server.Client()
	if _, err := services.aiService.UpdateSettings(user.ID, UpdateAISettingsRequest{
		APIHost: server.URL,
		APIKey:  "sk-api-test",
		Model:   "MiniMax-M2.5",
	}); err != nil {
		t.Fatalf("update settings: %v", err)
	}

	result, err := services.aiService.GenerateTitle(context.Background(), "这是一段足够长的正文内容用于测试引号标题", user.ID)
	if err != nil {
		t.Fatalf("generate title: %v", err)
	}
	if len(result.Titles) != 1 {
		t.Fatalf("expected 1 title, got %d", len(result.Titles))
	}
	if result.Titles[0] != "修复登录白屏问题" {
		t.Fatalf("expected quotes stripped, got: %q", result.Titles[0])
	}
}

func TestTruncateAndTrimBody(t *testing.T) {
	short := "hello world"
	if got := truncateAndTrimBody(short, 100); got != short {
		t.Fatalf("expected %q, got %q", short, got)
	}

	long := strings.Repeat("甲", generateTitleBodyMaxChars+500)
	got := truncateAndTrimBody(long, generateTitleBodyMaxChars)
	if utf8.RuneCountInString(got) != generateTitleBodyMaxChars {
		t.Fatalf("expected %d chars, got %d", generateTitleBodyMaxChars, utf8.RuneCountInString(got))
	}

	withSpaces := "  " + strings.Repeat("甲", 10) + "  "
	if got := truncateAndTrimBody(withSpaces, 100); got != strings.Repeat("甲", 10) {
		t.Fatalf("expected trimmed output, got %q", got)
	}
}

func TestBuildIssueSuggestionPromptLimitsIssueContentByRunes(t *testing.T) {
	issue := &model.Issue{
		Title: strings.Repeat("甲", issueSuggestionTitleMaxChars+500),
		Body:  strings.Repeat("乙", issueSuggestionBodyMaxChars+5000),
	}

	prompt := buildIssueSuggestionPrompt(issue, nil)

	if !utf8.ValidString(prompt) {
		t.Fatalf("expected valid utf-8 prompt")
	}
	if got := utf8.RuneCountInString(prompt); got > issueSuggestionMaxChars {
		t.Fatalf("expected prompt within %d chars, got %d", issueSuggestionMaxChars, got)
	}
	if got := strings.Count(prompt, "甲"); got != issueSuggestionTitleMaxChars {
		t.Fatalf("expected %d title runes, got %d", issueSuggestionTitleMaxChars, got)
	}
	if got := strings.Count(prompt, "乙"); got != issueSuggestionBodyMaxChars {
		t.Fatalf("expected %d body runes, got %d", issueSuggestionBodyMaxChars, got)
	}
}

func TestBuildIssueSuggestionPromptHandlesLongIssueWithComments(t *testing.T) {
	issue := &model.Issue{
		Title: strings.Repeat("题", issueSuggestionMaxChars),
		Body:  strings.Repeat("文", issueSuggestionMaxChars),
	}
	comments := []model.IssueComment{
		{
			AuthorLogin: "alice",
			Body:        strings.Repeat("评", issueSuggestionMaxChars),
		},
	}

	prompt := buildIssueSuggestionPrompt(issue, comments)

	if !utf8.ValidString(prompt) {
		t.Fatalf("expected valid utf-8 prompt")
	}
	if got := utf8.RuneCountInString(prompt); got > issueSuggestionMaxChars {
		t.Fatalf("expected prompt within %d chars, got %d", issueSuggestionMaxChars, got)
	}
	if !strings.Contains(prompt, "评论 1（@alice）") {
		t.Fatalf("expected prompt to include comment entry")
	}
}
