package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// 自动化多触发器（INT-118）CRUD 定向测试：全量替换语义（带 id 更新、不带 id
// 新建、未提交的删除、未知/重复 id 400）、校验规则、启用约束与 cron 重排。

// apiTriggerInput 构造触发器输入的便捷函数。
func apiTriggerInput(triggerType model.AutomationTriggerType, mutate func(*api.AutomationTriggerInput)) api.AutomationTriggerInput {
	in := api.AutomationTriggerInput{TriggerType: api.AutomationTriggerType(triggerType)}
	if mutate != nil {
		mutate(&in)
	}
	return in
}

// createEnvForAPI 建一个属于该用户、可通过 service 校验的 T3 环境。
func (e *automationTestEnv) createEnvForAPI(t *testing.T, userID string) string {
	t.Helper()
	now := time.Now().UTC()
	envID := uuid.NewString()
	if err := e.db.Create(&model.T3Environment{
		ID: envID, UserID: userID, Name: "env-" + uuid.NewString()[:8],
		Address: "https://t3.example.com", BearerTokenEncrypted: []byte("x"),
		BearerExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create env: %v", err)
	}
	return envID
}

func validAutomationRequest(envID string, triggers ...api.AutomationTriggerInput) *CreateAutomationRequest {
	return &CreateAutomationRequest{
		Name:           "多触发器",
		EnvironmentId:  envID,
		T3ProjectId:    "t3-proj",
		PromptTemplate: "处理 {{issue_title}}",
		Triggers:       triggers,
	}
}

func TestAutomationTriggers_CreateStoresAllTriggers(t *testing.T) {
	env := setupAutomationTestEnv(t)
	user := env.createUser(t)
	envID := env.createEnvForAPI(t, user.ID)

	req := validAutomationRequest(envID,
		apiTriggerInput(model.AutomationTriggerCron, func(in *api.AutomationTriggerInput) {
			in.CronExpression = api.Ptr("0 9 * * *")
			in.Timezone = api.Ptr("Asia/Shanghai")
		}),
		apiTriggerInput(model.AutomationTriggerIssueStatusChanged, func(in *api.AutomationTriggerInput) {
			in.EventFilter = &api.AutomationEventFilter{Field: fieldState("state"), To: api.Ptr("closed")}
		}),
		apiTriggerInput(model.AutomationTriggerGitHubPROpened, func(in *api.AutomationTriggerInput) {
			in.ModelSelections = []api.AutomationModelSelection{{InstanceId: "inst-a", Model: "claude"}}
			in.DebounceSeconds = api.Ptr(120)
		}),
	)
	resp, err := env.svc.Create(context.Background(), user.ID, req)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(resp.Triggers) != 3 {
		t.Fatalf("triggers = %d, want 3", len(resp.Triggers))
	}
	for i, tr := range resp.Triggers {
		if tr.Id == "" {
			t.Fatalf("trigger %d id empty", i)
		}
	}
	cron := resp.Triggers[0]
	if cron.CronExpression == nil || *cron.CronExpression != "0 9 * * *" {
		t.Fatalf("cron expression = %v", cron.CronExpression)
	}
	if cron.NextRunAt == nil {
		t.Fatal("cron trigger must carry recomputed next_run_at")
	}
	if next, err := time.Parse(time.RFC3339Nano, *cron.NextRunAt); err != nil || !next.After(time.Now()) {
		t.Fatalf("next_run_at = %v err=%v, want future", cron.NextRunAt, err)
	}
	issue := resp.Triggers[1]
	if issue.EventFilter == nil || issue.EventFilter.To == nil || *issue.EventFilter.To != "closed" {
		t.Fatalf("issue event filter = %v", issue.EventFilter)
	}
	if issue.CronExpression != nil || issue.NextRunAt != nil {
		t.Fatal("non-cron trigger must not expose cron fields")
	}
	pr := resp.Triggers[2]
	if len(pr.ModelSelections) != 1 || pr.DebounceSeconds == nil || *pr.DebounceSeconds != 120 {
		t.Fatalf("pr trigger = %+v", pr)
	}

	// 落库形状一致（触发器行挂在 automation 上，position 按下标）。
	rows, err := env.triggers.ListByAutomationID(context.Background(), resp.Id)
	if err != nil {
		t.Fatalf("list triggers: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("stored triggers = %d, want 3", len(rows))
	}
}

// PR 触发的防抖缺省 60；非 PR 触发的 PR 字段被清空。
func TestAutomationTriggers_FieldDefaultsAndCrossTypeClearing(t *testing.T) {
	env := setupAutomationTestEnv(t)
	user := env.createUser(t)
	envID := env.createEnvForAPI(t, user.ID)

	req := validAutomationRequest(envID,
		apiTriggerInput(model.AutomationTriggerGitHubPRSynchronize, func(in *api.AutomationTriggerInput) {
			in.ModelSelections = []api.AutomationModelSelection{
				{InstanceId: "inst-a", Model: "claude"},
				{InstanceId: "inst-a", Model: "claude"}, // 重复 → 400
			}
		}),
	)
	if _, err := env.svc.Create(context.Background(), user.ID, req); err == nil {
		t.Fatal("duplicate model selections must be rejected")
	}

	req = validAutomationRequest(envID,
		apiTriggerInput(model.AutomationTriggerGitHubPRSynchronize, func(in *api.AutomationTriggerInput) {
			in.ModelSelections = []api.AutomationModelSelection{{InstanceId: "inst-a", Model: "claude"}}
		}),
		apiTriggerInput(model.AutomationTriggerIssueCreated, func(in *api.AutomationTriggerInput) {
			// 交叉字段会被忽略并清空。
			in.ModelSelections = []api.AutomationModelSelection{{InstanceId: "inst-a", Model: "claude"}}
			in.DebounceSeconds = api.Ptr(30)
			in.CronExpression = api.Ptr("*/5 * * * *")
		}),
	)
	resp, err := env.svc.Create(context.Background(), user.ID, req)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	pr := resp.Triggers[0]
	if pr.DebounceSeconds == nil || *pr.DebounceSeconds != prReviewDebounceDefaultSeconds {
		t.Fatalf("pr debounce = %v, want default 60", pr.DebounceSeconds)
	}
	issue := resp.Triggers[1]
	if issue.ModelSelections != nil || issue.DebounceSeconds != nil {
		t.Fatalf("issue trigger leaked PR fields: %+v", issue)
	}
	if issue.CronExpression != nil || issue.NextRunAt != nil {
		t.Fatalf("issue trigger leaked cron fields: %+v", issue)
	}
}

// cron 触发器不携带交叉字段（契约）：事件过滤字段携带即 400；项目范围合法但
// 不参与时间调度，响应与落库都要清空。
func TestAutomationTriggers_CronClearsCrossFields(t *testing.T) {
	env := setupAutomationTestEnv(t)
	user := env.createUser(t)
	envID := env.createEnvForAPI(t, user.ID)
	project := &model.Project{ID: uuid.NewString(), UserID: user.ID, Name: "范围项目"}
	if err := env.db.Create(project).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}

	// cron + 事件过滤字段 → 400。
	req := validAutomationRequest(envID, apiTriggerInput(model.AutomationTriggerCron, func(in *api.AutomationTriggerInput) {
		in.CronExpression = api.Ptr("0 9 * * *")
		in.EventFilter = &api.AutomationEventFilter{Labels: []string{"bug"}}
	}))
	if _, err := env.svc.Create(context.Background(), user.ID, req); err == nil {
		t.Fatal("cron trigger with event filter must be rejected")
	} else if appErr, ok := err.(*errs.AppError); !ok || appErr.Code != errs.ErrInvalidParams.Code {
		t.Fatalf("cron + event filter error = %v, want 400", err)
	}

	// cron + 项目范围：归属校验仍生效，但值被清空，响应与落库都不带。
	req = validAutomationRequest(envID, apiTriggerInput(model.AutomationTriggerCron, func(in *api.AutomationTriggerInput) {
		in.CronExpression = api.Ptr("0 9 * * *")
		in.IssueProjectId = api.Ptr(project.ID)
	}))
	resp, err := env.svc.Create(context.Background(), user.ID, req)
	if err != nil {
		t.Fatalf("create cron with project scope: %v", err)
	}
	cron := resp.Triggers[0]
	if cron.IssueProjectId != nil {
		t.Fatalf("cron trigger leaked issue_project_id: %s", *cron.IssueProjectId)
	}
	if cron.EventFilter != nil {
		t.Fatalf("cron trigger leaked event_filter: %+v", cron.EventFilter)
	}
	rows, err := env.triggers.ListByAutomationID(context.Background(), resp.Id)
	if err != nil {
		t.Fatalf("list triggers: %v", err)
	}
	for _, row := range rows {
		if row.IssueProjectID != nil || row.EventFilterJSON != "" {
			t.Fatalf("stored cron trigger leaked cross fields: %+v", row)
		}
	}
}

func TestAutomationTriggers_UpdateFullReplace(t *testing.T) {
	env := setupAutomationTestEnv(t)
	user := env.createUser(t)
	envID := env.createEnvForAPI(t, user.ID)

	created, err := env.svc.Create(context.Background(), user.ID, validAutomationRequest(envID,
		apiTriggerInput(model.AutomationTriggerIssueCreated, nil),
		apiTriggerInput(model.AutomationTriggerCron, func(in *api.AutomationTriggerInput) {
			in.CronExpression = api.Ptr("0 9 * * *")
		}),
	))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	keep := created.Triggers[0].Id
	drop := created.Triggers[1].Id

	// 全量替换：保留 issue 触发器（改类型+过滤）、丢弃 cron 触发器、追加 PR 触发器。
	update := &UpdateAutomationRequest{
		Name:           created.Name,
		EnvironmentId:  envID,
		T3ProjectId:    created.T3ProjectId,
		PromptTemplate: created.PromptTemplate,
		Triggers: []api.AutomationTriggerInput{
			apiTriggerInput(model.AutomationTriggerIssueStatusChanged, func(in *api.AutomationTriggerInput) {
				in.Id = api.Ptr(keep)
				in.EventFilter = &api.AutomationEventFilter{Field: fieldState("state")}
			}),
			apiTriggerInput(model.AutomationTriggerGitHubPROpened, func(in *api.AutomationTriggerInput) {
				in.ModelSelections = []api.AutomationModelSelection{{InstanceId: "inst-b", Model: "glm"}}
			}),
		},
	}
	updated, err := env.svc.Update(context.Background(), user.ID, created.Id, update)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(updated.Triggers) != 2 {
		t.Fatalf("triggers after replace = %d, want 2", len(updated.Triggers))
	}
	kept := updated.Triggers[0]
	if kept.Id != keep {
		t.Fatalf("kept trigger id = %s, want %s (id preserved)", kept.Id, keep)
	}
	if kept.TriggerType != api.AutomationTriggerType(model.AutomationTriggerIssueStatusChanged) {
		t.Fatalf("kept trigger type = %s, want updated in place", kept.TriggerType)
	}
	if kept.EventFilter == nil || kept.EventFilter.Field == nil || string(*kept.EventFilter.Field) != "state" {
		t.Fatalf("kept trigger filter = %v, want updated", kept.EventFilter)
	}
	if updated.Triggers[1].Id == drop || updated.Triggers[1].Id == "" {
		t.Fatalf("new trigger id = %q, want fresh", updated.Triggers[1].Id)
	}
	// 被丢弃的触发器（连同它的 cron 字段）从库里消失。
	if n := countRuns(t, env.db, "1=1"); n != 0 {
		t.Fatalf("runs leaked: %d", n)
	}
	rows, err := env.triggers.ListByAutomationID(context.Background(), created.Id)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, row := range rows {
		if row.ID == drop {
			t.Fatal("unsubmitted trigger must be deleted")
		}
		if row.ID == keep && row.TriggerType != model.AutomationTriggerIssueStatusChanged {
			t.Fatalf("kept trigger type = %s, want updated in place", row.TriggerType)
		}
	}
}

// 未知 id / 重复 id → 400。
func TestAutomationTriggers_UpdateRejectsBadIDs(t *testing.T) {
	env := setupAutomationTestEnv(t)
	user := env.createUser(t)
	envID := env.createEnvForAPI(t, user.ID)

	created, err := env.svc.Create(context.Background(), user.ID, validAutomationRequest(envID,
		apiTriggerInput(model.AutomationTriggerIssueCreated, nil),
	))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	base := func() *UpdateAutomationRequest {
		return &UpdateAutomationRequest{
			Name: created.Name, EnvironmentId: envID, T3ProjectId: created.T3ProjectId,
			PromptTemplate: created.PromptTemplate,
			Triggers:       []api.AutomationTriggerInput{apiTriggerInput(model.AutomationTriggerIssueCreated, nil)},
		}
	}

	unknown := base()
	unknown.Triggers[0].Id = api.Ptr("no-such-trigger")
	if _, err := env.svc.Update(context.Background(), user.ID, created.Id, unknown); err == nil {
		t.Fatal("unknown trigger id must be rejected")
	} else if appErr, ok := err.(*errs.AppError); !ok || appErr.Code != errs.ErrInvalidParams.Code {
		t.Fatalf("unknown id error = %v, want 400 code", err)
	}

	dup := base()
	existing := created.Triggers[0].Id
	dup.Triggers[0].Id = api.Ptr(existing)
	dup.Triggers = append(dup.Triggers, apiTriggerInput(model.AutomationTriggerIssueCreated, func(in *api.AutomationTriggerInput) {
		in.Id = api.Ptr(existing)
	}))
	if _, err := env.svc.Update(context.Background(), user.ID, created.Id, dup); err == nil {
		t.Fatal("duplicate trigger id in payload must be rejected")
	}
}

// 启用时至少一个触发器；停用允许 0 个（triggers 必须显式传空数组）。
func TestAutomationTriggers_EnableRequiresAtLeastOne(t *testing.T) {
	env := setupAutomationTestEnv(t)
	user := env.createUser(t)
	envID := env.createEnvForAPI(t, user.ID)

	req := validAutomationRequest(envID)
	req.Enabled = api.Ptr(true)
	if _, err := env.svc.Create(context.Background(), user.ID, req); err == nil {
		t.Fatal("enabled automation without triggers must be rejected")
	}

	req = validAutomationRequest(envID)
	req.Triggers = []api.AutomationTriggerInput{}
	resp, err := env.svc.Create(context.Background(), user.ID, req)
	if err != nil {
		t.Fatalf("create disabled without triggers: %v", err)
	}
	if len(resp.Triggers) != 0 {
		t.Fatalf("triggers = %d, want 0", len(resp.Triggers))
	}
}

// triggers 缺失或 null（nil slice）必须 400：生成类型没有 required 校验，nil
// 会被当成「清空全部触发器」，把整条自动化误删成空配置。
func TestAutomationTriggers_NilTriggersRejected(t *testing.T) {
	env := setupAutomationTestEnv(t)
	user := env.createUser(t)
	envID := env.createEnvForAPI(t, user.ID)

	// Create：不带 triggers / 显式 null → 400。
	createNil := validAutomationRequest(envID)
	createNil.Triggers = nil
	if _, err := env.svc.Create(context.Background(), user.ID, createNil); err == nil {
		t.Fatal("create without triggers must be rejected")
	} else if appErr, ok := err.(*errs.AppError); !ok || appErr.Code != errs.ErrInvalidParams.Code {
		t.Fatalf("nil triggers error = %v, want 400 code", err)
	}

	// Update：带已有触发器的自动化提交 nil triggers → 400 且触发器一条不少。
	created, err := env.svc.Create(context.Background(), user.ID, validAutomationRequest(envID,
		apiTriggerInput(model.AutomationTriggerIssueCreated, nil),
		apiTriggerInput(model.AutomationTriggerIssueLabelAdded, nil),
	))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	update := &UpdateAutomationRequest{
		Name:           "改成新名字",
		EnvironmentId:  envID,
		T3ProjectId:    created.T3ProjectId,
		PromptTemplate: created.PromptTemplate,
		Triggers:       nil,
	}
	if _, err := env.svc.Update(context.Background(), user.ID, created.Id, update); err == nil {
		t.Fatal("update without triggers must be rejected")
	} else if appErr, ok := err.(*errs.AppError); !ok || appErr.Code != errs.ErrInvalidParams.Code {
		t.Fatalf("nil triggers update error = %v, want 400 code", err)
	}
	rows, err := env.triggers.ListByAutomationID(context.Background(), created.Id)
	if err != nil {
		t.Fatalf("list triggers: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("triggers after rejected update = %d, want 2", len(rows))
	}
}

// 显式空数组是合法输入：enabled=false 时 200 并清空全部触发器。
func TestAutomationTriggers_ExplicitEmptyClearsAll(t *testing.T) {
	env := setupAutomationTestEnv(t)
	user := env.createUser(t)
	envID := env.createEnvForAPI(t, user.ID)

	created, err := env.svc.Create(context.Background(), user.ID, validAutomationRequest(envID,
		apiTriggerInput(model.AutomationTriggerIssueCreated, nil),
	))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	update := &UpdateAutomationRequest{
		Name:           created.Name,
		EnvironmentId:  envID,
		T3ProjectId:    created.T3ProjectId,
		PromptTemplate: created.PromptTemplate,
		Enabled:        api.Ptr(false),
		Triggers:       []api.AutomationTriggerInput{},
	}
	updated, err := env.svc.Update(context.Background(), user.ID, created.Id, update)
	if err != nil {
		t.Fatalf("update with empty triggers: %v", err)
	}
	if len(updated.Triggers) != 0 {
		t.Fatalf("triggers after empty-array update = %d, want 0", len(updated.Triggers))
	}
}

// 并发删除（INT-118 审查）：保留 id 的触发器在 id 归属校验之后、事务提交之前
// 被删除，按 id 更新命中 0 行必须回滚整个事务，父自动化（可能 enabled=true）
// 不得带着「启用但零触发器」提交成功。
func TestAutomationTriggers_ConcurrentTriggerDeleteRollsBack(t *testing.T) {
	env := setupAutomationTestEnv(t)
	user := env.createUser(t)
	envID := env.createEnvForAPI(t, user.ID)

	created, err := env.svc.Create(context.Background(), user.ID, validAutomationRequest(envID,
		apiTriggerInput(model.AutomationTriggerIssueCreated, nil),
	))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	keep := created.Triggers[0].Id
	// 绕过 service 直接删行：模拟并发删除在 Update 的 ListByAutomationID 之后发生。
	if err := env.db.Where("id = ?", keep).Delete(&model.AutomationTrigger{}).Error; err != nil {
		t.Fatalf("simulate concurrent delete: %v", err)
	}

	// 直接驱动 applyTriggerDiff：id 归属校验已通过（列表里还有它）之后行消失，
	// 正是 RowsAffected=0 的路径。
	err = env.db.Transaction(func(tx *gorm.DB) error {
		return env.svc.applyTriggerDiff(tx, created.Id, []builtTrigger{{
			ID:   keep,
			Type: model.AutomationTriggerIssueCreated,
		}})
	})
	if !errors.Is(err, errTriggerConcurrentlyModified) {
		t.Fatalf("applyTriggerDiff error = %v, want errTriggerConcurrentlyModified", err)
	}
	// 事务回滚：重建出的新行不存在，误删行也不会「顺手恢复」。
	rows, err := env.triggers.ListByAutomationID(context.Background(), created.Id)
	if err != nil {
		t.Fatalf("list triggers: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("triggers after rolled-back diff = %d, want 0", len(rows))
	}

	// 端到端：先删后提交走未知 id 校验，同样 400 且父自动化未被修改。
	update := &UpdateAutomationRequest{
		Name:           "并发改名",
		EnvironmentId:  envID,
		T3ProjectId:    created.T3ProjectId,
		PromptTemplate: created.PromptTemplate,
		Enabled:        api.Ptr(true),
		Triggers: []api.AutomationTriggerInput{
			apiTriggerInput(model.AutomationTriggerIssueCreated, func(in *api.AutomationTriggerInput) {
				in.Id = api.Ptr(keep)
			}),
		},
	}
	if _, err := env.svc.Update(context.Background(), user.ID, created.Id, update); err == nil {
		t.Fatal("update with deleted trigger id must fail")
	} else if appErr, ok := err.(*errs.AppError); !ok || appErr.Code != errs.ErrInvalidParams.Code {
		t.Fatalf("concurrent delete error = %v, want 400 code", err)
	}
	var row model.Automation
	if err := env.db.Where("id = ?", created.Id).First(&row).Error; err != nil {
		t.Fatalf("load automation: %v", err)
	}
	if row.Name == "并发改名" || row.Enabled {
		t.Fatalf("automation must be untouched after rejected update: name=%q enabled=%v", row.Name, row.Enabled)
	}
}

// 触发器总数上限 20。
func TestAutomationTriggers_MaxTwenty(t *testing.T) {
	env := setupAutomationTestEnv(t)
	user := env.createUser(t)
	envID := env.createEnvForAPI(t, user.ID)

	triggers := make([]api.AutomationTriggerInput, 0, automationTriggersMax+1)
	for i := 0; i <= automationTriggersMax; i++ {
		triggers = append(triggers, apiTriggerInput(model.AutomationTriggerIssueCreated, nil))
	}
	req := validAutomationRequest(envID, triggers...)
	if _, err := env.svc.Create(context.Background(), user.ID, req); err == nil {
		t.Fatal("more than 20 triggers must be rejected")
	}
}

// issue_project_id 必须属于当前用户。
func TestAutomationTriggers_ProjectScopeOwnership(t *testing.T) {
	env := setupAutomationTestEnv(t)
	user := env.createUser(t)
	other := env.createUser(t)
	envID := env.createEnvForAPI(t, user.ID)
	foreignProject := &model.Project{ID: uuid.NewString(), UserID: other.ID, Name: "别人的项目"}
	if err := env.db.Create(foreignProject).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}

	req := validAutomationRequest(envID, apiTriggerInput(model.AutomationTriggerIssueCreated, func(in *api.AutomationTriggerInput) {
		in.IssueProjectId = api.Ptr(foreignProject.ID)
	}))
	if _, err := env.svc.Create(context.Background(), user.ID, req); err == nil {
		t.Fatal("project scope from another user must be rejected")
	}

	ownProject := &model.Project{ID: uuid.NewString(), UserID: user.ID, Name: "自己的项目"}
	if err := env.db.Create(ownProject).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}
	req.Triggers[0].IssueProjectId = api.Ptr(ownProject.ID)
	if _, err := env.svc.Create(context.Background(), user.ID, req); err != nil {
		t.Fatalf("own project scope must pass: %v", err)
	}
}

// cron 保存即重排：更新保留 id 的 cron 触发器时 next_run_at 从当前时间重算。
func TestAutomationTriggers_CronReschedulesOnEverySave(t *testing.T) {
	env := setupAutomationTestEnv(t)
	user := env.createUser(t)
	envID := env.createEnvForAPI(t, user.ID)

	created, err := env.svc.Create(context.Background(), user.ID, validAutomationRequest(envID,
		apiTriggerInput(model.AutomationTriggerCron, func(in *api.AutomationTriggerInput) {
			in.CronExpression = api.Ptr("0 9 * * *")
		}),
	))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	first := created.Triggers[0]

	// 同一触发器原样再保存一次：表达式不变但 next_run_at 仍重算（≥ 第一次）。
	update := &UpdateAutomationRequest{
		Name: created.Name, EnvironmentId: envID, T3ProjectId: created.T3ProjectId,
		PromptTemplate: created.PromptTemplate,
		Triggers: []api.AutomationTriggerInput{
			apiTriggerInput(model.AutomationTriggerCron, func(in *api.AutomationTriggerInput) {
				in.Id = api.Ptr(first.Id)
				in.CronExpression = api.Ptr("0 9 * * *")
			}),
		},
	}
	updated, err := env.svc.Update(context.Background(), user.ID, created.Id, update)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got := updated.Triggers[0].NextRunAt; got == nil {
		t.Fatal("next_run_at missing after save")
	}

	// 非法表达式/非法时区 → 400。
	bad := update
	bad.Triggers[0].CronExpression = api.Ptr("*/10 * * * * *")
	if _, err := env.svc.Update(context.Background(), user.ID, created.Id, bad); err == nil {
		t.Fatal("six-field cron must be rejected")
	}
	bad.Triggers[0].CronExpression = api.Ptr("0 9 * * *")
	bad.Triggers[0].Timezone = api.Ptr("Not/AZone")
	if _, err := env.svc.Update(context.Background(), user.ID, created.Id, bad); err == nil {
		t.Fatal("invalid timezone must be rejected")
	}
}

// 删除带活动运行的触发器：历史运行保留（弱引用），编辑不报错。
func TestAutomationTriggers_DeleteKeepsRunHistory(t *testing.T) {
	env := setupAutomationTestEnv(t)
	user := env.createUser(t)
	envID := env.createEnvForAPI(t, user.ID)

	created, err := env.svc.Create(context.Background(), user.ID, validAutomationRequest(envID,
		apiTriggerInput(model.AutomationTriggerIssueCreated, nil),
		apiTriggerInput(model.AutomationTriggerIssueLabelAdded, nil),
	))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	dropped := created.Triggers[1].Id

	// 直接落一条挂在被删触发器上的历史运行。
	if err := env.db.Create(&model.AutomationRun{
		ID: uuid.NewString(), UserID: user.ID, AutomationID: created.Id,
		TriggerID: dropped, TriggerType: model.AutomationTriggerIssueLabelAdded,
		TriggerKey: "event:" + uuid.NewString(), TriggeredAt: time.Now().UTC(),
		ConfigSnapshotJSON: "{}", EnvironmentID: "env", T3ProjectID: "p",
		ThreadID: uuid.NewString(), LaunchCommandID: uuid.NewString(),
		InitialMessageID: uuid.NewString(), Status: model.AutomationRunCompleted,
		FinishedAt: api.Ptr(time.Now().UTC()), DeadlineAt: time.Now().UTC().Add(time.Hour),
		PostState: model.AutomationPostStateNone,
	}).Error; err != nil {
		t.Fatalf("seed history run: %v", err)
	}

	update := &UpdateAutomationRequest{
		Name: created.Name, EnvironmentId: envID, T3ProjectId: created.T3ProjectId,
		PromptTemplate: created.PromptTemplate,
		Triggers: []api.AutomationTriggerInput{
			apiTriggerInput(model.AutomationTriggerIssueCreated, func(in *api.AutomationTriggerInput) {
				in.Id = api.Ptr(created.Triggers[0].Id)
			}),
		},
	}
	if _, err := env.svc.Update(context.Background(), user.ID, created.Id, update); err != nil {
		t.Fatalf("update dropping trigger with history: %v", err)
	}
	if n := countRuns(t, env.db, "trigger_id = ?", dropped); n != 1 {
		t.Fatalf("history runs = %d, want 1 (weak reference keeps history)", n)
	}
}

// fieldState 构造事件过滤的 field 枚举指针。
func fieldState(v string) *api.AutomationEventFilterField {
	f := api.AutomationEventFilterField(v)
	return &f
}
