package service

import (
	"context"
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
// 出入参用生成类型。旧 JSON 可能带 supports_batch，反序列化自然忽略（INT-89 移除）。
func toIssuePromptItems(items model.IssuePromptItems) []api.IssuePromptItem {
	if items == nil {
		return nil
	}
	out := make([]api.IssuePromptItem, 0, len(items))
	for _, item := range items {
		out = append(out, api.IssuePromptItem{
			Id:          item.ID,
			Name:        item.Name,
			Content:     item.Content,
			Preferences: toAPIPromptPreferences(item.Preferences),
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
			ID:          item.Id,
			Name:        item.Name,
			Content:     item.Content,
			Preferences: fromAPIPromptPreferences(item.Preferences),
		})
	}
	return out
}

func toAPIPromptPreferences(p *model.IssuePromptPreferences) *api.IssuePromptPreferences {
	if p == nil {
		return nil
	}
	out := &api.IssuePromptPreferences{
		Environment: p.Environment,
		Workspace:   p.Workspace,
	}
	if p.ModelSelection != nil {
		ms := &api.T3ModelSelection{
			InstanceId: p.ModelSelection.InstanceID,
			Model:      p.ModelSelection.Model,
		}
		if len(p.ModelSelection.Options) > 0 {
			ms.Options = make([]api.T3ModelOption, 0, len(p.ModelSelection.Options))
			for _, option := range p.ModelSelection.Options {
				ms.Options = append(ms.Options, api.T3ModelOption{Id: option.ID, Value: option.Value})
			}
		}
		out.ModelSelection = ms
	}
	return out
}

func fromAPIPromptPreferences(p *api.IssuePromptPreferences) *model.IssuePromptPreferences {
	if p == nil {
		return nil
	}
	out := &model.IssuePromptPreferences{
		Environment: p.Environment,
		Workspace:   p.Workspace,
	}
	if p.ModelSelection != nil {
		ms := &model.IssuePromptModelSelection{
			InstanceID: p.ModelSelection.InstanceId,
			Model:      p.ModelSelection.Model,
		}
		if len(p.ModelSelection.Options) > 0 {
			ms.Options = make([]model.IssuePromptModelOption, 0, len(p.ModelSelection.Options))
			for _, option := range p.ModelSelection.Options {
				ms.Options = append(ms.Options, model.IssuePromptModelOption{ID: option.Id, Value: option.Value})
			}
		}
		out.ModelSelection = ms
	}
	return out
}

type IssuePromptService struct {
	repo    *repository.UserIssuePromptSettingRepository
	envRepo *repository.T3EnvironmentRepository
}

func NewIssuePromptService(repo *repository.UserIssuePromptSettingRepository, envRepo *repository.T3EnvironmentRepository) *IssuePromptService {
	return &IssuePromptService{repo: repo, envRepo: envRepo}
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

func (s *IssuePromptService) UpdatePrompts(ctx context.Context, userID string, req UpdateIssuePromptsRequest) (*IssuePromptsResponse, error) {
	if len(req.Prompts) < 1 {
		return nil, errs.ErrInvalidParams
	}
	for _, item := range req.Prompts {
		if strings.TrimSpace(item.Id) == "" || strings.TrimSpace(item.Name) == "" || strings.TrimSpace(item.Content) == "" {
			return nil, errs.ErrInvalidParams
		}
		if err := s.validatePreferences(ctx, userID, item.Preferences); err != nil {
			return nil, err
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

// validatePreferences 校验提示词的 T3 启动偏好：environment 只能是 "auto" 或
// 当前用户已存在的环境 ID；workspace 只能是 "worktree"|"root"；model_selection
// 的 instance_id/model 非空、options 为 {id, value} 且 value 只取 string|bool
// （T3 ProviderOptionSelectionValue）。非法值返回 40001。
func (s *IssuePromptService) validatePreferences(ctx context.Context, userID string, prefs *api.IssuePromptPreferences) error {
	if prefs == nil {
		return nil
	}
	if prefs.Environment != nil {
		if env := *prefs.Environment; env != "auto" {
			if _, err := s.envRepo.FindByIDAndUserID(ctx, env, userID); err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return errs.ErrInvalidParams
				}
				return errs.ErrInternal
			}
		}
	}
	if prefs.Workspace != nil {
		switch *prefs.Workspace {
		case "worktree", "root":
		default:
			return errs.ErrInvalidParams
		}
	}
	if ms := prefs.ModelSelection; ms != nil {
		if strings.TrimSpace(ms.InstanceId) == "" || strings.TrimSpace(ms.Model) == "" {
			return errs.ErrInvalidParams
		}
		for _, option := range ms.Options {
			if strings.TrimSpace(option.Id) == "" {
				return errs.ErrInvalidParams
			}
			switch option.Value.(type) {
			case string, bool:
			default:
				return errs.ErrInvalidParams
			}
		}
	}
	return nil
}

// IssuePromptInput 组装「启动线程」提示词的输入（与 web 端 issue-prompt.ts 同名）。
type IssuePromptInput struct {
	ProjectID string
	IssueID   string
	Content   string
	// Reason 仅推荐场景传入（推荐理由）；其余场景置空。
	Reason string
}

// BuildIssuePrompt 组装发送给 Agent 的完整提示词（INT-89）：用户模板正文在前，
// 系统附加信息以 > 引用块附在结尾——附加块完全由系统生成，用户只编辑模板
// 正文。npx 命令必须带 --skill fast-ship：仓库 skills/ 下还有内部技能，
// 不指定会被一并安装（实测 `npx skills add --list` 发现 3 个技能）。
// Reason 非空才追加推荐理由行（仅推荐场景）。
func BuildIssuePrompt(in IssuePromptInput) string {
	var b strings.Builder
	b.WriteString(strings.TrimRight(in.Content, "\n"))
	b.WriteString("\n\n> 以下信息由 Fast Ship 附加。请使用 fast-ship 技能查询并处理该问题。\n")
	b.WriteString("> 若当前环境未安装 fast-ship 技能，先征得用户同意，再执行：\n")
	b.WriteString("> `npx skills add https://github.com/gitbobobo/fast_ship --skill fast-ship`\n")
	b.WriteString("> 项目ID：" + in.ProjectID + "\n")
	b.WriteString("> 问题ID：" + in.IssueID)
	if reason := strings.TrimSpace(in.Reason); reason != "" {
		// 多行理由逐行补 > 前缀，保持整段仍是合法引用块；标签只出现在首行
		//（与 web 端 issue-prompt.ts 同口径）。
		for i, line := range strings.Split(reason, "\n") {
			if i == 0 {
				b.WriteString("\n> 推荐理由：" + line)
			} else {
				b.WriteString("\n> " + line)
			}
		}
	}
	return b.String()
}
