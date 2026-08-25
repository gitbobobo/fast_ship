package service

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"gorm.io/gorm"
)

const (
	collabBodyMaxRunes        = 8000
	collabAgentLogin   string = "代理"
)

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

type IssueCollabDocResponse struct {
	IssueID   string                   `json:"issue_id"`
	Body      string                   `json:"body"`
	Author    IssueCollabActorResponse `json:"author"`
	CreatedAt string                   `json:"created_at"`
	UpdatedAt string                   `json:"updated_at"`
}

type IssueCollabAreaResponse struct {
	Consensus *IssueCollabDocResponse `json:"consensus"`
	Summary   *IssueCollabDocResponse `json:"summary"`
}

type UpsertIssueCollabRequest struct {
	Body string `json:"body"`
}

func (s *IssueCollabService) GetArea(issueID, userID string) (*IssueCollabAreaResponse, error) {
	if _, err := s.ensureAccess(issueID, userID); err != nil {
		return nil, err
	}

	docs, err := s.collabRepo.ListByIssueID(issueID)
	if err != nil {
		return nil, errs.ErrInternal
	}

	var sources []collabActorSource
	docByKind := make(map[model.CollabDocumentKind]model.IssueCollabDocument, len(docs))
	for _, doc := range docs {
		docByKind[doc.Kind] = doc
		sources = append(sources, collabActorSource{UserID: doc.AuthorUserID, Kind: doc.AuthorKind})
	}
	userMap, err := s.resolveActors(sources)
	if err != nil {
		return nil, errs.ErrInternal
	}

	var consensusResponse *IssueCollabDocResponse
	if doc, ok := docByKind[model.CollabDocumentKindConsensus]; ok {
		resp := s.toDocResponse(doc, userMap)
		consensusResponse = &resp
	}
	var summaryResponse *IssueCollabDocResponse
	if doc, ok := docByKind[model.CollabDocumentKindSummary]; ok {
		resp := s.toDocResponse(doc, userMap)
		summaryResponse = &resp
	}

	return &IssueCollabAreaResponse{
		Consensus: consensusResponse,
		Summary:   summaryResponse,
	}, nil
}

func (s *IssueCollabService) Upsert(issueID, userID string, authorKind model.CollabAuthorKind, kind model.CollabDocumentKind, req UpsertIssueCollabRequest) (*IssueCollabDocResponse, error) {
	if !kind.Valid() {
		return nil, errs.ErrInvalidParams
	}
	if _, err := s.ensureAccess(issueID, userID); err != nil {
		return nil, err
	}

	trimmed := strings.TrimSpace(req.Body)
	if err := validateCollabText(trimmed, 1, collabBodyMaxRunes); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	createdAt := now
	existing, err := s.collabRepo.Get(issueID, kind)
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrInternal
		}
	} else {
		createdAt = existing.CreatedAt
	}

	doc := &model.IssueCollabDocument{
		IssueID:      issueID,
		Kind:         kind,
		Body:         trimmed,
		AuthorUserID: userID,
		AuthorKind:   authorKind,
		CreatedAt:    createdAt,
		UpdatedAt:    now,
	}
	if err := s.collabRepo.Upsert(doc); err != nil {
		return nil, errs.ErrInternal
	}

	userMap, err := s.userRepo.ListByIDs([]string{userID})
	if err != nil {
		return nil, errs.ErrInternal
	}
	resp := s.toDocResponse(*doc, userMap)
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

func (s *IssueCollabService) Delete(issueID, userID string, kind model.CollabDocumentKind) error {
	if !kind.Valid() {
		return errs.ErrInvalidParams
	}
	if _, err := s.ensureAccess(issueID, userID); err != nil {
		return err
	}
	if err := s.collabRepo.Delete(issueID, kind); err != nil {
		return errs.ErrInternal
	}
	return nil
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

func (s *IssueCollabService) toDocResponse(doc model.IssueCollabDocument, userMap map[string]model.User) IssueCollabDocResponse {
	return IssueCollabDocResponse{
		IssueID:   doc.IssueID,
		Body:      doc.Body,
		Author:    s.buildActor(doc.AuthorUserID, doc.AuthorKind, userMap),
		CreatedAt: formatTime(doc.CreatedAt),
		UpdatedAt: formatTime(doc.UpdatedAt),
	}
}

func validateCollabText(value string, minRunes, maxRunes int) error {
	count := utf8.RuneCountInString(value)
	if count < minRunes || count > maxRunes {
		return errs.ErrInvalidParams
	}
	return nil
}
