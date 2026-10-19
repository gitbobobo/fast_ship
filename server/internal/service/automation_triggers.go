package service

// 触发器聚合逻辑（INT-118）：解析/校验 triggers[]、全量替换差分、行与响应构造。
// 从 automation.go 拆出，保持「自动化本体」与「触发器聚合」两个单元的边界，
// 与 automation_triggers_test.go 对应。

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// requireTriggersAndPublicURL 启用自动化的前置条件：至少一个触发器（400）与
// public_url 已配置（41202）。
func (s *AutomationService) requireTriggersAndPublicURL(ctx context.Context, built []builtTrigger) error {
	if len(built) == 0 {
		return errs.New(errs.ErrInvalidParams.Code, "启用自动化至少需要一个触发器")
	}
	return s.requirePublicURL(ctx)
}

// errTriggerConcurrentlyModified 按 id 更新触发器命中 0 行：id 归属校验之后、
// 事务提交之前该触发器已被并发删除。哨兵错误跨出事务，由 Update 映射为 400。
var errTriggerConcurrentlyModified = errors.New("automation trigger concurrently modified")

// applyTriggerDiff 全量替换触发器：请求里的 id 命中已有行则整行更新（保留
// created_at），不带 id 的新建，未再提交的删除。事务内执行（build 阶段已做
// 未知 id / 重复 id 校验）。
func (s *AutomationService) applyTriggerDiff(tx *gorm.DB, automationID string, built []builtTrigger) error {
	now := time.Now().UTC()
	keep := make(map[string]bool, len(built))
	for i := range built {
		bt := &built[i]
		if bt.ID == "" {
			row := newTriggerRow(automationID, bt, now, now)
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			keep[row.ID] = true
			continue
		}
		keep[bt.ID] = true
		// 按 id 更新必须核对影响行数：0 行 = 触发器已被并发删除，照常提交
		// 会得到「启用但零触发器」的父配置，整事务回滚。
		res := tx.Model(&model.AutomationTrigger{}).Where("id = ?", bt.ID).
			Updates(map[string]any{
				"trigger_type":          bt.Type,
				"position":              bt.Position,
				"cron_expression":       bt.CronExpression,
				"timezone":              bt.Timezone,
				"next_run_at":           bt.NextRunAt,
				"issue_project_id":      bt.IssueProjectID,
				"event_filter_json":     bt.EventFilterJSON,
				"model_selections_json": bt.ModelSelectionsJSON,
				"debounce_seconds":      bt.DebounceSeconds,
				"updated_at":            now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errTriggerConcurrentlyModified
		}
	}
	existing, err := s.triggers.ListByAutomationIDTx(tx, automationID)
	if err != nil {
		return err
	}
	for i := range existing {
		if !keep[existing[i].ID] {
			if err := s.triggers.DeleteTx(tx, existing[i].ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// builtTrigger 是一个触发器输入的归一化产物：交叉字段已清空、cron 已预排
// next_run_at、PR 已校验多模型。ID 为空表示新建，非空表示更新该触发器。
type builtTrigger struct {
	ID                  string
	Type                model.AutomationTriggerType
	CronExpression      string
	Timezone            string
	NextRunAt           *time.Time
	IssueProjectID      *string
	EventFilterJSON     string
	ModelSelectionsJSON string
	DebounceSeconds     int
	Position            int
}

// buildAutomationTriggers 校验并归一化 triggers[]（create/update 共用）。
// existing 非空表示更新语义：带 id 必须命中已有触发器（未知 id 报 400）；
// 触发器总数 ≤ 20；同一 payload 内 id 不可重复。cron 在此解析表达式并从 now
// 重算 next_run_at（存 UTC）；非 cron 清空 cron 字段；非 PR 清空多模型/防抖。
//
// triggers 是必填字段：缺失或 null（nil slice）直接 400——生成类型没有 required
// 校验，nil 会被当成「清空全部触发器」，把整条自动化误删成空配置。
func (s *AutomationService) buildAutomationTriggers(
	userID string, inputs []api.AutomationTriggerInput, existing []model.AutomationTrigger,
) ([]builtTrigger, error) {
	if inputs == nil {
		return nil, errs.New(errs.ErrInvalidParams.Code, "triggers 不能为 null；清空请传空数组")
	}
	if len(inputs) > automationTriggersMax {
		return nil, errs.New(errs.ErrInvalidParams.Code, "一条自动化最多配置 20 个触发器")
	}
	existingByID := make(map[string]struct{}, len(existing))
	for i := range existing {
		existingByID[existing[i].ID] = struct{}{}
	}
	seenIDs := map[string]bool{}
	out := make([]builtTrigger, 0, len(inputs))
	for i := range inputs {
		input := &inputs[i]
		id := strings.TrimSpace(api.Deref(input.Id))
		if id != "" {
			if seenIDs[id] {
				return nil, errs.New(errs.ErrInvalidParams.Code, "触发器 id 重复："+id)
			}
			if _, ok := existingByID[id]; !ok {
				return nil, errs.New(errs.ErrInvalidParams.Code, "未知的触发器 id "+id)
			}
		}
		seenIDs[id] = true

		triggerType := model.AutomationTriggerType(input.TriggerType)
		if !model.IsValidAutomationTriggerType(triggerType) {
			return nil, errs.New(errs.ErrInvalidParams.Code, "未知的触发类型 "+string(triggerType))
		}

		bt := builtTrigger{ID: id, Type: triggerType, Position: i}

		// 项目范围：非空必须属于当前用户（跨用户一律按不存在处理）。
		if pid := strings.TrimSpace(api.Deref(input.IssueProjectId)); pid != "" {
			if _, err := s.projectRepo.FindByID(pid, userID); err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil, errs.ErrProjectNotFound
				}
				return nil, errs.ErrInternal
			}
			bt.IssueProjectID = &pid
		}

		filterJSON := ""
		if input.EventFilter != nil {
			if err := validateAutomationEventFilter(triggerType, input.EventFilter); err != nil {
				return nil, err
			}
			if raw, err := json.Marshal(input.EventFilter); err == nil && string(raw) != "{}" && string(raw) != "null" {
				filterJSON = string(raw)
			} else if err != nil {
				return nil, errs.ErrInvalidParams
			}
		}
		bt.EventFilterJSON = filterJSON

		if triggerType == model.AutomationTriggerCron {
			// 交叉字段清空（契约）：cron 只按时间调度，项目范围与事件过滤不参与，
			// 校验通过也不落库。
			bt.IssueProjectID = nil
			bt.EventFilterJSON = ""
			cronExpression := strings.TrimSpace(api.Deref(input.CronExpression))
			timezone := strings.TrimSpace(api.Deref(input.Timezone))
			_, next, err := parseAutomationCron(cronExpression, timezone, time.Now())
			if err != nil {
				return nil, err
			}
			bt.CronExpression = cronExpression
			bt.Timezone = timezone
			bt.NextRunAt = &next
		}

		if triggerType.IsPRTrigger() {
			// PR 触发不做事件字段过滤；项目范围用 issue_project_id。
			if filterJSON != "" {
				return nil, errs.New(errs.ErrInvalidParams.Code, "GitHub PR 触发不支持事件过滤字段")
			}
			sels, err := normalizePRModelSelections(input.ModelSelections)
			if err != nil {
				return nil, err
			}
			raw, err := json.Marshal(sels)
			if err != nil {
				return nil, errs.ErrInvalidParams
			}
			bt.ModelSelectionsJSON = string(raw)

			debounceSeconds := prReviewDebounceDefaultSeconds
			if input.DebounceSeconds != nil {
				debounceSeconds = *input.DebounceSeconds
			}
			if debounceSeconds < 0 || debounceSeconds > prReviewDebounceMaxSeconds {
				return nil, errs.New(errs.ErrInvalidParams.Code, "防抖窗口须在 0 到 86400 秒之间（0=立即）")
			}
			bt.DebounceSeconds = debounceSeconds
		}

		out = append(out, bt)
	}
	return out, nil
}

// newTriggerRow 由归一化产物构造新建触发器行（仅 bt.ID 为空的新建路径调用，
// id 在此生成；position 取 payload 下标）。
func newTriggerRow(automationID string, bt *builtTrigger, createdAt, updatedAt time.Time) model.AutomationTrigger {
	return model.AutomationTrigger{
		ID:                  uuid.NewString(),
		AutomationID:        automationID,
		TriggerType:         bt.Type,
		Position:            bt.Position,
		CronExpression:      bt.CronExpression,
		Timezone:            bt.Timezone,
		NextRunAt:           bt.NextRunAt,
		IssueProjectID:      bt.IssueProjectID,
		EventFilterJSON:     bt.EventFilterJSON,
		ModelSelectionsJSON: bt.ModelSelectionsJSON,
		DebounceSeconds:     bt.DebounceSeconds,
		CreatedAt:           createdAt,
		UpdatedAt:           updatedAt,
	}
}

// normalizePRModelSelections 校验 PR 触发的多模型选择：1–10 个、每个显式给出
// instance_id/model、(instance_id, model) 不重复。必须显式选择，不能「项目默认」。
func normalizePRModelSelections(sels []api.AutomationModelSelection) ([]api.AutomationModelSelection, error) {
	if len(sels) < prReviewModelsMin {
		return nil, errs.New(errs.ErrInvalidParams.Code, "GitHub PR 触发必须至少选择一个模型")
	}
	if len(sels) > prReviewModelsMax {
		return nil, errs.New(errs.ErrInvalidParams.Code, "GitHub PR 触发最多选择 10 个模型")
	}
	seen := map[string]bool{}
	for i := range sels {
		sel := &sels[i]
		if strings.TrimSpace(sel.InstanceId) == "" || strings.TrimSpace(sel.Model) == "" {
			return nil, errs.New(errs.ErrInvalidParams.Code, "模型选择缺少 instance_id 或 model")
		}
		key := sel.InstanceId + "::" + sel.Model
		if seen[key] {
			return nil, errs.New(errs.ErrInvalidParams.Code, "模型选择重复："+sel.InstanceId+" / "+sel.Model)
		}
		seen[key] = true
	}
	return sels, nil
}

// validateAutomationEventFilter 校验事件过滤结构与触发类型匹配。
func validateAutomationEventFilter(triggerType model.AutomationTriggerType, filter *api.AutomationEventFilter) error {
	if filter == nil {
		return nil
	}
	if triggerType.IsPRTrigger() {
		// GitHub PR 触发不带事件字段过滤（开轮范围只由项目与 action 决定）。
		return errs.New(errs.ErrInvalidParams.Code, "GitHub PR 触发不支持事件过滤字段")
	}
	switch triggerType {
	case model.AutomationTriggerCron:
		// cron 只按时间调度：事件过滤字段不属于它，携带即参数错误。
		if (filter.Field != nil && *filter.Field != "") || len(filter.Labels) > 0 ||
			(filter.From != nil && *filter.From != "") || (filter.To != nil && *filter.To != "") {
			return errs.New(errs.ErrInvalidParams.Code, "cron 触发不支持事件过滤字段")
		}
	case model.AutomationTriggerIssueStatusChanged:
		if filter.Field != nil && *filter.Field != "" && *filter.Field != "state" && *filter.Field != "workflow_status" {
			return errs.New(errs.ErrInvalidParams.Code, "状态过滤 field 只支持 state 或 workflow_status")
		}
		if len(filter.Labels) > 0 {
			return errs.New(errs.ErrInvalidParams.Code, "状态变化过滤不支持 labels")
		}
	case model.AutomationTriggerIssueLabelAdded:
		if filter.Field != nil && *filter.Field != "" {
			return errs.New(errs.ErrInvalidParams.Code, "加标签过滤不支持 field/from/to")
		}
	case model.AutomationTriggerIssueCreated:
		// 新建事件只支持项目范围过滤；带其它过滤字段视为参数错误。
		if (filter.Field != nil && *filter.Field != "") || len(filter.Labels) > 0 ||
			(filter.From != nil && *filter.From != "") || (filter.To != nil && *filter.To != "") {
			return errs.New(errs.ErrInvalidParams.Code, "issue_created 触发不支持事件过滤字段")
		}
	}
	return nil
}

// newAutomationTriggerResponse 触发器响应：数组顺序由调用方按 position 排定。
func newAutomationTriggerResponse(t *model.AutomationTrigger) api.AutomationTrigger {
	resp := api.AutomationTrigger{
		Id:          t.ID,
		TriggerType: api.AutomationTriggerType(t.TriggerType),
		CreatedAt:   t.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	if t.TriggerType == model.AutomationTriggerCron {
		resp.CronExpression = api.NonEmptyPtr(optString(t.CronExpression))
		resp.Timezone = api.NonEmptyPtr(optString(t.Timezone))
		if t.NextRunAt != nil {
			resp.NextRunAt = api.Ptr(t.NextRunAt.UTC().Format(time.RFC3339Nano))
		}
	}
	if t.IssueProjectID != nil {
		resp.IssueProjectId = api.Ptr(*t.IssueProjectID)
	}
	if t.EventFilterJSON != "" {
		var filter AutomationEventFilter
		if json.Unmarshal([]byte(t.EventFilterJSON), &filter) == nil {
			resp.EventFilter = &filter
		}
	}
	if t.TriggerType.IsPRTrigger() {
		if t.ModelSelectionsJSON != "" {
			var sels []AutomationModelSelection
			if json.Unmarshal([]byte(t.ModelSelectionsJSON), &sels) == nil {
				resp.ModelSelections = sels
			}
		}
		resp.DebounceSeconds = api.Ptr(t.DebounceSeconds)
	}
	return resp
}
