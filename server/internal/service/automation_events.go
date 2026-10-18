package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Issue 事件入口（INT-90）：内部 Issue 写路径在自身事务里调用
// EnqueueIssueEventTx 匹配启用的自动化并预写运行行。事件在调用方事务内——
// 事务回滚事件即消失，不存在「Issue 已回滚、线程还被拉起」的错位。
// 不做规则树：匹配只有「项目范围 + 可选过滤」，没有表达式。

// IssueEvent 一次真实 Issue 变化。ID 由本模块生成 UUID：事件身份是这次变化
// 本身，不是「旧值→新值」的内容哈希（同样 from→to 的两次变化是两次事件）。
type IssueEvent struct {
	ID string
	// Type 触发类型（issue_created / issue_status_changed / issue_label_added）。
	Type model.AutomationTriggerType
	// UserID 事件归属用户（= 项目 user_id），事件只匹配该用户的自动化。
	UserID    string
	ProjectID string
	IssueID   string
	// TriggerKey 幂等键（event:<ID>），同一触发器内唯一。
	TriggerKey string
	// StatusField/From/To 仅状态事件；LabelsAdded 仅加标签事件。
	StatusField string
	From        string
	To          string
	LabelsAdded []string
	// IssueSequenceNumber/IssueTitle 供触发上下文与提示词占位（正文派发时再读库）。
	IssueSequenceNumber int
	IssueTitle          string
	OccurredAt          time.Time
}

// IssueEventSink 是 IssueService 依赖的事件出口（由 AutomationService 实现）。
// 接口化便于测试注入假实现；Issue 写路径对自动化无感知。
type IssueEventSink interface {
	EnqueueIssueEventTx(tx *gorm.DB, event IssueEvent) error
}

// EnqueueIssueEventTx 匹配启用的触发器并把运行行写进调用方事务（INT-118 起
// 匹配单位是触发器：父 automation 启用 + 触发器类型 + 项目范围 + 可选过滤）。
// 忙碌（该触发器已有活动运行或后置待执行）不排队：仍记一条 failed/automation_busy
// 的终态运行，用户在运行记录里能看到这次触发被拒。
func (s *AutomationService) EnqueueIssueEventTx(tx *gorm.DB, event IssueEvent) error {
	triggers, err := repository.ListIssueEventTriggersTx(tx, event.UserID, event.Type, event.ProjectID)
	if err != nil {
		return err
	}
	if len(triggers) == 0 {
		return nil
	}
	// 父自动化批量读（快照与环境字段都在父行）。
	autoIDs := make([]string, 0, len(triggers))
	for i := range triggers {
		autoIDs = append(autoIDs, triggers[i].AutomationID)
	}
	var autos []model.Automation
	if err := tx.Where("id IN ?", autoIDs).Find(&autos).Error; err != nil {
		return err
	}
	autosByID := make(map[string]model.Automation, len(autos))
	for i := range autos {
		autosByID[autos[i].ID] = autos[i]
	}

	now := event.OccurredAt
	for i := range triggers {
		t := &triggers[i]
		a, ok := autosByID[t.AutomationID]
		if !ok {
			continue // 触发器与自动化并发删除：跳过
		}
		if !eventFilterMatches(t.EventFilterJSON, event) {
			continue
		}

		triggerCtx := triggerContextFromEvent(event)
		contextJSON, err := json.Marshal(triggerCtx)
		if err != nil {
			return err
		}

		busy, err := repository.HasActiveRunByTriggerTx(tx, t.ID)
		if err != nil {
			return err
		}

		if busy {
			run := newAutomationRunRow(&a, t, event, contextJSON, now)
			finished := now
			run.Status = model.AutomationRunFailed
			run.FinishedAt = &finished
			run.ErrorCode = "automation_busy"
			run.ErrorMessage = "自动化已有进行中的运行（或后置操作待执行），本次触发未执行"
			if err := tx.Create(run).Error; err != nil {
				if isRunDuplicateTriggerKey(err) {
					continue
				}
				return err
			}
			if err := publishAutomationBusyTx(s.sink, tx, &a, run); err != nil {
				return err
			}
			continue
		}

		run := newAutomationRunRow(&a, t, event, contextJSON, now)
		if err := tx.Create(run).Error; err != nil {
			// 同事件重放（事务重试）撞 (trigger_id, trigger_key) 唯一约束：
			// 该触发已在库中，跳过而不让调用方事务失败。
			if isRunDuplicateTriggerKey(err) {
				continue
			}
			return err
		}
	}
	return nil
}

// isRunDuplicateTriggerKey 识别 (trigger_id, trigger_key) 唯一冲突。
func isRunDuplicateTriggerKey(err error) bool {
	return err != nil &&
		strings.Contains(err.Error(), "UNIQUE constraint failed") &&
		strings.Contains(err.Error(), "trigger_key")
}

// publishAutomationBusyTx 忙碌跳过时发站内通知。dedup 按自动化身份
// （environment + kind + automation_id），同一自动化反复忙碌只续 last_seen。
func publishAutomationBusyTx(sink *NotificationSink, tx *gorm.DB, a *model.Automation, run *model.AutomationRun) error {
	if sink == nil {
		return nil
	}
	return sink.PublishTx(tx, NotificationInput{
		UserID:        a.UserID,
		Kind:          model.NotificationAutomationBusy,
		EnvironmentID: a.EnvironmentID,
		RunID:         run.ID,
		ProblemKey:    a.ID,
		StateKey:      "automation_busy",
		Title:         "自动化触发被跳过",
		Body:          fmt.Sprintf("「%s」已有进行中的运行（或后置操作待执行），本次触发未执行。", a.Name),
	})
}

// newAutomationRunRow 构造一条待派发运行行：trigger_id/trigger_type 取触发器，
// 预分配全部 T3 身份（thread/command/message），状态留空串等待 worker 认领，
// deadline 从 triggered_at 起算。
func newAutomationRunRow(a *model.Automation, t *model.AutomationTrigger, event IssueEvent, contextJSON []byte, now time.Time) *model.AutomationRun {
	return &model.AutomationRun{
		ID:                 uuid.NewString(),
		UserID:             a.UserID,
		AutomationID:       a.ID,
		TriggerID:          t.ID,
		TriggerType:        t.TriggerType,
		TriggerKey:         event.TriggerKey,
		TriggerContextJSON: string(contextJSON),
		TriggeredAt:        now,
		ConfigSnapshotJSON: snapshotJSON(a, t),
		EnvironmentID:      a.EnvironmentID,
		T3ProjectID:        a.T3ProjectID,
		ThreadID:           uuid.NewString(),
		LaunchCommandID:    uuid.NewString(),
		InitialMessageID:   uuid.NewString(),
		Status:             model.RunStatusPendingRun,
		DeadlineAt:         now.Add(time.Duration(a.TimeoutSeconds) * time.Second),
		PostState:          model.AutomationPostStateNone,
	}
}

// marshalJSON 简单转发，统一错误处理点。
func marshalJSON(v any) ([]byte, error) { return json.Marshal(v) }

// jsonUnmarshal 字符串入口（TriggerContextJSON 等以 text 列存储）。
func jsonUnmarshal(raw string, v any) error { return json.Unmarshal([]byte(raw), v) }

// triggerContextFromEvent 把事件裁成落库上下文（不含 issue 正文，派发时再读）。
func triggerContextFromEvent(event IssueEvent) AutomationTriggerContext {
	ctx := AutomationTriggerContext{
		ProjectId:           &event.ProjectID,
		IssueId:             &event.IssueID,
		IssueSequenceNumber: &event.IssueSequenceNumber,
		IssueTitle:          &event.IssueTitle,
	}
	switch event.Type {
	case model.AutomationTriggerIssueStatusChanged:
		ctx.Field = &event.StatusField
		ctx.From = &event.From
		ctx.To = &event.To
	case model.AutomationTriggerIssueLabelAdded:
		ctx.LabelsAdded = event.LabelsAdded
	}
	return ctx
}

// automationConfigSnapshot 是运行创建时的配置快照。派发/回报/后置全部读快照，
// 用户随后修改配置不影响已存在的运行。TriggerID/TriggerType 记录当时的触发器。
type automationConfigSnapshot struct {
	PromptTemplate string                      `json:"prompt_template"`
	ModelSelection json.RawMessage             `json:"model_selection,omitempty"`
	RuntimeMode    string                      `json:"runtime_mode"`
	Workspace      json.RawMessage             `json:"workspace,omitempty"`
	TimeoutSeconds int                         `json:"timeout_seconds"`
	PostAction     model.AutomationPostAction  `json:"post_action"`
	PostOnTimeout  bool                        `json:"post_on_timeout"`
	TriggerID      string                      `json:"trigger_id,omitempty"`
	TriggerType    model.AutomationTriggerType `json:"trigger_type"`
	AutomationName string                      `json:"automation_name"`
}

// snapshotJSON 由 automation + 触发器构造快照（INT-118：触发器拆行后运行行的
// 快照由两者合成；单模型走 automation 的缺省，PR 多模型在调用前已换入
// ModelSelectionJSON）。
func snapshotJSON(a *model.Automation, t *model.AutomationTrigger) string {
	snap := automationConfigSnapshot{
		PromptTemplate: a.PromptTemplate,
		RuntimeMode:    a.RuntimeMode,
		TimeoutSeconds: a.TimeoutSeconds,
		PostAction:     a.PostAction,
		PostOnTimeout:  a.PostOnTimeout,
		TriggerID:      t.ID,
		TriggerType:    t.TriggerType,
		AutomationName: a.Name,
	}
	if a.ModelSelectionJSON != "" {
		snap.ModelSelection = json.RawMessage(a.ModelSelectionJSON)
	}
	if a.WorkspaceJSON != "" {
		snap.Workspace = json.RawMessage(a.WorkspaceJSON)
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		// 快照构造失败不该发生（字段全是可序列化类型）；退化为最小快照。
		raw, _ = json.Marshal(automationConfigSnapshot{
			PromptTemplate: a.PromptTemplate,
			RuntimeMode:    a.RuntimeMode,
			PostAction:     a.PostAction,
			TriggerID:      t.ID,
			TriggerType:    t.TriggerType,
			AutomationName: a.Name,
		})
	}
	return string(raw)
}
