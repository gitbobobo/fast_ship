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
