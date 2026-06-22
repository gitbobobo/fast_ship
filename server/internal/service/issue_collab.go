package service

import (
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	collabSuggestionBodyMaxRunes = 4000 // 单条建议正文上限：宽松值，容纳带 Markdown 的说明
	collabMaxSuggestions         = 30   // 单次替换的建议条数上限：宽松值，覆盖大型 issue 的拆解
	collabPlanBodyMaxRunes       = 8000 // 计划正文上限：与 summary 对齐
	collabReviewBodyMaxRunes     = 8000 // 审查正文上限：与 summary 对齐
	collabSummaryBodyMaxRunes    = 8000
	collabMaxCommitIDs           = 20
	collabAgentLogin     string  = "代理"
)

var collabCommitIDRe = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)

type IssueCollabService struct {
	collabRepo  *repository.IssueCollabRepository
	issueRepo   *repository.IssueRepository
	projectRepo *repository.ProjectRepository
	userRepo    *repository.UserRepository
}

func NewIssueCollabService(
	collabRepo *repository.IssueCollabRepository,
	issueRepo *repository.IssueRepository,
	projectRepo *repository.ProjectRepository,
	userRepo *repository.UserRepository,
) *IssueCollabService {
	return &IssueCollabService{
		collabRepo:  collabRepo,
		issueRepo:   issueRepo,
		projectRepo: projectRepo,
		userRepo:    userRepo,
	}
}

type IssueCollabActorResponse struct {
	Kind      string `json:"kind"`
	Login     string `json:"login"`
	AvatarURL string `json:"avatar_url"`
}

type IssueCollabSuggestionResponse struct {
	ID        string                   `json:"id"`
	IssueID   string                   `json:"issue_id"`
	Body      string                   `json:"body"`
	SortOrder int                      `json:"sort_order"`
	Author    IssueCollabActorResponse `json:"author"`
	CreatedAt string                   `json:"created_at"`
	UpdatedAt string                   `json:"updated_at"`
}

type IssueCollabPlanResponse struct {
	IssueID   string                   `json:"issue_id"`
	Body      string                   `json:"body"`
	Author    IssueCollabActorResponse `json:"author"`
	CreatedAt string                   `json:"created_at"`
	UpdatedAt string                   `json:"updated_at"`
}

type IssueCollabReviewResponse struct {
	IssueID   string                   `json:"issue_id"`
	Body      string                   `json:"body"`
	Author    IssueCollabActorResponse `json:"author"`
	CreatedAt string                   `json:"created_at"`
	UpdatedAt string                   `json:"updated_at"`
}

type IssueCollabSummaryResponse struct {
	IssueID   string                   `json:"issue_id"`
	Body      string                   `json:"body"`
	CommitIDs []string                 `json:"commit_ids"`
	Author    IssueCollabActorResponse `json:"author"`
	CreatedAt string                   `json:"created_at"`
	UpdatedAt string                   `json:"updated_at"`
}

type IssueCollabAreaResponse struct {
	Suggestions []IssueCollabSuggestionResponse `json:"suggestions"`
	Plan        *IssueCollabPlanResponse        `json:"plan"`
	Review      *IssueCollabReviewResponse      `json:"review"`
	Summary     *IssueCollabSummaryResponse     `json:"summary"`
}

type IssueCollabSuggestionInput struct {
	Body string `json:"body"`
}

type ReplaceIssueCollabSuggestionsRequest struct {
	Items []IssueCollabSuggestionInput `json:"items"`
}

type UpsertIssueCollabPlanRequest struct {
	Body string `json:"body"`
}

type UpsertIssueCollabReviewRequest struct {
	Body string `json:"body"`
}

type UpsertIssueCollabSummaryRequest struct {
	Body      string   `json:"body"`
	CommitIDs []string `json:"commit_ids"`
}

func (s *IssueCollabService) GetArea(issueID, userID string) (*IssueCollabAreaResponse, error) {
	if _, err := s.ensureAccess(issueID, userID); err != nil {
		return nil, err
	}

	suggestions, err := s.collabRepo.ListSuggestionsByIssueID(issueID)
	if err != nil {
		return nil, errs.ErrInternal
	}

	// plan/review/summary 绝大多数 issue 任意时刻都不存在，必须对 ErrRecordNotFound 兜底为 nil，否则 GET /collab 会高频 500。
	plan, err := s.collabRepo.GetPlan(issueID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errs.ErrInternal
	}
	review, err := s.collabRepo.GetReview(issueID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errs.ErrInternal
	}
	summary, err := s.collabRepo.GetSummary(issueID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errs.ErrInternal
	}

	var sources []collabActorSource
	for _, sug := range suggestions {
		sources = append(sources, collabActorSource{UserID: sug.AuthorUserID, Kind: sug.AuthorKind})
	}
	if plan != nil {
		sources = append(sources, collabActorSource{UserID: plan.AuthorUserID, Kind: plan.AuthorKind})
	}
	if review != nil {
		sources = append(sources, collabActorSource{UserID: review.AuthorUserID, Kind: review.AuthorKind})
	}
	if summary != nil {
		sources = append(sources, collabActorSource{UserID: summary.AuthorUserID, Kind: summary.AuthorKind})
	}
	userMap, err := s.resolveActors(sources)
	if err != nil {
		return nil, errs.ErrInternal
	}

	suggestionResponses := make([]IssueCollabSuggestionResponse, 0, len(suggestions))
	for _, sug := range suggestions {
		suggestionResponses = append(suggestionResponses, s.toSuggestionResponse(sug, userMap))
	}

	var planResponse *IssueCollabPlanResponse
	if plan != nil {
		resp := s.toPlanResponse(*plan, userMap)
		planResponse = &resp
	}
	var reviewResponse *IssueCollabReviewResponse
	if review != nil {
		resp := s.toReviewResponse(*review, userMap)
		reviewResponse = &resp
	}
	var summaryResponse *IssueCollabSummaryResponse
	if summary != nil {
		resp := s.toSummaryResponse(*summary, userMap)
		summaryResponse = &resp
	}

	return &IssueCollabAreaResponse{
		Suggestions: suggestionResponses,
		Plan:        planResponse,
		Review:      reviewResponse,
		Summary:     summaryResponse,
	}, nil
}

func (s *IssueCollabService) ReplaceSuggestions(issueID, userID string, authorKind model.CollabAuthorKind, req ReplaceIssueCollabSuggestionsRequest) ([]IssueCollabSuggestionResponse, error) {
	if _, err := s.ensureAccess(issueID, userID); err != nil {
		return nil, err
	}
	// items == nil（如 "items":null）视为非法，防止误清空；空数组 [] 才允许清空全部建议。
	if req.Items == nil {
		return nil, errs.ErrInvalidParams
	}
	if len(req.Items) > collabMaxSuggestions {
		return nil, errs.ErrInvalidParams
	}

	now := time.Now().UTC()
	suggestions := make([]model.IssueCollabSuggestion, 0, len(req.Items))
	for index, item := range req.Items {
		body := strings.TrimSpace(item.Body)
		if err := validateCollabText(body, 1, collabSuggestionBodyMaxRunes); err != nil {
			return nil, err
		}
		suggestions = append(suggestions, model.IssueCollabSuggestion{
			ID:           uuid.NewString(),
			IssueID:      issueID,
			Body:         body,
			SortOrder:    index,
			AuthorUserID: userID,
			AuthorKind:   authorKind,
			CreatedAt:    now,
			UpdatedAt:    now,
		})
	}

	if err := s.collabRepo.Transaction(func(tx *gorm.DB) error {
		return s.collabRepo.ReplaceSuggestionsTx(tx, issueID, suggestions)
	}); err != nil {
		return nil, errs.ErrInternal
	}

	userMap, err := s.userRepo.ListByIDs([]string{userID})
	if err != nil {
		return nil, errs.ErrInternal
	}
	responses := make([]IssueCollabSuggestionResponse, 0, len(suggestions))
	for _, sug := range suggestions {
		responses = append(responses, s.toSuggestionResponse(sug, userMap))
	}
	return responses, nil
}

// prepareCollabDoc 抽出 plan/review 共同的 upsert 前置流程：鉴权 → 校验 body → 决定 CreatedAt（存在则保留旧值，否则用 now）。
// 返回 trim 后的 body 与应使用的 CreatedAt。
func (s *IssueCollabService) prepareCollabDoc(issueID, userID, body string, maxRunes int, getExisting func(issueID string) (time.Time, error)) (string, time.Time, error) {
	if _, err := s.ensureAccess(issueID, userID); err != nil {
		return "", time.Time{}, err
	}
	trimmed := strings.TrimSpace(body)
	if err := validateCollabText(trimmed, 1, maxRunes); err != nil {
		return "", time.Time{}, err
	}
	now := time.Now().UTC()
	existing, err := getExisting(issueID)
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return "", time.Time{}, errs.ErrInternal
		}
		return trimmed, now, nil
	}
	return trimmed, existing, nil
}

func (s *IssueCollabService) UpsertPlan(issueID, userID string, authorKind model.CollabAuthorKind, req UpsertIssueCollabPlanRequest) (*IssueCollabPlanResponse, error) {
	body, createdAt, err := s.prepareCollabDoc(issueID, userID, req.Body, collabPlanBodyMaxRunes, func(id string) (time.Time, error) {
		existing, err := s.collabRepo.GetPlan(id)
		if err != nil {
			return time.Time{}, err
		}
		return existing.CreatedAt, nil
	})
	if err != nil {
		return nil, err
	}

	plan := &model.IssueCollabPlan{
		IssueID:      issueID,
		Body:         body,
		AuthorUserID: userID,
		AuthorKind:   authorKind,
		CreatedAt:    createdAt,
		UpdatedAt:    time.Now().UTC(),
	}
	if err := s.collabRepo.UpsertPlan(plan); err != nil {
		return nil, errs.ErrInternal
	}

	userMap, err := s.userRepo.ListByIDs([]string{userID})
	if err != nil {
		return nil, errs.ErrInternal
	}
	resp := s.toPlanResponse(*plan, userMap)
	return &resp, nil
}

func (s *IssueCollabService) UpsertReview(issueID, userID string, authorKind model.CollabAuthorKind, req UpsertIssueCollabReviewRequest) (*IssueCollabReviewResponse, error) {
	body, createdAt, err := s.prepareCollabDoc(issueID, userID, req.Body, collabReviewBodyMaxRunes, func(id string) (time.Time, error) {
		existing, err := s.collabRepo.GetReview(id)
		if err != nil {
			return time.Time{}, err
		}
		return existing.CreatedAt, nil
	})
	if err != nil {
		return nil, err
	}

	review := &model.IssueCollabReview{
		IssueID:      issueID,
		Body:         body,
		AuthorUserID: userID,
		AuthorKind:   authorKind,
		CreatedAt:    createdAt,
		UpdatedAt:    time.Now().UTC(),
	}
	if err := s.collabRepo.UpsertReview(review); err != nil {
		return nil, errs.ErrInternal
	}

	userMap, err := s.userRepo.ListByIDs([]string{userID})
	if err != nil {
		return nil, errs.ErrInternal
	}
	resp := s.toReviewResponse(*review, userMap)
	return &resp, nil
}

func (s *IssueCollabService) UpsertSummary(issueID, userID string, authorKind model.CollabAuthorKind, req UpsertIssueCollabSummaryRequest) (*IssueCollabSummaryResponse, error) {
	if _, err := s.ensureAccess(issueID, userID); err != nil {
		return nil, err
	}
	body := strings.TrimSpace(req.Body)
	if err := validateCollabText(body, 1, collabSummaryBodyMaxRunes); err != nil {
		return nil, err
	}
	commitIDs, err := normalizeCollabCommitIDs(req.CommitIDs)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	summary := &model.IssueCollabSummary{
		IssueID:       issueID,
		Body:          body,
		CommitIDsJSON: toJSONString(commitIDs),
		AuthorUserID:  userID,
		AuthorKind:    authorKind,
		UpdatedAt:     now,
	}
	existing, err := s.collabRepo.GetSummary(issueID)
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrInternal
		}
		summary.CreatedAt = now
	} else {
		summary.CreatedAt = existing.CreatedAt
	}
	if err := s.collabRepo.UpsertSummary(summary); err != nil {
		return nil, errs.ErrInternal
	}

	userMap, err := s.userRepo.ListByIDs([]string{userID})
	if err != nil {
		return nil, errs.ErrInternal
	}
	resp := s.toSummaryResponse(*summary, userMap)
	return &resp, nil
}

func (s *IssueCollabService) ClearArea(issueID, userID string) error {
	if _, err := s.ensureAccess(issueID, userID); err != nil {
		return err
	}
	if err := s.collabRepo.DeleteAllByIssueID(issueID); err != nil {
		return errs.ErrInternal
	}
	return nil
}

func (s *IssueCollabService) deleteCollabSection(issueID, userID string, deleteFn func(string) error) error {
	if _, err := s.ensureAccess(issueID, userID); err != nil {
		return err
	}
	if err := deleteFn(issueID); err != nil {
		return errs.ErrInternal
	}
	return nil
}

func (s *IssueCollabService) ClearSuggestions(issueID, userID string) error {
	return s.deleteCollabSection(issueID, userID, s.collabRepo.DeleteSuggestionsByIssueID)
}

func (s *IssueCollabService) DeletePlan(issueID, userID string) error {
	return s.deleteCollabSection(issueID, userID, s.collabRepo.DeletePlanByIssueID)
}

func (s *IssueCollabService) DeleteReview(issueID, userID string) error {
	return s.deleteCollabSection(issueID, userID, s.collabRepo.DeleteReviewByIssueID)
}

func (s *IssueCollabService) DeleteSummary(issueID, userID string) error {
	return s.deleteCollabSection(issueID, userID, s.collabRepo.DeleteSummaryByIssueID)
}

func (s *IssueCollabService) ensureAccess(issueID, userID string) (*model.Issue, error) {
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
		return nil, errs.ErrInternal
	}
	return issue, nil
}

type collabActorSource struct {
	UserID string
	Kind   model.CollabAuthorKind
}

func (s *IssueCollabService) resolveActors(sources []collabActorSource) (map[string]model.User, error) {
	idSet := make(map[string]struct{})
	for _, src := range sources {
		if src.Kind == model.CollabAuthorUser && src.UserID != "" {
			idSet[src.UserID] = struct{}{}
		}
	}
	ids := make([]string, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	return s.userRepo.ListByIDs(ids)
}

func (s *IssueCollabService) buildActor(userID string, kind model.CollabAuthorKind, userMap map[string]model.User) IssueCollabActorResponse {
	if kind == model.CollabAuthorAgent {
		return IssueCollabActorResponse{Kind: string(model.CollabAuthorAgent), Login: collabAgentLogin}
	}
	user, ok := userMap[userID]
	if !ok {
		return IssueCollabActorResponse{Kind: string(model.CollabAuthorUser), Login: "未知用户"}
	}
	return IssueCollabActorResponse{
		Kind:      string(model.CollabAuthorUser),
		Login:     user.Username,
		AvatarURL: user.AvatarURL,
	}
}

func (s *IssueCollabService) toSuggestionResponse(sug model.IssueCollabSuggestion, userMap map[string]model.User) IssueCollabSuggestionResponse {
	return IssueCollabSuggestionResponse{
		ID:        sug.ID,
		IssueID:   sug.IssueID,
		Body:      sug.Body,
		SortOrder: sug.SortOrder,
		Author:    s.buildActor(sug.AuthorUserID, sug.AuthorKind, userMap),
		CreatedAt: formatTime(sug.CreatedAt),
		UpdatedAt: formatTime(sug.UpdatedAt),
	}
}

func (s *IssueCollabService) toPlanResponse(plan model.IssueCollabPlan, userMap map[string]model.User) IssueCollabPlanResponse {
	return IssueCollabPlanResponse{
		IssueID:   plan.IssueID,
		Body:      plan.Body,
		Author:    s.buildActor(plan.AuthorUserID, plan.AuthorKind, userMap),
		CreatedAt: formatTime(plan.CreatedAt),
		UpdatedAt: formatTime(plan.UpdatedAt),
	}
}

func (s *IssueCollabService) toReviewResponse(review model.IssueCollabReview, userMap map[string]model.User) IssueCollabReviewResponse {
	return IssueCollabReviewResponse{
		IssueID:   review.IssueID,
		Body:      review.Body,
		Author:    s.buildActor(review.AuthorUserID, review.AuthorKind, userMap),
		CreatedAt: formatTime(review.CreatedAt),
		UpdatedAt: formatTime(review.UpdatedAt),
	}
}

func (s *IssueCollabService) toSummaryResponse(summary model.IssueCollabSummary, userMap map[string]model.User) IssueCollabSummaryResponse {
	commitIDs := parseJSON[[]string](summary.CommitIDsJSON)
	if commitIDs == nil {
		commitIDs = []string{}
	}
	return IssueCollabSummaryResponse{
		IssueID:   summary.IssueID,
		Body:      summary.Body,
		CommitIDs: commitIDs,
		Author:    s.buildActor(summary.AuthorUserID, summary.AuthorKind, userMap),
		CreatedAt: formatTime(summary.CreatedAt),
		UpdatedAt: formatTime(summary.UpdatedAt),
	}
}

func validateCollabText(value string, minRunes, maxRunes int) error {
	count := utf8.RuneCountInString(value)
	if count < minRunes || count > maxRunes {
		return errs.ErrInvalidParams
	}
	return nil
}

func normalizeCollabCommitIDs(raw []string) ([]string, error) {
	if len(raw) > collabMaxCommitIDs {
		return nil, errs.ErrInvalidParams
	}
	commitIDs := make([]string, 0, len(raw))
	for _, id := range raw {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" {
			return nil, errs.ErrInvalidParams
		}
		if !collabCommitIDRe.MatchString(trimmed) {
			return nil, errs.ErrInvalidParams
		}
		commitIDs = append(commitIDs, trimmed)
	}
	return commitIDs, nil
}

func CollabAuthorKindFromAuth(isJWT bool) model.CollabAuthorKind {
	if isJWT {
		return model.CollabAuthorUser
	}
	return model.CollabAuthorAgent
}
