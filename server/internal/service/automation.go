package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// AutomationService 用户可配置自动化（INT-90）的管理面：CRUD、运行记录查询。
// 调度/派发/回报见 automation_scheduler.go、automation_runner.go、automation_report.go；
// Issue 事件入口见 automation_events.go。

// automationCronParser 只接受标准五字段 cron（分 时 日 月 周）。
// 禁止秒字段（不含 Second 位）、@every 等描述符（不含 Descriptor 位）与
// 表达式内时区前缀（CRON_TZ/TZ 解析器不识别，落到解析错误）。
var automationCronParser = cron.NewParser(
	cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow,
)

// 自动化超时边界：缺省 1 小时，可配 1 分钟–24 小时，从 triggered_at 计。
const (
	automationTimeoutDefaultSeconds = 3600
	automationTimeoutMinSeconds     = 60
	automationTimeoutMaxSeconds     = 24 * 3600

	automationNameMaxLen = 200

	// 一条自动化的触发器数量上限（INT-118）。
	automationTriggersMax = 20

	// 后置操作窗口：正常结束先等线程可安全收尾，最多 5 分钟。
	automationPostWindow = 5 * time.Minute

	// 运行令牌在回报截止后仍可用于重放完全相同的结束回报。
	automationRunTokenGrace = 5 * time.Minute
)

// automationPostWaitBudget 后置重试的间隔（线程未安静时下一轮再试）。
const automationPostRetryDelay = 15 * time.Second

// AutomationPromptPlaceholders 模板里可用的占位符（事件触发时替换，cron 触发留空）。
var automationPromptPlaceholders = []string{
	"{{issue_id}}", "{{issue_title}}", "{{issue_body}}", "{{trigger_key}}",
}

// automationPRPromptPlaceholders GitHub PR 评审模板的占位符（webhook 上下文替换）。
var automationPRPromptPlaceholders = []string{
	"{{repo}}", "{{pr_number}}", "{{pr_title}}", "{{pr_url}}", "{{head_sha}}",
}

type AutomationService struct {
	db          *gorm.DB
	autos       *repository.AutomationRepository
	triggers    *repository.AutomationTriggerRepository
	runs        *repository.AutomationRunRepository
	envRepo     *repository.T3EnvironmentRepository
	projectRepo *repository.ProjectRepository
	issueRepo   *repository.IssueRepository
	sink        *NotificationSink
	cfg         *config.Config
	logger      *zap.Logger
	// publicURLSrc 部署 public_url 读取入口（INT-129）：main 注入
	// DeploymentSettingsService；缺省只读 config，保持既有构造与测试行为。
	publicURLSrc PublicURLSource
}

func NewAutomationService(
	db *gorm.DB,
	autos *repository.AutomationRepository,
	triggers *repository.AutomationTriggerRepository,
	runs *repository.AutomationRunRepository,
	envRepo *repository.T3EnvironmentRepository,
	projectRepo *repository.ProjectRepository,
	issueRepo *repository.IssueRepository,
	sink *NotificationSink,
	cfg *config.Config,
	logger *zap.Logger,
) *AutomationService {
	return &AutomationService{
		db: db, autos: autos, triggers: triggers, runs: runs, envRepo: envRepo,
		projectRepo: projectRepo, issueRepo: issueRepo,
		sink: sink, cfg: cfg, logger: logger,
		publicURLSrc: configPublicURLSource{cfg: cfg},
	}
}

// SetPublicURLSource 注入部署 public_url 读取入口（INT-129，main 调用一次）。
func (s *AutomationService) SetPublicURLSource(src PublicURLSource) {
	s.publicURLSrc = src
}

// PublicURLOK 自动化回报端点依赖对外可达的基址；启用/派发前必须已配置。
func (s *AutomationService) PublicURLOK(ctx context.Context) bool {
	return s.publicURLSrc.PublicURL(ctx) != ""
}

func (s *AutomationService) List(ctx context.Context, userID string) ([]AutomationResponse, error) {
	items, err := s.autos.ListByUserID(ctx, userID)
	if err != nil {
		return nil, errs.ErrInternal
	}
	triggers, err := s.triggers.ListByUserIDTx(s.db.WithContext(ctx), userID)
	if err != nil {
		return nil, errs.ErrInternal
	}
	// ListByUserIDTx 已按 automation_id + position 排序，分组后每组保持顺序。
	byAutomation := map[string][]model.AutomationTrigger{}
	for i := range triggers {
		t := &triggers[i]
		byAutomation[t.AutomationID] = append(byAutomation[t.AutomationID], *t)
	}
	out := make([]AutomationResponse, 0, len(items))
	for i := range items {
		out = append(out, newAutomationResponse(&items[i], byAutomation[items[i].ID]))
	}
	return out, nil
}

func (s *AutomationService) Get(ctx context.Context, userID, id string) (*AutomationResponse, error) {
	a, err := s.autos.FindByIDAndUserID(ctx, id, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrAutomationNotFound
		}
		return nil, errs.ErrInternal
	}
	triggers, err := s.triggers.ListByAutomationID(ctx, id)
	if err != nil {
		return nil, errs.ErrInternal
	}
	resp := newAutomationResponse(a, triggers)
	return &resp, nil
}

func (s *AutomationService) ListRuns(ctx context.Context, userID, automationID string, limit int) ([]AutomationRunResponse, error) {
	if _, err := s.autos.FindByIDAndUserID(ctx, automationID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrAutomationNotFound
		}
		return nil, errs.ErrInternal
	}
	items, err := s.runs.ListByAutomationID(ctx, automationID, limit)
	if err != nil {
		return nil, errs.ErrInternal
	}
	out := make([]AutomationRunResponse, 0, len(items))
	for i := range items {
		out = append(out, newAutomationRunResponse(&items[i]))
	}
	return out, nil
}

func (s *AutomationService) GetRun(ctx context.Context, userID, runID string) (*AutomationRunResponse, error) {
	run, err := s.runs.FindByIDAndUserID(ctx, runID, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrAutomationRunNotFound
		}
		return nil, errs.ErrInternal
	}
	resp := newAutomationRunResponse(run)
	return &resp, nil
}

// Create 新建自动化（默认停用、空提示词由 handler 层 zod/必填约束保证）。启用时
// 要求 public_url 已配置且至少一个触发器。自动化与触发器同事务落库。
func (s *AutomationService) Create(ctx context.Context, userID string, req *api.CreateAutomationRequest) (*AutomationResponse, error) {
	built, err := s.buildAutomationConfig(userID, req)
	if err != nil {
		return nil, err
	}
	builtTriggers, err := s.buildAutomationTriggers(userID, req.Triggers, nil)
	if err != nil {
		return nil, err
	}
	if req.Enabled != nil && *req.Enabled {
		if err := s.requireTriggersAndPublicURL(ctx, builtTriggers); err != nil {
			return nil, err
		}
		built.Enabled = true
	} else {
		built.Enabled = false
	}

	now := time.Now().UTC()
	a := &model.Automation{
		ID:                 uuid.NewString(),
		UserID:             userID,
		CreatedAt:          now,
		UpdatedAt:          now,
		Enabled:            built.Enabled,
		Name:               built.Name,
		EnvironmentID:      built.EnvironmentID,
		T3ProjectID:        built.T3ProjectID,
		PromptTemplate:     built.PromptTemplate,
		ModelSelectionJSON: built.ModelSelectionJSON,
		RuntimeMode:        built.RuntimeMode,
		WorkspaceJSON:      built.WorkspaceJSON,
		TimeoutSeconds:     built.TimeoutSeconds,
		PostAction:         built.PostAction,
		PostOnTimeout:      built.PostOnTimeout,
	}
	rows := make([]model.AutomationTrigger, 0, len(builtTriggers))
	for i := range builtTriggers {
		rows = append(rows, newTriggerRow(a.ID, &builtTriggers[i], now, now))
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.autos.CreateTx(tx, a); err != nil {
			return err
		}
		for i := range rows {
			if err := tx.Create(&rows[i]).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		if strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
			return nil, errs.ErrT3EnvironmentNotFound
		}
		return nil, errs.ErrInternal
	}
	resp := newAutomationResponse(a, rows)
	return &resp, nil
}

// Update 全量更新语义：请求里给出完整配置；id 与 created_at 不可改。
// 触发器全量替换（保留 id 的更新、未提交的删除、未知 id 报 400）与自动化本体
// 在同一事务。更新只影响后续触发，进行中的运行按 config_snapshot 执行。
func (s *AutomationService) Update(ctx context.Context, userID, id string, req *api.UpdateAutomationRequest) (*AutomationResponse, error) {
	if _, err := s.autos.FindByIDAndUserID(ctx, id, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrAutomationNotFound
		}
		return nil, errs.ErrInternal
	}
	existing, err := s.triggers.ListByAutomationID(ctx, id)
	if err != nil {
		return nil, errs.ErrInternal
	}

	built, err := s.buildAutomationConfig(userID, req)
	if err != nil {
		return nil, err
	}
	builtTriggers, err := s.buildAutomationTriggers(userID, req.Triggers, existing)
	if err != nil {
		return nil, err
	}
	if req.Enabled != nil && *req.Enabled {
		if err := s.requireTriggersAndPublicURL(ctx, builtTriggers); err != nil {
			return nil, err
		}
		built.Enabled = true
	} else {
		built.Enabled = false
	}

	enabled := built.Enabled
	envID := built.EnvironmentID
	projectID := built.T3ProjectID
	prompt := built.PromptTemplate
	modelSel := built.ModelSelectionJSON
	runtimeMode := built.RuntimeMode
	workspace := built.WorkspaceJSON
	postAction := built.PostAction
	update := &repository.AutomationUpdate{
		Name:               &built.Name,
		Enabled:            &enabled,
		EnvironmentID:      &envID,
		T3ProjectID:        &projectID,
		PromptTemplate:     &prompt,
		ModelSelectionJSON: &modelSel,
		RuntimeMode:        &runtimeMode,
		WorkspaceJSON:      &workspace,
		TimeoutSeconds:     &built.TimeoutSeconds,
		PostAction:         &postAction,
		PostOnTimeout:      &built.PostOnTimeout,
		UpdatedAt:          time.Now().UTC(),
	}

	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.autos.UpdateTx(tx, id, userID, update); err != nil {
			return err
		}
		return s.applyTriggerDiff(tx, id, builtTriggers)
	}); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrAutomationNotFound
		}
		if errors.Is(err, errTriggerConcurrentlyModified) {
			return nil, errs.New(errs.ErrInvalidParams.Code, "触发器已被并发修改，请刷新后重试")
		}
		if strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
			return nil, errs.ErrT3EnvironmentNotFound
		}
		return nil, errs.ErrInternal
	}
	return s.Get(ctx, userID, id)
}

// Delete 删除自动化；有活动运行（未结束或后置待执行）时 40919，不排队不强杀。
func (s *AutomationService) Delete(ctx context.Context, userID, id string) error {
	if _, err := s.autos.FindByIDAndUserID(ctx, id, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrAutomationNotFound
		}
		return errs.ErrInternal
	}
	active := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		active, err = repository.HasActiveRunTx(tx, id)
		return err
	})
	if err != nil {
		return errs.ErrInternal
	}
	if active {
		return errs.ErrAutomationActive
	}
	if err := s.autos.Delete(ctx, id, userID); err != nil {
		return errs.ErrInternal
	}
	return nil
}

// requirePublicURL 启用自动化的前置条件（41202）。
func (s *AutomationService) requirePublicURL(ctx context.Context) error {
	if !s.PublicURLOK(ctx) {
		return errs.ErrPublicURLMissing
	}
	return nil
}

// builtAutomation 是一次自动化本体配置解析的产物（create/update 共用）。
// 触发器解析产物见 builtTrigger。
type builtAutomation struct {
	Name               string
	Enabled            bool
	EnvironmentID      string
	T3ProjectID        string
	PromptTemplate     string
	ModelSelectionJSON string
	RuntimeMode        string
	WorkspaceJSON      string
	TimeoutSeconds     int
	PostAction         model.AutomationPostAction
	PostOnTimeout      bool
}

// automationConfigInput 是 create/update 共用的归一化配置输入（两个请求类型
// 本体字段一致，转换见 newAutomationConfigInput）。触发器在 buildAutomationTriggers
// 单独解析。
type automationConfigInput struct {
	Name           string
	EnvironmentId  string
	T3ProjectId    string
	PromptTemplate string
	ModelSelection *api.AutomationModelSelection
	RuntimeMode    *string
	Workspace      *api.AutomationWorkspace
	TimeoutSeconds *int
	PostAction     *api.AutomationPostAction
	PostOnTimeout  *bool
}

func newAutomationConfigInput(req any) automationConfigInput {
	switch r := req.(type) {
	case *api.CreateAutomationRequest:
		return automationConfigInput{
			Name: r.Name, EnvironmentId: r.EnvironmentId, T3ProjectId: r.T3ProjectId,
			PromptTemplate: r.PromptTemplate, ModelSelection: r.ModelSelection,
			RuntimeMode: r.RuntimeMode, Workspace: r.Workspace,
			TimeoutSeconds: r.TimeoutSeconds, PostAction: r.PostAction, PostOnTimeout: r.PostOnTimeout,
		}
	case *api.UpdateAutomationRequest:
		return automationConfigInput{
			Name: r.Name, EnvironmentId: r.EnvironmentId, T3ProjectId: r.T3ProjectId,
			PromptTemplate: r.PromptTemplate, ModelSelection: r.ModelSelection,
			RuntimeMode: r.RuntimeMode, Workspace: r.Workspace,
			TimeoutSeconds: r.TimeoutSeconds, PostAction: r.PostAction, PostOnTimeout: r.PostOnTimeout,
		}
	}
	return automationConfigInput{}
}

// buildAutomationConfig 校验并归一化自动化本体配置（不含触发器）。请求字段指针
// 在入口解引用（nil → 缺省值），实现体只处理归一后的值。
func (s *AutomationService) buildAutomationConfig(
	userID string, req any,
) (*builtAutomation, error) {
	input := newAutomationConfigInput(req)
	name := input.Name
	runtimeMode := api.Deref(input.RuntimeMode)
	var timeoutSeconds int
	if input.TimeoutSeconds != nil {
		timeoutSeconds = *input.TimeoutSeconds
	}
	postAction := api.AutomationPostAction("")
	if input.PostAction != nil {
		postAction = *input.PostAction
	}
	postOnTimeout := api.Deref(input.PostOnTimeout)

	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > automationNameMaxLen {
		return nil, errs.New(errs.ErrInvalidParams.Code, "自动化名称不能为空且不超过 200 字")
	}
	if input.EnvironmentId == "" || input.T3ProjectId == "" {
		return nil, errs.New(errs.ErrInvalidParams.Code, "必须选择 T3 环境与项目")
	}

	// 环境必须存在且属于该用户（跨用户一律按不存在处理）。
	if _, err := s.envRepo.FindByIDAndUserID(context.Background(), input.EnvironmentId, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrT3EnvironmentNotFound
		}
		return nil, errs.ErrInternal
	}

	prompt := strings.TrimSpace(input.PromptTemplate)
	if prompt == "" {
		return nil, errs.New(errs.ErrInvalidParams.Code, "提示词模板不能为空")
	}

	mode := string(api.FullAccess)
	if runtimeMode != "" {
		switch api.CreateT3ThreadRequestRuntimeMode(runtimeMode) {
		case api.ApprovalRequired, api.AutoAcceptEdits, api.Auto, api.FullAccess:
			mode = runtimeMode
		default:
			return nil, errs.New(errs.ErrInvalidParams.Code, "未知的运行模式 "+runtimeMode)
		}
	}

	if timeoutSeconds == 0 {
		timeoutSeconds = automationTimeoutDefaultSeconds
	}
	if timeoutSeconds < automationTimeoutMinSeconds || timeoutSeconds > automationTimeoutMaxSeconds {
		return nil, errs.New(errs.ErrInvalidParams.Code, "超时时长须在 1 分钟到 24 小时之间")
	}

	post := model.AutomationPostNone
	if postAction != "" {
		if !model.IsValidAutomationPostAction(model.AutomationPostAction(postAction)) {
			return nil, errs.New(errs.ErrInvalidParams.Code, "未知的后置操作 "+string(postAction))
		}
		post = model.AutomationPostAction(postAction)
	}

	// 单模型缺省：cron/issue 触发落到它；PR 触发始终用触发器自己的多模型。
	modelSelJSON := ""
	if input.ModelSelection != nil {
		if input.ModelSelection.InstanceId == "" || input.ModelSelection.Model == "" {
			return nil, errs.New(errs.ErrInvalidParams.Code, "模型选择缺少 instance_id 或 model")
		}
		raw, err := json.Marshal(input.ModelSelection)
		if err != nil {
			return nil, errs.ErrInvalidParams
		}
		modelSelJSON = string(raw)
	}

	workspaceJSON := ""
	if input.Workspace != nil && string(input.Workspace.Type) != "" {
		switch api.T3ThreadWorkspaceType(input.Workspace.Type) {
		case api.Root, api.Worktree, api.ExistingWorktree:
		default:
			return nil, errs.New(errs.ErrInvalidParams.Code, "未知的工作区类型 "+string(input.Workspace.Type))
		}
		raw, err := json.Marshal(input.Workspace)
		if err != nil {
			return nil, errs.ErrInvalidParams
		}
		workspaceJSON = string(raw)
	}

	return &builtAutomation{
		Name:               name,
		EnvironmentID:      input.EnvironmentId,
		T3ProjectID:        input.T3ProjectId,
		PromptTemplate:     prompt,
		ModelSelectionJSON: modelSelJSON,
		RuntimeMode:        mode,
		WorkspaceJSON:      workspaceJSON,
		TimeoutSeconds:     timeoutSeconds,
		PostAction:         post,
		PostOnTimeout:      postOnTimeout,
	}, nil
}

// parseAutomationCron 解析五字段 cron 表达式并计算下一次触发（UTC 存储）。
// timezone 为独立字段，API 未传默认 UTC。
func parseAutomationCron(expression, timezone string, now time.Time) (*time.Location, time.Time, error) {
	// robfig 解析器会无条件剥离 TZ=/CRON_TZ= 前缀；规范要求时区走独立字段，
	// 表达式内时区前缀在这里显式拒绝。
	trimmed := strings.TrimSpace(expression)
	if strings.HasPrefix(trimmed, "TZ=") || strings.HasPrefix(trimmed, "CRON_TZ=") {
		return nil, time.Time{}, errs.New(errs.ErrInvalidParams.Code, "cron 表达式不允许内嵌时区前缀，请使用独立的时区字段")
	}
	schedule, err := automationCronParser.Parse(trimmed)
	if err != nil {
		return nil, time.Time{}, errs.New(errs.ErrInvalidParams.Code, "cron 表达式无效（须为标准五字段 分 时 日 月 周）: "+err.Error())
	}
	loc := time.UTC
	if timezone != "" {
		loaded, err := time.LoadLocation(timezone)
		if err != nil {
			return nil, time.Time{}, errs.New(errs.ErrInvalidParams.Code, "时区无效（须为 IANA 名称，如 Asia/Shanghai）")
		}
		loc = loaded
	}
	next := schedule.Next(now.In(loc)).UTC()
	return loc, next, nil
}

// eventFilterMatches 判断事件是否通过可选过滤。没有过滤=该类型全部真实事件。
func eventFilterMatches(filterJSON string, event IssueEvent) bool {
	if strings.TrimSpace(filterJSON) == "" {
		return true
	}
	var filter struct {
		Field  *string  `json:"field"`
		From   *string  `json:"from"`
		To     *string  `json:"to"`
		Labels []string `json:"labels"`
	}
	if err := json.Unmarshal([]byte(filterJSON), &filter); err != nil {
		return true // 配置损坏时不过滤（与「没有过滤」同语义），不吞事件
	}
	switch event.Type {
	case model.AutomationTriggerIssueStatusChanged:
		if filter.Field != nil && *filter.Field != "" && *filter.Field != event.StatusField {
			return false
		}
		if filter.From != nil && *filter.From != "" && *filter.From != event.From {
			return false
		}
		if filter.To != nil && *filter.To != "" && *filter.To != event.To {
			return false
		}
		return true
	case model.AutomationTriggerIssueLabelAdded:
		if len(filter.Labels) == 0 {
			return true
		}
		for _, wanted := range filter.Labels {
			for _, added := range event.LabelsAdded {
				if wanted == added {
					return true
				}
			}
		}
		return false
	}
	return true
}

// ---- API 响应构造 ----

func newAutomationResponse(a *model.Automation, triggers []model.AutomationTrigger) AutomationResponse {
	resp := AutomationResponse{
		Id:             a.ID,
		Name:           a.Name,
		Enabled:        a.Enabled,
		EnvironmentId:  a.EnvironmentID,
		T3ProjectId:    a.T3ProjectID,
		TimeoutSeconds: a.TimeoutSeconds,
		PostAction:     api.AutomationPostAction(a.PostAction),
		PostOnTimeout:  a.PostOnTimeout,
		RuntimeMode:    a.RuntimeMode,
		PromptTemplate: a.PromptTemplate,
		Triggers:       make([]api.AutomationTrigger, 0, len(triggers)),
		CreatedAt:      a.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:      a.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	for i := range triggers {
		resp.Triggers = append(resp.Triggers, newAutomationTriggerResponse(&triggers[i]))
	}
	if a.ModelSelectionJSON != "" {
		var sel AutomationModelSelection
		if json.Unmarshal([]byte(a.ModelSelectionJSON), &sel) == nil {
			resp.ModelSelection = &sel
		}
	}
	if a.WorkspaceJSON != "" {
		var ws AutomationWorkspace
		if json.Unmarshal([]byte(a.WorkspaceJSON), &ws) == nil {
			resp.Workspace = &ws
		}
	}
	return resp
}

func newAutomationRunResponse(run *model.AutomationRun) AutomationRunResponse {
	status := api.AutomationRunStatus(run.Status)
	resp := AutomationRunResponse{
		Id:            run.ID,
		AutomationId:  run.AutomationID,
		TriggerId:     run.TriggerID,
		TriggerType:   api.AutomationTriggerType(run.TriggerType),
		TriggerKey:    run.TriggerKey,
		TriggeredAt:   run.TriggeredAt.UTC().Format(time.RFC3339Nano),
		EnvironmentId: run.EnvironmentID,
		T3ProjectId:   run.T3ProjectID,
		ThreadId:      run.ThreadID,
		Status:        &status,
		DeadlineAt:    api.Ptr(run.DeadlineAt.UTC().Format(time.RFC3339Nano)),
		PostState:     api.AutomationPostState(run.PostState),
		PostAttempt:   run.PostAttempt,
	}
	if run.TriggerContextJSON != "" {
		var ctx AutomationTriggerContext
		if json.Unmarshal([]byte(run.TriggerContextJSON), &ctx) == nil {
			resp.TriggerContext = &ctx
		}
	}
	if run.DispatchStartedAt != nil {
		resp.DispatchStartedAt = api.Ptr(run.DispatchStartedAt.UTC().Format(time.RFC3339Nano))
	}
	if run.DispatchedAt != nil {
		resp.DispatchedAt = api.Ptr(run.DispatchedAt.UTC().Format(time.RFC3339Nano))
	}
	if run.StartedAt != nil {
		resp.StartedAt = api.Ptr(run.StartedAt.UTC().Format(time.RFC3339Nano))
	}
	if run.FinishedAt != nil {
		resp.FinishedAt = api.Ptr(run.FinishedAt.UTC().Format(time.RFC3339Nano))
	}
	resp.ErrorCode = api.NonEmptyPtr(&run.ErrorCode)
	resp.ErrorMessage = api.NonEmptyPtr(&run.ErrorMessage)
	resp.Summary = api.NonEmptyPtr(&run.Summary)
	if run.PostNextAttemptAt != nil {
		resp.PostNextAttemptAt = api.Ptr(run.PostNextAttemptAt.UTC().Format(time.RFC3339Nano))
	}
	if run.PostDeadlineAt != nil {
		resp.PostDeadlineAt = api.Ptr(run.PostDeadlineAt.UTC().Format(time.RFC3339Nano))
	}
	resp.PostError = api.NonEmptyPtr(&run.PostError)
	if run.RepoFullName != "" {
		resp.RepoFullName = api.Ptr(run.RepoFullName)
		resp.PrNumber = api.Ptr(run.PRNumber)
		resp.RoundNumber = api.Ptr(run.RoundNumber)
	}
	if run.HeadSHA != "" {
		resp.HeadSha = api.Ptr(run.HeadSHA)
	}
	if run.ModelInstanceID != "" {
		resp.ModelInstanceId = api.Ptr(run.ModelInstanceID)
	}
	if run.ModelName != "" {
		resp.ModelName = api.Ptr(run.ModelName)
	}
	return resp
}

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// renderAutomationPrompt 渲染用户模板占位符并追加服务端回报说明。
// 渲染结果不落库（config_snapshot 只存模板原样）。prInstructions 非空时是
// GitHub PR 评审运行的代发端点说明（INT-93）。
func renderAutomationPrompt(template, publicURL, token, runID string, ctxData map[string]string, prInstructions string) string {
	body := template
	for key, value := range ctxData {
		body = strings.ReplaceAll(body, key, value)
	}
	for _, key := range automationPromptPlaceholders {
		body = strings.ReplaceAll(body, key, "")
	}
	for _, key := range automationPRPromptPlaceholders {
		body = strings.ReplaceAll(body, key, "")
	}

	instructions := fmt.Sprintf(`
## 自动化回报说明（系统附加）

你是 Fast Ship 自动化任务启动的线程。本次运行结束后必须回报结果：

- 回报端点：POST %s/api/automation-agent/report
- 认证头：Authorization: Bearer %s
- 开始工作时先回报：{"phase":"started"}
- 结束时回报：{"phase":"finished","summary":"一句话结果摘要","error_code":"失败时的错误码，可省略","error_message":"失败详情，可省略"}

运行期间你还可以（令牌在本运行结束前有效）：
- 读取用户线程：GET %s/api/automation-agent/threads
- 读取单个线程详情：GET %s/api/automation-agent/environments/{environmentId}/threads/{threadId}?limit=50
- 读取条目正文：GET %s/api/automation-agent/environments/{environmentId}/threads/{threadId}/items/{itemId}
- 发布站内通知：POST %s/api/automation-agent/notifications（{"title":"...","body":"..."}）

注意：结束回报（finished）被受理后令牌立即失效，之后不能再读取线程或发通知。
`, publicURL, token, publicURL, publicURL, publicURL, publicURL)

	if prInstructions != "" {
		instructions += "\n" + prInstructions
	}

	return strings.TrimSpace(body) + "\n" + instructions
}
