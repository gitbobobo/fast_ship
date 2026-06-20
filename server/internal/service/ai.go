package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	neturl "net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/crypto"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

const (
	defaultMiniMaxAPIHost             = "https://api.minimaxi.com"
	defaultMiniMaxModel               = "MiniMax-M2.5"
	issueSuggestionRequestTimeout     = 30 * time.Second
	issueSuggestionMaxCompletionToken = 800
	issueSuggestionMaxItems           = 12
	issueSuggestionMaxChars           = 24000
	issueSuggestionTitleMaxChars      = 2000
	issueSuggestionBodyMaxChars       = 14000
	generateTitleMaxCompletionToken   = 4096
	generateTitleBodyMaxChars         = 10000
)

var listPrefixRe = regexp.MustCompile(`^\s*\d+[\.)]\s*|^\s*[-*•]\s*`)

type GenerateTitleResponse struct {
	Titles []string `json:"titles"`
}

type AIService struct {
	settingsRepo *repository.UserAISettingRepository
	issueRepo    *repository.IssueRepository
	commentRepo  *repository.IssueCommentRepository
	projectRepo  *repository.ProjectRepository
	cfg          *config.Config
	httpClient   *http.Client
	logger       *zap.Logger
}

type AISettingsResponse struct {
	APIHost    string  `json:"api_host"`
	Model      string  `json:"model"`
	Configured bool    `json:"configured"`
	UpdatedAt  *string `json:"updated_at,omitempty"`
}

type UpdateAISettingsRequest struct {
	APIHost string `json:"api_host"`
	APIKey  string `json:"api_key"`
	Model   string `json:"model"`
}

type IssueChecklistSuggestionsResponse struct {
	Items []IssueChecklistSuggestionItem `json:"items"`
}

type IssueChecklistSuggestionItem struct {
	Title string `json:"title"`
}

type minimaxChatRequest struct {
	Model               string           `json:"model"`
	Messages            []minimaxMessage `json:"messages"`
	Temperature         float64          `json:"temperature,omitempty"`
	MaxCompletionTokens int              `json:"max_completion_tokens,omitempty"`
}

type minimaxMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type minimaxChatResponse struct {
	BaseResp *struct {
		StatusCode int    `json:"status_code"`
		StatusMsg  string `json:"status_msg"`
	} `json:"base_resp"`
	Choices []struct {
		Message minimaxMessage `json:"message"`
	} `json:"choices"`
}

type rawIssueChecklistSuggestions struct {
	Items []string `json:"items"`
}

func NewAIService(
	settingsRepo *repository.UserAISettingRepository,
	issueRepo *repository.IssueRepository,
	commentRepo *repository.IssueCommentRepository,
	projectRepo *repository.ProjectRepository,
	cfg *config.Config,
	logger *zap.Logger,
) *AIService {
	return &AIService{
		settingsRepo: settingsRepo,
		issueRepo:    issueRepo,
		commentRepo:  commentRepo,
		projectRepo:  projectRepo,
		cfg:          cfg,
		logger:       logger,
		httpClient: &http.Client{
			Timeout: issueSuggestionRequestTimeout,
		},
	}
}

func (s *AIService) GetSettings(userID string) (*AISettingsResponse, error) {
	setting, err := s.settingsRepo.Get(userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &AISettingsResponse{
				APIHost:    defaultMiniMaxAPIHost,
				Model:      defaultMiniMaxModel,
				Configured: false,
			}, nil
		}
		return nil, errs.ErrInternal
	}

	resp := &AISettingsResponse{
		APIHost:    setting.APIHost,
		Model:      setting.Model,
		Configured: len(setting.APIKeyEncrypted) > 0,
	}
	if !setting.UpdatedAt.IsZero() {
		value := formatTime(setting.UpdatedAt.UTC())
		resp.UpdatedAt = &value
	}
	return resp, nil
}

func (s *AIService) UpdateSettings(userID string, req UpdateAISettingsRequest) (*AISettingsResponse, error) {
	apiHost := strings.TrimSpace(req.APIHost)
	if apiHost == "" {
		apiHost = defaultMiniMaxAPIHost
	}
	if _, err := neturl.ParseRequestURI(apiHost); err != nil {
		return nil, errs.ErrInvalidParams
	}

	modelName := strings.TrimSpace(req.Model)
	if modelName == "" {
		modelName = defaultMiniMaxModel
	}

	var existing *model.UserAISetting
	existing, err := s.settingsRepo.Get(userID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errs.ErrInternal
	}

	apiKeyPlain := strings.TrimSpace(req.APIKey)
	var encrypted []byte
	switch {
	case apiKeyPlain != "":
		encrypted, err = crypto.Encrypt([]byte(apiKeyPlain), []byte(s.cfg.Encryption.Key))
		if err != nil {
			return nil, errs.ErrInternal
		}
	case existing != nil && len(existing.APIKeyEncrypted) > 0:
		encrypted = existing.APIKeyEncrypted
	default:
		return nil, errs.ErrInvalidParams
	}

	now := time.Now().UTC()
	setting := &model.UserAISetting{
		UserID:          userID,
		APIHost:         apiHost,
		Model:           modelName,
		APIKeyEncrypted: encrypted,
		UpdatedAt:       now,
	}
	if existing != nil && !existing.CreatedAt.IsZero() {
		setting.CreatedAt = existing.CreatedAt
	} else {
		setting.CreatedAt = now
	}

	if err := s.settingsRepo.Upsert(setting); err != nil {
		return nil, errs.ErrInternal
	}

	value := formatTime(now)
	return &AISettingsResponse{
		APIHost:    setting.APIHost,
		Model:      setting.Model,
		Configured: true,
		UpdatedAt:  &value,
	}, nil
}

func (s *AIService) SuggestIssueChecklist(ctx context.Context, issueID, userID, actor string) (*IssueChecklistSuggestionsResponse, error) {
	issue, err := s.issueRepo.FindByID(issueID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrIssueNotFound
		}
		return nil, errs.ErrInternal
	}

	if _, err := s.projectRepo.FindByID(issue.ProjectID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrProjectNotFound
		}
		return nil, errs.ErrNotOwner
	}

	setting, apiKey, err := s.getDecryptedSetting(userID)
	if err != nil {
		return nil, err
	}

	comments, err := s.commentRepo.ListAllByIssueID(issue.ID)
	if err != nil {
		return nil, errs.ErrInternal
	}

	requestPayload := minimaxChatRequest{
		Model: setting.Model,
		Messages: []minimaxMessage{
			{
				Role:    "system",
				Content: "你是一个资深产品与研发协作助手。你需要根据问题标题、正文和评论内容，提炼出适合加入 checklist 的可执行事项。只返回 JSON，不要输出任何额外说明。JSON 格式必须为 {\"items\":[\"事项1\",\"事项2\"]}。每个事项必须是简洁的中文短句，长度不超过 30 个字。",
			},
			{
				Role:    "user",
				Content: buildIssueSuggestionPrompt(issue, comments),
			},
		},
		Temperature:         0.2,
		MaxCompletionTokens: issueSuggestionMaxCompletionToken,
	}

	result, err := s.callMiniMax(ctx, strings.TrimRight(setting.APIHost, "/"), apiKey, requestPayload)
	if err != nil {
		return nil, err
	}

	s.logger.Info("issue checklist suggestions generated",
		zap.String("action", "suggest_checklist"),
		zap.String("issue_id", issueID),
		zap.String("user_id", userID),
		zap.String("actor", actor),
		zap.Int("items", len(result)),
	)
	return &IssueChecklistSuggestionsResponse{Items: result}, nil
}

func (s *AIService) GenerateTitle(ctx context.Context, body, userID string) (*GenerateTitleResponse, error) {
	setting, apiKey, err := s.getDecryptedSetting(userID)
	if err != nil {
		return nil, err
	}

	trimmedBody := truncateAndTrimBody(body, generateTitleBodyMaxChars)

	payload := minimaxChatRequest{
		Model: setting.Model,
		Messages: []minimaxMessage{
			{
				Role:    "system",
				Content: "你是一个资深产品与研发协作助手。你需要根据问题的正文内容，生成 3 个不同风格的中文标题供用户选择。每个标题不超过 50 个字，不要使用引号，不要输出任何额外说明，每行一个标题，只输出标题文本。",
			},
			{
				Role:    "user",
				Content: "请根据以下问题正文，生成 3 个简短的标题：\n\n" + trimmedBody,
			},
		},
		Temperature:         0.7,
		MaxCompletionTokens: generateTitleMaxCompletionToken,
	}

	content, err := s.callMiniMaxRaw(ctx, strings.TrimRight(setting.APIHost, "/"), apiKey, payload)
	if err != nil {
		return nil, err
	}

	var titles []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		line = strings.Trim(line, "\"'")
		line = listPrefixRe.ReplaceAllString(line, "")
		if line != "" {
			titles = append(titles, line)
		}
	}

	if len(titles) == 0 {
		return nil, errs.ErrAIProvider
	}

	return &GenerateTitleResponse{Titles: titles}, nil
}

func (s *AIService) getDecryptedSetting(userID string) (*model.UserAISetting, string, error) {
	setting, err := s.settingsRepo.Get(userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", errs.ErrAISettingsNotFound
		}
		return nil, "", errs.ErrInternal
	}
	if len(setting.APIKeyEncrypted) == 0 {
		return nil, "", errs.ErrAISettingsNotFound
	}

	apiKey, err := crypto.Decrypt(setting.APIKeyEncrypted, []byte(s.cfg.Encryption.Key))
	if err != nil {
		return nil, "", errs.ErrInternal
	}

	return setting, string(apiKey), nil
}

func truncateAndTrimBody(body string, maxChars int) string {
	runes := []rune(body)
	if len(runes) > maxChars {
		runes = runes[:maxChars]
	}
	return strings.TrimSpace(string(runes))
}

func (s *AIService) callMiniMax(
	ctx context.Context,
	apiHost string,
	apiKey string,
	payload minimaxChatRequest,
) ([]IssueChecklistSuggestionItem, error) {
	content, err := s.callMiniMaxRaw(ctx, apiHost, apiKey, payload)
	if err != nil {
		return nil, err
	}

	var raw rawIssueChecklistSuggestions
	if err := json.Unmarshal([]byte(extractJSONObject(content)), &raw); err != nil {
		return nil, errs.ErrAIProvider
	}

	items := make([]IssueChecklistSuggestionItem, 0, min(len(raw.Items), issueSuggestionMaxItems))
	for _, item := range raw.Items {
		title := strings.TrimSpace(item)
		if title == "" {
			continue
		}
		items = append(items, IssueChecklistSuggestionItem{Title: title})
		if len(items) >= issueSuggestionMaxItems {
			break
		}
	}
	return items, nil
}

func (s *AIService) callMiniMaxRaw(
	ctx context.Context,
	apiHost string,
	apiKey string,
	payload minimaxChatRequest,
) (string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", errs.ErrInternal
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiHost+"/v1/text/chatcompletion_v2", bytes.NewReader(body))
	if err != nil {
		return "", errs.ErrInternal
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", errs.ErrAIProvider
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", errs.ErrAIProvider
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", errs.ErrAIProvider
	}

	var decoded minimaxChatResponse
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return "", errs.ErrAIProvider
	}
	if decoded.BaseResp != nil && decoded.BaseResp.StatusCode != 0 {
		return "", errs.ErrAIProvider
	}
	if len(decoded.Choices) == 0 {
		return "", errs.ErrAIProvider
	}

	content := strings.TrimSpace(decoded.Choices[0].Message.Content)
	if content == "" {
		return "", errs.ErrAIProvider
	}

	return content, nil
}

func buildIssueSuggestionPrompt(issue *model.Issue, comments []model.IssueComment) string {
	var builder strings.Builder
	usedChars := 0
	appendText := func(text string, limit int) {
		appendPromptWithinLimit(&builder, &usedChars, text, limit)
	}

	appendText("请阅读下面的问题内容，并输出适合补充到 checklist 的事项列表。\n", issueSuggestionMaxChars)
	appendText("要求：\n", issueSuggestionMaxChars)
	appendText("1. 只输出 JSON。\n", issueSuggestionMaxChars)
	appendText("2. checklist 事项必须是可执行动作。\n", issueSuggestionMaxChars)
	appendText("3. 不要输出原因、来源、优先级。\n", issueSuggestionMaxChars)
	appendText("4. 如果内容不足，也尽量给出 1-5 项最合理的补充事项。\n\n", issueSuggestionMaxChars)
	appendText("标题：\n", issueSuggestionMaxChars)
	appendText(strings.TrimSpace(issue.Title), issueSuggestionTitleMaxChars)
	appendText("\n\n正文：\n", issueSuggestionMaxChars)
	appendText(strings.TrimSpace(issue.Body), issueSuggestionBodyMaxChars)
	appendText("\n\n评论：\n", issueSuggestionMaxChars)

	hasComment := false
	for index, comment := range comments {
		line := strings.TrimSpace(comment.Body)
		if line == "" {
			continue
		}
		hasComment = true
		entry := "- 评论 " + strconv.Itoa(index+1) + "（@" + comment.AuthorLogin + "）：" + line + "\n"
		appendText(entry, issueSuggestionMaxChars)
		if usedChars >= issueSuggestionMaxChars {
			break
		}
	}

	if !hasComment {
		appendText("- 无评论\n", issueSuggestionMaxChars)
	}

	return builder.String()
}

func appendPromptWithinLimit(builder *strings.Builder, usedChars *int, text string, limit int) {
	if limit <= 0 || *usedChars >= issueSuggestionMaxChars || text == "" {
		return
	}

	remaining := issueSuggestionMaxChars - *usedChars
	allowed := min(limit, remaining)
	if allowed <= 0 {
		return
	}

	runes := []rune(text)
	if len(runes) > allowed {
		runes = runes[:allowed]
	}

	builder.WriteString(string(runes))
	*usedChars += len(runes)
}

func extractJSONObject(value string) string {
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(trimmed, "```") {
		trimmed = strings.TrimPrefix(trimmed, "```json")
		trimmed = strings.TrimPrefix(trimmed, "```")
		trimmed = strings.TrimSuffix(trimmed, "```")
		trimmed = strings.TrimSpace(trimmed)
	}

	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start >= 0 && end >= start {
		return trimmed[start : end+1]
	}
	return trimmed
}
