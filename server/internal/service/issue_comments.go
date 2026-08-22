package service

import (
	"context"
	"errors"
	"fmt"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	ghclient "github.com/godbobo/fast_ship/server/internal/pkg/github"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"strings"
	"time"
)

func (s *IssueService) ListComments(issueID, userID string, page, pageSize int) ([]IssueCommentResponse, int64, error) {
	issue, err := s.issueRepo.FindByID(issueID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, 0, errs.ErrIssueNotFound
		}
		return nil, 0, errs.ErrInternal
	}
	if _, err := s.projectRepo.FindByID(issue.ProjectID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, 0, errs.ErrProjectNotFound
		}
		return nil, 0, errs.ErrNotOwner
	}
	comments, total, err := s.commentRepo.List(issueID, page, pageSize)
	if err != nil {
		return nil, 0, errs.ErrInternal
	}

	resp := make([]IssueCommentResponse, 0, len(comments))
	for _, comment := range comments {
		resp = append(resp, toIssueCommentResponse(comment))
	}
	return resp, total, nil
}

func (s *IssueService) CreateInternalComment(issueID, userID string, req CreateInternalIssueCommentRequest, actor string) (*IssueCommentResponse, error) {
	body := strings.TrimSpace(req.Body)
	if body == "" {
		return nil, errs.ErrInvalidParams
	}

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
	if issue.Source == model.IssueSourceGitHub {
		if issue.GitHubMeta == nil {
			return nil, errs.ErrInternal
		}

		project, err := s.projectRepo.FindByID(issue.ProjectID, userID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, errs.ErrProjectNotFound
			}
			return nil, errs.ErrInternal
		}
		tokenBytes, appErr := s.decryptGitHubToken(project)
		if appErr != nil {
			return nil, appErr
		}

		client := s.newClient(string(tokenBytes), project.GithubOwner, project.GithubRepo)
		createdComment, err := client.CreateIssueComment(context.Background(), issue.GitHubMeta.Number, body)
		if err != nil {
			return nil, errs.New(errs.ErrGitHubAPI.Code, fmt.Sprintf("创建 GitHub Issue 评论失败: %v", err))
		}

		comment := buildGitHubIssueCommentModel(issue.ID, createdComment)
		if err := s.commentRepo.Upsert(comment); err != nil {
			return nil, errs.ErrInternal
		}

		now := time.Now().UTC()
		updatedAt := comment.GitHubUpdatedAt
		if updatedAt.IsZero() {
			updatedAt = now
		}
		issue.UpdatedAt = updatedAt
		if err := s.issueRepo.Save(issue); err != nil {
			return nil, errs.ErrInternal
		}

		meta := issue.GitHubMeta
		meta.CommentsCount++
		meta.SyncedAt = now
		if err := s.gitHubMetaRepo.Upsert(meta); err != nil {
			return nil, errs.ErrInternal
		}

		s.logger.Info("issue comment created",
			zap.String("action", "create_comment"),
			zap.String("issue_id", issueID),
			zap.String("user_id", userID),
			zap.String("actor", actor),
			zap.String("source", "github"),
		)
		resp := toIssueCommentResponse(*comment)
		return &resp, nil
	}
	if issue.Source != model.IssueSourceInternal {
		return nil, errs.ErrIssueReadOnly
	}

	user, err := s.userRepo.FindByID(userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrUserNotFound
		}
		return nil, errs.ErrInternal
	}

	commentID, err := s.commentRepo.NextSyntheticCommentID(issueID)
	if err != nil {
		return nil, errs.ErrInternal
	}
	now := time.Now().UTC()
	comment := &model.IssueComment{
		ID:              uuid.NewString(),
		IssueID:         issueID,
		Source:          model.IssueSourceInternal,
		AuthorUserID:    userID,
		GitHubCommentID: commentID,
		Body:            req.Body,
		BodyHTML:        "",
		AuthorLogin:     user.Username,
		AuthorAvatarURL: "",
		GitHubCreatedAt: now,
		GitHubUpdatedAt: now,
	}

	if err := s.commentRepo.Create(comment); err != nil {
		return nil, errs.ErrInternal
	}

	issue.UpdatedAt = now
	if err := s.issueRepo.Save(issue); err != nil {
		return nil, errs.ErrInternal
	}

	s.logger.Info("issue comment created",
		zap.String("action", "create_comment"),
		zap.String("issue_id", issueID),
		zap.String("user_id", userID),
		zap.String("actor", actor),
		zap.String("source", "internal"),
	)
	resp := toIssueCommentResponse(*comment)
	return &resp, nil
}

// CreateInternalCommentIdempotent is used by durable workflows. The key is
// persisted with the local comment after the side effect; the local unique
// constraint and remote marker lookup make retries converge on that effect.
func (s *IssueService) CreateInternalCommentIdempotent(issueID, userID string, req CreateInternalIssueCommentRequest, actor, idempotencyKey string) (*IssueCommentResponse, error) {
	body := strings.TrimSpace(req.Body)
	if body == "" {
		return nil, errs.ErrInvalidParams
	}
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
	if idempotencyKey != "" {
		if existing, findErr := s.commentRepo.FindByIdempotencyKey(issueID, idempotencyKey); findErr == nil {
			resp := toIssueCommentResponse(*existing)
			return &resp, nil
		} else if !errors.Is(findErr, gorm.ErrRecordNotFound) {
			return nil, errs.ErrInternal
		}
	}

	if issue.Source == model.IssueSourceGitHub {
		if issue.GitHubMeta == nil {
			return nil, errs.ErrInternal
		}
		project, err := s.projectRepo.FindByID(issue.ProjectID, userID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, errs.ErrProjectNotFound
			}
			return nil, errs.ErrInternal
		}
		tokenBytes, appErr := s.decryptGitHubToken(project)
		if appErr != nil {
			return nil, appErr
		}
		client := s.newClient(string(tokenBytes), project.GithubOwner, project.GithubRepo)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		commentBody := body
		var createdComment *ghclient.IssueComment
		if idempotencyKey != "" {
			marker := fmt.Sprintf("<!-- fast-ship-hook:%s -->", idempotencyKey)
			for page := 1; createdComment == nil; {
				items, response, listErr := client.ListIssueComments(ctx, issue.GitHubMeta.Number, page, 100)
				if listErr != nil {
					return nil, errs.New(errs.ErrGitHubAPI.Code, fmt.Sprintf("查询 GitHub Issue 评论失败: %v", listErr))
				}
				for _, item := range items {
					if item != nil && strings.Contains(item.GetBody(), marker) {
						createdComment = item
						break
					}
				}
				if response == nil || response.NextPage == 0 {
					break
				}
				page = response.NextPage
			}
			if createdComment == nil {
				commentBody += "\n\n" + marker
			}
		}
		if createdComment == nil {
			createdComment, err = client.CreateIssueComment(ctx, issue.GitHubMeta.Number, commentBody)
			if err != nil {
				return nil, errs.New(errs.ErrGitHubAPI.Code, fmt.Sprintf("创建 GitHub Issue 评论失败: %v", err))
			}
		}
		comment := buildGitHubIssueCommentModel(issue.ID, createdComment)
		comment.IdempotencyKey = idempotencyKey
		if err := s.commentRepo.Upsert(comment); err != nil {
			return nil, errs.ErrInternal
		}
		now := time.Now().UTC()
		updatedAt := comment.GitHubUpdatedAt
		if updatedAt.IsZero() {
			updatedAt = now
		}
		issue.UpdatedAt = updatedAt
		if err := s.issueRepo.Save(issue); err != nil {
			return nil, errs.ErrInternal
		}
		meta := issue.GitHubMeta
		meta.CommentsCount++
		meta.SyncedAt = now
		if err := s.gitHubMetaRepo.Upsert(meta); err != nil {
			return nil, errs.ErrInternal
		}
		resp := toIssueCommentResponse(*comment)
		return &resp, nil
	}
	if issue.Source != model.IssueSourceInternal {
		return nil, errs.ErrIssueReadOnly
	}
	user, err := s.userRepo.FindByID(userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrUserNotFound
		}
		return nil, errs.ErrInternal
	}
	commentID, err := s.commentRepo.NextSyntheticCommentID(issueID)
	if err != nil {
		return nil, errs.ErrInternal
	}
	now := time.Now().UTC()
	comment := &model.IssueComment{
		ID: uuid.NewString(), IssueID: issueID, Source: model.IssueSourceInternal,
		AuthorUserID: userID, GitHubCommentID: commentID, Body: body, AuthorLogin: user.Username,
		GitHubCreatedAt: now, GitHubUpdatedAt: now, IdempotencyKey: idempotencyKey,
	}
	if err := s.commentRepo.Create(comment); err != nil {
		if idempotencyKey != "" {
			if existing, findErr := s.commentRepo.FindByIdempotencyKey(issueID, idempotencyKey); findErr == nil {
				resp := toIssueCommentResponse(*existing)
				return &resp, nil
			}
		}
		return nil, errs.ErrInternal
	}
	issue.UpdatedAt = now
	if err := s.issueRepo.Save(issue); err != nil {
		return nil, errs.ErrInternal
	}
	resp := toIssueCommentResponse(*comment)
	return &resp, nil
}

func (s *IssueService) ListTimeline(issueID, userID string, page, pageSize int) ([]IssueTimelineEventResponse, int64, error) {
	issue, err := s.issueRepo.FindByID(issueID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, 0, errs.ErrIssueNotFound
		}
		return nil, 0, errs.ErrInternal
	}
	if _, err := s.projectRepo.FindByID(issue.ProjectID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, 0, errs.ErrProjectNotFound
		}
		return nil, 0, errs.ErrNotOwner
	}
	if issue.Source != model.IssueSourceGitHub || issue.GitHubMeta == nil {
		return []IssueTimelineEventResponse{}, 0, nil
	}

	events, total, err := s.timelineRepo.List(issueID, false, page, pageSize)
	if err != nil {
		return nil, 0, errs.ErrInternal
	}

	resp := make([]IssueTimelineEventResponse, 0, len(events))
	for _, event := range events {
		resp = append(resp, toIssueTimelineResponse(event))
	}
	return resp, total, nil
}
