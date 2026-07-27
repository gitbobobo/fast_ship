package service

import (
	"errors"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"gorm.io/gorm"
)

type IssuePromptsResponse struct {
	// Prompts 为 nil 时序列化为 JSON null，前端据此兜底为默认提示词。
	Prompts []model.IssuePromptItem `json:"prompts"`
}

type UpdateIssuePromptsRequest struct {
	Prompts []model.IssuePromptItem `json:"prompts"`
}

type IssuePromptService struct {
	repo *repository.UserIssuePromptSettingRepository
}

func NewIssuePromptService(repo *repository.UserIssuePromptSettingRepository) *IssuePromptService {
	return &IssuePromptService{repo: repo}
}

func (s *IssuePromptService) GetPrompts(userID string) (*IssuePromptsResponse, error) {
	setting, err := s.repo.Get(userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &IssuePromptsResponse{Prompts: nil}, nil
		}
		return nil, errs.ErrInternal
	}
	return &IssuePromptsResponse{Prompts: setting.Prompts}, nil
}

func (s *IssuePromptService) UpdatePrompts(userID string, req UpdateIssuePromptsRequest) (*IssuePromptsResponse, error) {
	if len(req.Prompts) < 1 {
		return nil, errs.ErrInvalidParams
	}
	for _, item := range req.Prompts {
		if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.Name) == "" || strings.TrimSpace(item.Content) == "" {
			return nil, errs.ErrInvalidParams
		}
	}

	// created_at 由 GORM 首次插入时自动填充；OnConflict 不更新该列，跨更新自动保留。
	now := time.Now().UTC()
	setting := &model.UserIssuePromptSetting{
		UserID:    userID,
		Prompts:   req.Prompts,
		UpdatedAt: now,
	}

	if err := s.repo.Upsert(setting); err != nil {
		return nil, errs.ErrInternal
	}

	return &IssuePromptsResponse{Prompts: req.Prompts}, nil
}
