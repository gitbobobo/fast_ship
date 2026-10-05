package service

import (
	"errors"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"gorm.io/gorm"
)

// IssuePromptItem ↔ api.IssuePromptItem：存储用 model 形态（gorm valuer/scanner），
// 出入参用生成类型；supports_batch 始终回显以兼容旧输出。
func toIssuePromptItems(items model.IssuePromptItems) []api.IssuePromptItem {
	if items == nil {
		return nil
	}
	out := make([]api.IssuePromptItem, 0, len(items))
	for _, item := range items {
		out = append(out, api.IssuePromptItem{
			Id:            item.ID,
			Name:          item.Name,
			Content:       item.Content,
			SupportsBatch: api.Ptr(item.SupportsBatch),
		})
	}
	return out
}

func fromIssuePromptItems(items []api.IssuePromptItem) model.IssuePromptItems {
	if items == nil {
		return nil
	}
	out := make(model.IssuePromptItems, 0, len(items))
	for _, item := range items {
		out = append(out, model.IssuePromptItem{
			ID:            item.Id,
			Name:          item.Name,
			Content:       item.Content,
			SupportsBatch: api.Deref(item.SupportsBatch),
		})
	}
	return out
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
	return &IssuePromptsResponse{Prompts: toIssuePromptItems(setting.Prompts)}, nil
}

func (s *IssuePromptService) UpdatePrompts(userID string, req UpdateIssuePromptsRequest) (*IssuePromptsResponse, error) {
	if len(req.Prompts) < 1 {
		return nil, errs.ErrInvalidParams
	}
	for _, item := range req.Prompts {
		if strings.TrimSpace(item.Id) == "" || strings.TrimSpace(item.Name) == "" || strings.TrimSpace(item.Content) == "" {
			return nil, errs.ErrInvalidParams
		}
	}

	// created_at 由 GORM 首次插入时自动填充；OnConflict 不更新该列，跨更新自动保留。
	now := time.Now().UTC()
	setting := &model.UserIssuePromptSetting{
		UserID:    userID,
		Prompts:   fromIssuePromptItems(req.Prompts),
		UpdatedAt: now,
	}

	if err := s.repo.Upsert(setting); err != nil {
		return nil, errs.ErrInternal
	}

	return &IssuePromptsResponse{Prompts: req.Prompts}, nil
}
