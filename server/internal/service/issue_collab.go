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
	collabNoteBodyMaxRunes            = 4000
	collabQuestionBodyMaxRunes        = 1000
	collabOptionMaxRunes              = 100
	collabMaxOptions                  = 8
	collabMaxQuestionsPerBatch        = 20
	collabAnswerMaxRunes              = 1000
	collabSummaryBodyMaxRunes         = 8000
	collabMaxCommitIDs                = 20
	collabAgentLogin           string = "代理"
	CollabCustomOptionSentinel        = "__collab_custom__"
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

type IssueCollabNoteResponse struct {
	ID        string                   `json:"id"`
	IssueID   string                   `json:"issue_id"`
	Body      string                   `json:"body"`
	Author    IssueCollabActorResponse `json:"author"`
	CreatedAt string                   `json:"created_at"`
	UpdatedAt string                   `json:"updated_at"`
}

type IssueCollabQuestionAnswerResponse struct {
	Value      string                   `json:"value"`
	Author     IssueCollabActorResponse `json:"author"`
	AnsweredAt string                   `json:"answered_at"`
}

type IssueCollabQuestionResponse struct {
	ID        string                             `json:"id"`
	IssueID   string                             `json:"issue_id"`
	Body      string                             `json:"body"`
	Options   []string                           `json:"options"`
	SortOrder int                                `json:"sort_order"`
	Author    IssueCollabActorResponse           `json:"author"`
	Answer    *IssueCollabQuestionAnswerResponse `json:"answer"`
	CreatedAt string                             `json:"created_at"`
	UpdatedAt string                             `json:"updated_at"`
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
	Notes     []IssueCollabNoteResponse     `json:"notes"`
	Questions []IssueCollabQuestionResponse `json:"questions"`
	Summary   *IssueCollabSummaryResponse   `json:"summary"`
}

type CreateIssueCollabNoteRequest struct {
	Body string `json:"body"`
}

type UpdateIssueCollabNoteRequest struct {
	Body string `json:"body"`
}

type IssueCollabQuestionInput struct {
	Body    string   `json:"body"`
	Options []string `json:"options"`
}

type CreateIssueCollabQuestionsRequest struct {
	Items []IssueCollabQuestionInput `json:"items"`
}

type AnswerIssueCollabQuestionRequest struct {
	Answer string `json:"answer"`
}

type UpsertIssueCollabSummaryRequest struct {
	Body      string   `json:"body"`
	CommitIDs []string `json:"commit_ids"`
}

func (s *IssueCollabService) GetArea(issueID, userID string) (*IssueCollabAreaResponse, error) {
	if _, err := s.ensureAccess(issueID, userID); err != nil {
		return nil, err
	}

	notes, err := s.collabRepo.ListNotesByIssueID(issueID)
	if err != nil {
		return nil, errs.ErrInternal
	}
	questions, err := s.collabRepo.ListQuestionsByIssueID(issueID)
	if err != nil {
		return nil, errs.ErrInternal
	}
	summary, err := s.collabRepo.GetSummary(issueID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errs.ErrInternal
	}

	userMap, err := s.resolveActors(notes, questions, summary)
	if err != nil {
		return nil, errs.ErrInternal
	}

	noteResponses := make([]IssueCollabNoteResponse, 0, len(notes))
	for _, note := range notes {
		noteResponses = append(noteResponses, s.toNoteResponse(note, userMap))
	}

	questionResponses := make([]IssueCollabQuestionResponse, 0, len(questions))
	for _, question := range questions {
		questionResponses = append(questionResponses, s.toQuestionResponse(question, userMap))
	}

	var summaryResponse *IssueCollabSummaryResponse
	if summary != nil {
		summaryResp := s.toSummaryResponse(*summary, userMap)
		summaryResponse = &summaryResp
	}

	return &IssueCollabAreaResponse{
		Notes:     noteResponses,
		Questions: questionResponses,
		Summary:   summaryResponse,
	}, nil
}

func (s *IssueCollabService) CreateNote(issueID, userID string, authorKind model.CollabAuthorKind, req CreateIssueCollabNoteRequest) (*IssueCollabNoteResponse, error) {
	if _, err := s.ensureAccess(issueID, userID); err != nil {
		return nil, err
	}
	body := strings.TrimSpace(req.Body)
	if err := validateCollabText(body, 1, collabNoteBodyMaxRunes); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	note := &model.IssueCollabNote{
		ID:           uuid.NewString(),
		IssueID:      issueID,
		Body:         body,
		AuthorUserID: userID,
		AuthorKind:   authorKind,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.collabRepo.CreateNote(note); err != nil {
		return nil, errs.ErrInternal
	}

	userMap, err := s.userRepo.ListByIDs([]string{userID})
	if err != nil {
		return nil, errs.ErrInternal
	}
	resp := s.toNoteResponse(*note, userMap)
	return &resp, nil
}

func (s *IssueCollabService) UpdateNote(issueID, noteID, userID string, req UpdateIssueCollabNoteRequest) (*IssueCollabNoteResponse, error) {
	if _, err := s.ensureAccess(issueID, userID); err != nil {
		return nil, err
	}
	note, err := s.collabRepo.GetNote(noteID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrIssueCollabNotFound
		}
		return nil, errs.ErrInternal
	}
	if note.IssueID != issueID {
		return nil, errs.ErrIssueCollabNotFound
	}

	body := strings.TrimSpace(req.Body)
	if err := validateCollabText(body, 1, collabNoteBodyMaxRunes); err != nil {
		return nil, err
	}
	if err := s.collabRepo.UpdateNote(noteID, body); err != nil {
		return nil, errs.ErrInternal
	}

	updated, err := s.collabRepo.GetNote(noteID)
	if err != nil {
		return nil, errs.ErrInternal
	}
	userMap, err := s.userRepo.ListByIDs([]string{userID})
	if err != nil {
		return nil, errs.ErrInternal
	}
	resp := s.toNoteResponse(*updated, userMap)
	return &resp, nil
}

func (s *IssueCollabService) DeleteNote(issueID, noteID, userID string) error {
	if _, err := s.ensureAccess(issueID, userID); err != nil {
		return err
	}
	note, err := s.collabRepo.GetNote(noteID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrIssueCollabNotFound
		}
		return errs.ErrInternal
	}
	if note.IssueID != issueID {
		return errs.ErrIssueCollabNotFound
	}
	if err := s.collabRepo.DeleteNote(noteID); err != nil {
		return errs.ErrInternal
	}
	return nil
}

func (s *IssueCollabService) CreateQuestions(issueID, userID string, authorKind model.CollabAuthorKind, req CreateIssueCollabQuestionsRequest) ([]IssueCollabQuestionResponse, error) {
	if _, err := s.ensureAccess(issueID, userID); err != nil {
		return nil, err
	}
	if len(req.Items) == 0 {
		return nil, errs.ErrInvalidParams
	}
	if len(req.Items) > collabMaxQuestionsPerBatch {
		return nil, errs.ErrInvalidParams
	}

	now := time.Now().UTC()
	questions := make([]model.IssueCollabQuestion, 0, len(req.Items))
	for _, item := range req.Items {
		body := strings.TrimSpace(item.Body)
		if err := validateCollabText(body, 1, collabQuestionBodyMaxRunes); err != nil {
			return nil, err
		}
		options, err := normalizeCollabOptions(item.Options)
		if err != nil {
			return nil, err
		}
		questions = append(questions, model.IssueCollabQuestion{
			ID:           uuid.NewString(),
			IssueID:      issueID,
			Body:         body,
			OptionsJSON:  toJSONString(options),
			AuthorUserID: userID,
			AuthorKind:   authorKind,
			CreatedAt:    now,
			UpdatedAt:    now,
		})
	}

	if err := s.collabRepo.Transaction(func(tx *gorm.DB) error {
		return s.collabRepo.CreateQuestionsTx(tx, issueID, questions)
	}); err != nil {
		return nil, errs.ErrInternal
	}

	userMap, err := s.userRepo.ListByIDs([]string{userID})
	if err != nil {
		return nil, errs.ErrInternal
	}
	responses := make([]IssueCollabQuestionResponse, 0, len(questions))
	for _, question := range questions {
		responses = append(responses, s.toQuestionResponse(question, userMap))
	}
	return responses, nil
}

func (s *IssueCollabService) AnswerQuestion(issueID, questionID, userID string, authorKind model.CollabAuthorKind, req AnswerIssueCollabQuestionRequest) (*IssueCollabQuestionResponse, error) {
	if _, err := s.ensureAccess(issueID, userID); err != nil {
		return nil, err
	}
	question, err := s.collabRepo.GetQuestion(questionID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrIssueCollabNotFound
		}
		return nil, errs.ErrInternal
	}
	if question.IssueID != issueID {
		return nil, errs.ErrIssueCollabNotFound
	}

	answer := strings.TrimSpace(req.Answer)
	if err := validateCollabText(answer, 1, collabAnswerMaxRunes); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	if err := s.collabRepo.UpdateAnswer(questionID, answer, userID, authorKind, now); err != nil {
		return nil, errs.ErrInternal
	}

	updated, err := s.collabRepo.GetQuestion(questionID)
	if err != nil {
		return nil, errs.ErrInternal
	}
	userMap, err := s.resolveActors(nil, []model.IssueCollabQuestion{*updated}, nil)
	if err != nil {
		return nil, errs.ErrInternal
	}
	resp := s.toQuestionResponse(*updated, userMap)
	return &resp, nil
}

func (s *IssueCollabService) DeleteQuestion(issueID, questionID, userID string) error {
	if _, err := s.ensureAccess(issueID, userID); err != nil {
		return err
	}
	question, err := s.collabRepo.GetQuestion(questionID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrIssueCollabNotFound
		}
		return errs.ErrInternal
	}
	if question.IssueID != issueID {
		return errs.ErrIssueCollabNotFound
	}
	if err := s.collabRepo.DeleteQuestion(questionID); err != nil {
		return errs.ErrInternal
	}
	return nil
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

func (s *IssueCollabService) resolveActors(notes []model.IssueCollabNote, questions []model.IssueCollabQuestion, summary *model.IssueCollabSummary) (map[string]model.User, error) {
	idSet := make(map[string]struct{})
	collect := func(id string, kind model.CollabAuthorKind) {
		if kind == model.CollabAuthorUser && id != "" {
			idSet[id] = struct{}{}
		}
	}
	for _, note := range notes {
		collect(note.AuthorUserID, note.AuthorKind)
	}
	for _, question := range questions {
		collect(question.AuthorUserID, question.AuthorKind)
		if question.AnsweredAt != nil {
			collect(question.AnswerAuthorUserID, question.AnswerAuthorKind)
		}
	}
	if summary != nil {
		collect(summary.AuthorUserID, summary.AuthorKind)
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

func (s *IssueCollabService) toNoteResponse(note model.IssueCollabNote, userMap map[string]model.User) IssueCollabNoteResponse {
	return IssueCollabNoteResponse{
		ID:        note.ID,
		IssueID:   note.IssueID,
		Body:      note.Body,
		Author:    s.buildActor(note.AuthorUserID, note.AuthorKind, userMap),
		CreatedAt: formatTime(note.CreatedAt),
		UpdatedAt: formatTime(note.UpdatedAt),
	}
}

func (s *IssueCollabService) toQuestionResponse(question model.IssueCollabQuestion, userMap map[string]model.User) IssueCollabQuestionResponse {
	options := parseJSON[[]string](question.OptionsJSON)
	if options == nil {
		options = []string{}
	}

	var answer *IssueCollabQuestionAnswerResponse
	if question.AnsweredAt != nil && question.AnswerValue != "" {
		answer = &IssueCollabQuestionAnswerResponse{
			Value:      question.AnswerValue,
			Author:     s.buildActor(question.AnswerAuthorUserID, question.AnswerAuthorKind, userMap),
			AnsweredAt: formatTime(*question.AnsweredAt),
		}
	}

	return IssueCollabQuestionResponse{
		ID:        question.ID,
		IssueID:   question.IssueID,
		Body:      question.Body,
		Options:   options,
		SortOrder: question.SortOrder,
		Author:    s.buildActor(question.AuthorUserID, question.AuthorKind, userMap),
		Answer:    answer,
		CreatedAt: formatTime(question.CreatedAt),
		UpdatedAt: formatTime(question.UpdatedAt),
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

func normalizeCollabOptions(raw []string) ([]string, error) {
	seen := make(map[string]struct{}, len(raw))
	options := make([]string, 0, len(raw))
	for _, option := range raw {
		trimmed := strings.TrimSpace(option)
		// 拒绝前端"其他(自填)"哨兵，避免与单选项 value 冲突导致无法作答
		if trimmed == "" || trimmed == CollabCustomOptionSentinel {
			return nil, errs.ErrInvalidParams
		}
		if utf8.RuneCountInString(trimmed) > collabOptionMaxRunes {
			return nil, errs.ErrInvalidParams
		}
		if _, ok := seen[trimmed]; ok {
			return nil, errs.ErrInvalidParams
		}
		seen[trimmed] = struct{}{}
		options = append(options, trimmed)
	}
	if len(options) > collabMaxOptions {
		return nil, errs.ErrInvalidParams
	}
	return options, nil
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
