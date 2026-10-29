package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// T3 客户端写操作（INT-85）：REST 表达用户操作，适配器转成 T3 一次性
// WS RPC。线程/队列/请求状态由 T3 持有——Fast Ship 不建队列库、不建任务
// 调度器、不做第二套线程状态机。每个请求一条 WS 会话（探测 → 拨号 → 一次
// 或数次 RPC → 断开），整段受 ctx 与独立预算约束。
//
// 协议事实（2026-10 核对自 T3 源码）：
//   - orchestration.dispatchCommand 的 payload 就是 OrchestrationV2Command
//     对象（无额外 command 包装），写方法全部要求 orchestration:operate
//     （apps/server/src/auth/RpcAuthorization.ts）
//   - message.dispatch 的 dispatchMode 联合里 queue_after_active 即
//     「活动运行时排队」；deliveryIntent=auto 是服务端代投递语义，
//     本集成禁止使用
//   - Stop 口径（INT-173）：capabilities.threadStop 声明支持时派发公开命令
//     thread.stop——服务端选目标、挂队列、停委派任务，客户端不自选 run；
//     能力缺失（旧上游）才回退 run.interrupt holdQueue=true 的自选 run 路径
//   - launchThread 输入见 contracts orchestrationV2.ts
//     OrchestrationV2ThreadLaunchInput；threadId 可由客户端指定（稳定重试）

// t3WriteBudget 是写操作在 t3RemoteBudgets 之外的独立预算位。
func (s *T3ThreadsService) t3WriteBudget(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, s.budgets.ThreadDetail)
}

// RequireOperate 给 handler 在解码 body 之前调用：只读 40300，环境不存在 40417。
func (s *T3ThreadsService) RequireOperate(ctx context.Context, userID, envID string) error {
	_, err := s.loadEnvForWrite(ctx, userID, envID)
	return err
}

// loadEnvForWrite 解析环境并施加 operate 守卫（40300），凭证不可用时报
// 「请重新配对」。
func (s *T3ThreadsService) loadEnvForWrite(ctx context.Context, userID, envID string) (*model.T3Environment, error) {
	env, err := s.envRepo.FindByIDAndUserID(ctx, envID, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrT3EnvironmentNotFound
		}
		return nil, errs.ErrInternal
	}
	if err := requireT3Operate(env); err != nil {
		return nil, err
	}
	return env, nil
}

// dialAfterProbe 与读路径同一探测顺序：先 HTTP 会话分类凭证状态，探通后
// 再开 WS 会话（握手失败不带状态码，凭证问题不能落成笼统 50202）。
func (s *T3ThreadsService) dialAfterProbe(ctx context.Context, env *model.T3Environment) (*t3RPCSession, error) {
	token, err := s.bearerToken(env)
	if err != nil {
		return nil, errs.New(errs.ErrT3Upstream.Code, "环境凭证不可用，请重新配对")
	}
	if status, probeErr := s.http.probeSession(ctx, env.Address, token); status != T3EnvironmentStatusOK {
		return nil, errs.New(errs.ErrT3Upstream.Code, probeErr)
	}
	session, err := dialT3RPC(ctx, wsBaseOf(env.Address), token)
	if err != nil {
		return nil, s.upstreamError(err)
	}
	return session, nil
}

// newT3CommandId 生成 T3 CommandId（非空 trimmed 字符串即可，用 UUID）。
func newT3CommandId() string { return uuid.NewString() }

func firstNonEmptyID(values ...*string) string {
	for _, value := range values {
		if value != nil && strings.TrimSpace(*value) != "" {
			return strings.TrimSpace(*value)
		}
	}
	return uuid.NewString()
}

func t3MessageHasContent(text string, attachments []T3ChatAttachment) bool {
	return strings.TrimSpace(text) != "" || len(attachments) > 0
}

// normalizeT3AttachmentInput 把请求里的附件元数据转成上游 camelCase 形状。
// 返回供 dispatchCommand/launchThread 直接使用的数组（调用方保证缺省发 []）。
func normalizeT3AttachmentInput(attachments []T3ChatAttachment) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(attachments))
	for _, attachment := range attachments {
		if attachment.Id == "" || attachment.Name == "" || attachment.Type != "image" {
			return nil, errs.ErrInvalidParams
		}
		if !t3SupportedImageMime(attachment.MimeType) {
			return nil, errs.New(errs.ErrInvalidParams.Code, "附件 "+attachment.Id+" 的类型不受支持（仅 PNG/JPEG/GIF/WebP）")
		}
		if attachment.SizeBytes <= 0 || attachment.SizeBytes > t3MaxImageBytes {
			return nil, errs.New(errs.ErrInvalidParams.Code, "附件 "+attachment.Id+" 的大小超出限制（单图 10MiB）")
		}
		out = append(out, map[string]any{
			"type":      "image",
			"id":        attachment.Id,
			"name":      attachment.Name,
			"mimeType":  attachment.MimeType,
			"sizeBytes": attachment.SizeBytes,
		})
	}
	return out, nil
}

// fetchProjectionForWrite 在既有 WS 会话上取线程完整投影（HTTP 面与 WS
// 快照共用凭证；这里走 HTTP 以获得完整历史窗口）。
func (s *T3ThreadsService) fetchProjectionForWrite(ctx context.Context, env *model.T3Environment, token, threadID string) (*t3ThreadProjection, error) {
	raw, err := s.http.fetchThreadDetail(ctx, env.Address, token, threadID)
	if err != nil {
		return nil, err
	}
	parsed, err := decodeT3Projection(raw)
	if err != nil {
		return nil, fmt.Errorf("线程投影解析失败: %w", err)
	}
	return parsed, nil
}

// ------ 启动器与工作区选项 ------

// ComposerOptions 聚合线程启动器选项：项目表 + server.getConfig（config 与
// 内嵌其中的 settings 原样透传，camelCase 留在适配器内）。上游 #26238 起删除
// 了 server.getSettings，设置改由 getConfig.settings 携带；旧环境 getConfig
// 无 settings 字段（字段缺失与「settings 存在但为 null/空」必须区分——前者
// 才回退 server.getSettings，后者是新版真实的空设置，多调一次旧 RPC 只会
// 得到 method-not-found）。项目表走 HTTP `GET /api/projects`（INT-116）：不再为拿项目配置拉全量 shell
// 快照、不建 shell 订阅；fetchProjectSnapshot 已把 401/403 分类成凭证被拒，
// 不再需要前置会话探测。
func (s *T3ThreadsService) ComposerOptions(ctx context.Context, userID, envID string) (*T3ComposerOptions, error) {
	env, err := s.envRepo.FindByIDAndUserID(ctx, envID, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrT3EnvironmentNotFound
		}
		return nil, errs.ErrInternal
	}

	ctx, cancel := s.t3WriteBudget(ctx)
	defer cancel()

	token, err := s.bearerToken(env)
	if err != nil {
		return nil, errs.New(errs.ErrT3Upstream.Code, "环境凭证不可用，请重新配对")
	}

	projectShells, err := s.http.fetchProjectSnapshot(ctx, env.Address, token)
	if err != nil {
		return nil, s.upstreamError(err)
	}

	session, err := dialT3RPC(ctx, wsBaseOf(env.Address), token)
	if err != nil {
		return nil, s.upstreamError(err)
	}
	defer session.Close()

	configValue, err := session.call(ctx, "server.getConfig", map[string]any{})
	if err != nil {
		return nil, s.upstreamError(err)
	}

	projects := make([]api.T3ComposerOptions_Projects, 0, len(projectShells))
	for _, project := range projectShells {
		item := api.T3ComposerOptions_Projects{
			Id:    project.ID,
			Title: project.Title,
		}
		if project.WorkspaceRoot != nil && *project.WorkspaceRoot != "" {
			item.WorkspaceRoot = api.Ptr(*project.WorkspaceRoot)
		}
		if project.DefaultModelSelection != nil {
			item.DefaultModelSelection = newT3ModelSelectionAPI(project.DefaultModelSelection)
		}
		projects = append(projects, item)
	}
	config, settings := map[string]any{}, map[string]any{}
	_ = json.Unmarshal(configValue, &config)
	// settings 内嵌在 getConfig 响应里。用 RawMessage 区分「字段缺失」与
	// 「字段为 null/空」：只有缺失才回退旧上游独立的 server.getSettings
	// （新版 settings 为 null/空对象都是真实空设置，不得去调已删除的
	// 旧 RPC）；字段存在时按其值原样解。
	var withSettings struct {
		Settings json.RawMessage `json:"settings"`
	}
	_ = json.Unmarshal(configValue, &withSettings)
	switch {
	case len(withSettings.Settings) == 0:
		// 字段缺失 = 旧上游（#26238 之前）无内嵌 settings：回退一次独立的
		// server.getSettings 取真实设置。getConfig 本身失败仍按上游错误
		// 处理（不会走到这里）；getSettings 失败宽容为空设置——它与
		// ComposerOptions 其余字段独立，不牵连整个应答。
		if settingsValue, err := session.call(ctx, "server.getSettings", map[string]any{}); err == nil {
			var legacy map[string]any
			if json.Unmarshal(settingsValue, &legacy) == nil && legacy != nil {
				settings = legacy
			}
		}
	case string(withSettings.Settings) == "null":
		// 新版 settings 为 null：空设置。
	default:
		_ = json.Unmarshal(withSettings.Settings, &settings)
	}
	return &T3ComposerOptions{
		Projects: projects,
		Config:   config,
		Settings: settings,
	}, nil
}

// newT3ModelSelectionAPI 归一化模型选择（options 原样透传）。
func newT3ModelSelectionAPI(m *t3ModelSelection) *api.T3ModelSelection {
	if m == nil {
		return nil
	}
	out := &api.T3ModelSelection{InstanceId: m.InstanceID, Model: m.Model}
	if len(m.Options) > 0 {
		out.Options = make([]api.T3ModelOption, 0, len(m.Options))
		for _, option := range m.Options {
			out.Options = append(out.Options, api.T3ModelOption{Id: option.ID, Value: option.Value})
		}
	}
	return out
}

// t3ModelSelectionWire 把 API 模型选择转成上游 wire 形状（options 为 [{id,value}]）。
func t3ModelSelectionWire(m *api.T3ModelSelection) map[string]any {
	if m == nil {
		return nil
	}
	wire := map[string]any{"instanceId": m.InstanceId, "model": m.Model}
	if len(m.Options) > 0 {
		options := make([]map[string]any, 0, len(m.Options))
		for _, option := range m.Options {
			options = append(options, map[string]any{"id": option.Id, "value": option.Value})
		}
		wire["options"] = options
	}
	return wire
}

// WorkspaceOptions 解析项目工作区：cwd 来自上游项目 workspaceRoot（不接受
// 前端任意路径；项目表走 HTTP `/api/projects`，零 shell 订阅），再在 T3 上取
// 当前状态与引用列表。
func (s *T3ThreadsService) WorkspaceOptions(ctx context.Context, userID, envID, projectID, query string, cursor *int) (*T3WorkspaceOptions, error) {
	if projectID == "" {
		return nil, errs.ErrInvalidParams
	}
	env, err := s.envRepo.FindByIDAndUserID(ctx, envID, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrT3EnvironmentNotFound
		}
		return nil, errs.ErrInternal
	}

	ctx, cancel := s.t3WriteBudget(ctx)
	defer cancel()

	token, err := s.bearerToken(env)
	if err != nil {
		return nil, errs.New(errs.ErrT3Upstream.Code, "环境凭证不可用，请重新配对")
	}

	projectShells, err := s.http.fetchProjectSnapshot(ctx, env.Address, token)
	if err != nil {
		return nil, s.upstreamError(err)
	}
	var workspaceRoot string
	for _, project := range projectShells {
		if project.ID == projectID && project.WorkspaceRoot != nil {
			workspaceRoot = *project.WorkspaceRoot
		}
	}
	if workspaceRoot == "" {
		return nil, errs.New(40401, "T3 项目不在该环境（或项目缺少 workspaceRoot）")
	}

	session, err := dialT3RPC(ctx, wsBaseOf(env.Address), token)
	if err != nil {
		return nil, s.upstreamError(err)
	}
	defer session.Close()

	statusValue, err := session.call(ctx, "vcs.refreshStatus", map[string]any{"cwd": workspaceRoot})
	if err != nil {
		return nil, s.upstreamError(err)
	}
	var status struct {
		IsRepo                *bool   `json:"isRepo"`
		RefName               *string `json:"refName"`
		HasWorkingTreeChanges *bool   `json:"hasWorkingTreeChanges"`
		HasPrimaryRemote      *bool   `json:"hasPrimaryRemote"`
	}
	if err := json.Unmarshal(statusValue, &status); err != nil {
		return nil, errs.New(errs.ErrT3Upstream.Code, "工作区状态解析失败: "+shortenT3Error(err))
	}

	listInput := map[string]any{"cwd": workspaceRoot, "refKind": "all"}
	if query != "" {
		listInput["query"] = query
	}
	if cursor != nil {
		listInput["cursor"] = *cursor
	}

	type listRefsPage struct {
		Refs []struct {
			Name         string  `json:"name"`
			IsRemote     *bool   `json:"isRemote"`
			IsDefault    *bool   `json:"isDefault"`
			WorktreePath *string `json:"worktreePath"`
		} `json:"refs"`
		IsRepo           *bool `json:"isRepo"`
		HasPrimaryRemote *bool `json:"hasPrimaryRemote"`
		NextCursor       *int  `json:"nextCursor"`
		TotalCount       int   `json:"totalCount"`
	}
	collectPage := func(page listRefsPage) []api.T3WorkspaceRef {
		refs := make([]api.T3WorkspaceRef, 0, len(page.Refs))
		for _, ref := range page.Refs {
			kind := "local"
			if api.Deref(ref.IsRemote) {
				kind = "remote"
			}
			item := api.T3WorkspaceRef{Name: ref.Name, Kind: kind}
			if ref.IsDefault != nil {
				item.IsDefault = api.Ptr(*ref.IsDefault)
			}
			if ref.WorktreePath != nil && *ref.WorktreePath != "" {
				item.WorktreePath = api.Ptr(*ref.WorktreePath)
			}
			refs = append(refs, item)
		}
		return refs
	}

	refsValue, err := session.call(ctx, "vcs.listRefs", listInput)
	if err != nil {
		return nil, s.upstreamError(err)
	}
	var refsResult listRefsPage
	if err := json.Unmarshal(refsValue, &refsResult); err != nil {
		return nil, errs.New(errs.ErrT3Upstream.Code, "引用列表解析失败: "+shortenT3Error(err))
	}
	refs := collectPage(refsResult)
	nextCursor := refsResult.NextCursor
	totalCount := refsResult.TotalCount

	// 未指定 cursor 时自动跟页，避免新建线程下拉只看到第一页分支。
	// 显式 cursor 仍只取该页（给还想自己分页的调用方）。
	const maxRefPages = 8
	for page := 1; cursor == nil && nextCursor != nil && page < maxRefPages; page++ {
		listInput["cursor"] = *nextCursor
		refsValue, err = session.call(ctx, "vcs.listRefs", listInput)
		if err != nil {
			return nil, s.upstreamError(err)
		}
		var more listRefsPage
		if err := json.Unmarshal(refsValue, &more); err != nil {
			return nil, errs.New(errs.ErrT3Upstream.Code, "引用列表解析失败: "+shortenT3Error(err))
		}
		refs = append(refs, collectPage(more)...)
		nextCursor = more.NextCursor
		if more.TotalCount > totalCount {
			totalCount = more.TotalCount
		}
	}
	return &T3WorkspaceOptions{
		ProjectId:             projectID,
		Cwd:                   workspaceRoot,
		IsRepo:                api.Ptr(api.Deref(status.IsRepo) || api.Deref(refsResult.IsRepo)),
		RefName:               api.NonEmptyPtr(status.RefName),
		HasWorkingTreeChanges: api.Ptr(api.Deref(status.HasWorkingTreeChanges)),
		HasPrimaryRemote:      api.Ptr(api.Deref(status.HasPrimaryRemote) || api.Deref(refsResult.HasPrimaryRemote)),
		Refs:                  refs,
		NextCursor:            nextCursor,
		TotalCount:            api.Ptr(totalCount),
	}, nil
}

// ------ 线程启动与消息 ------

// t3ThreadWorkspaceWire 把请求工作区解析成 launchThread 的 workspaceStrategy。
// worktree 的 base_ref 缺省为主检出当前分支（经 vcs.refreshStatus 解析），
// start_from_origin 缺省取 T3 设置 newWorktreesStartFromOrigin。
// root/worktree 的 cwd 由调用方从上游项目解析；existing_worktree 的路径原样转给 T3。
func (s *T3ThreadsService) t3ThreadWorkspaceWire(ctx context.Context, session *t3RPCSession, workspaceRoot string, workspace *api.T3ThreadWorkspace) (map[string]any, error) {
	if workspace == nil || workspace.Type == "" {
		return map[string]any{"type": "root"}, nil
	}
	switch workspace.Type {
	case "root":
		// Current checkout 不切换分支（INT-85 共识）。即使请求带了 branch 也丢弃。
		return map[string]any{"type": "root"}, nil
	case "existing_worktree":
		if workspace.WorktreePath == nil || *workspace.WorktreePath == "" {
			return nil, errs.New(errs.ErrInvalidParams.Code, "existing_worktree 需要提供 worktree_path")
		}
		wire := map[string]any{"type": "existing_worktree", "worktreePath": *workspace.WorktreePath}
		if workspace.Branch != nil && *workspace.Branch != "" {
			wire["branch"] = *workspace.Branch
		}
		return wire, nil
	case "worktree":
		wire := map[string]any{"type": "worktree"}
		if workspace.BaseRef != nil && *workspace.BaseRef != "" {
			wire["baseRef"] = *workspace.BaseRef
		} else {
			// 共识：新 worktree 的 base_ref 默认主检出当前分支。
			statusValue, err := session.call(ctx, "vcs.refreshStatus", map[string]any{"cwd": workspaceRoot})
			if err != nil {
				return nil, s.upstreamError(err)
			}
			var status struct {
				RefName *string `json:"refName"`
			}
			if err := json.Unmarshal(statusValue, &status); err != nil {
				return nil, errs.New(errs.ErrT3Upstream.Code, "工作区状态解析失败: "+shortenT3Error(err))
			}
			if api.Deref(status.RefName) == "" {
				return nil, errs.New(errs.ErrInvalidParams.Code, "无法确定主检出当前分支，请显式提供 base_ref")
			}
			wire["baseRef"] = *status.RefName
		}
		if workspace.Branch != nil && *workspace.Branch != "" {
			wire["branch"] = *workspace.Branch
		}
		if workspace.StartFromOrigin != nil {
			wire["startFromOrigin"] = *workspace.StartFromOrigin
		} else {
			// startFromOrigin 从 T3 设置解析（newWorktreesStartFromOrigin，缺省
			// false）。设置内嵌在 getConfig 响应里（上游 #26238 已删 getSettings）；
			// 读取失败或 settings 缺省时不带该字段，交回上游自身缺省。
			if configValue, err := session.call(ctx, "server.getConfig", map[string]any{}); err == nil {
				var settings struct {
					Settings struct {
						NewWorktreesStartFromOrigin *bool `json:"newWorktreesStartFromOrigin"`
					} `json:"settings"`
				}
				if json.Unmarshal(configValue, &settings) == nil && settings.Settings.NewWorktreesStartFromOrigin != nil {
					wire["startFromOrigin"] = *settings.Settings.NewWorktreesStartFromOrigin
				}
			}
		}
		return wire, nil
	default:
		return nil, errs.New(errs.ErrInvalidParams.Code, "未知的工作区类型 "+string(workspace.Type))
	}
}

// t3TitleSeed 从首条消息生成种子标题（对齐 T3 deriveThreadTitleSeed 的主干：
// 正文压缩空白；空正文用首个附件名；兜底 "New thread"）。
func t3TitleSeed(text string, attachments []T3ChatAttachment) string {
	normalized := strings.Join(strings.Fields(text), " ")
	if normalized != "" {
		return truncateT3Title(normalized)
	}
	if len(attachments) > 0 {
		name := strings.Join(strings.Fields(attachments[0].Name), " ")
		if name != "" {
			return truncateT3Title("Image: " + name)
		}
	}
	return "New thread"
}

func truncateT3Title(text string) string {
	runes := []rune(text)
	if len(runes) > 80 {
		return string(runes[:80])
	}
	return text
}

// CreateThread 经 orchestration.launchThread 启动线程。threadId 由 Fast Ship
// 生成（稳定，重试幂等）；title 缺省从首条消息生成种子并让 T3 生成正式标题。
// 项目与 cwd 从上游 shell 快照解析：模型缺省回落项目默认，worktree 的
// base_ref 缺省取主检出当前分支。
//
// 实现在 createThread（t3_thread_launch.go）：agent 派发复用同一实现并额外读取
// 「launch RPC 是否已发出」；本入口丢弃该信息，REST 语义不变（INT-85）。
func (s *T3ThreadsService) CreateThread(ctx context.Context, userID, envID string, req *T3CreateThreadRequest) (*T3ThreadLaunchResult, error) {
	result, _, err := s.createThread(ctx, userID, envID, req)
	return result, err
}

// SendMessage 经 dispatchCommand 下发 message.dispatch（queue_after_active）。
// attachments 必须发数组（无附件发 []）；禁止 deliveryIntent=auto。
func (s *T3ThreadsService) SendMessage(ctx context.Context, userID, envID, threadID string, req *T3SendMessageRequest) (*T3DispatchResult, error) {
	if req == nil || threadID == "" {
		return nil, errs.ErrInvalidParams
	}
	if !t3MessageHasContent(req.Text, req.Attachments) {
		return nil, errs.New(errs.ErrInvalidParams.Code, "消息正文与附件不能同时为空")
	}
	attachments, err := normalizeT3AttachmentInput(req.Attachments)
	if err != nil {
		return nil, err
	}

	env, err := s.loadEnvForWrite(ctx, userID, envID)
	if err != nil {
		return nil, err
	}

	ctx, cancel := s.t3WriteBudget(ctx)
	defer cancel()

	token, err := s.bearerToken(env)
	if err != nil {
		return nil, errs.New(errs.ErrT3Upstream.Code, "环境凭证不可用，请重新配对")
	}
	if status, probeErr := s.http.probeSession(ctx, env.Address, token); status != T3EnvironmentStatusOK {
		return nil, errs.New(errs.ErrT3Upstream.Code, probeErr)
	}

	session, err := dialT3RPC(ctx, wsBaseOf(env.Address), token)
	if err != nil {
		return nil, s.upstreamError(err)
	}
	defer session.Close()

	sequence, err := s.dispatchT3Command(ctx, session, threadID, map[string]any{
		"type":           "message.dispatch",
		"createdBy":      "user",
		"creationSource": "web",
		"commandId":      firstNonEmptyID(req.CommandId),
		"threadId":       threadID,
		"messageId":      firstNonEmptyID(req.MessageId),
		"text":           req.Text,
		"attachments":    attachments,
		"dispatchMode":   map[string]any{"type": "queue_after_active"},
	})
	if err != nil {
		return nil, err
	}
	return &T3DispatchResult{Sequence: sequence}, nil
}

// dispatchT3Command 发送 orchestration.dispatchCommand 并解析 {sequence}。
// provider 不支持排队/转向时上游拒绝 → 明确报告（不自动改 steer）。
func (s *T3ThreadsService) dispatchT3Command(ctx context.Context, session *t3RPCSession, threadID string, command map[string]any) (int, error) {
	value, err := session.call(ctx, "orchestration.dispatchCommand", command)
	if err != nil {
		if mapped := t3CommandConflictError(err); mapped != nil {
			return 0, mapped
		}
		return 0, s.upstreamError(fmt.Errorf("dispatchCommand(%s) 失败: %w", command["type"], err))
	}
	var result struct {
		Sequence *int `json:"sequence"`
	}
	if err := json.Unmarshal(value, &result); err != nil || result.Sequence == nil {
		return 0, errs.New(errs.ErrT3Upstream.Code, "命令受理结果解析失败")
	}
	return *result.Sequence, nil
}

// t3CommandConflictError 把上游可明确识别的状态冲突映射成 40916/40419。
// 识别依据是 Fail 错误的 _tag 或固定 cause 文案（Orchestrator 的判定分支
// 输出这些稳定字符串；除此之外一律按上游错误处理，不猜测）。
func t3CommandConflictError(err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "OrchestratorCommandIdConflictError"):
		return errs.New(errs.ErrT3StateConflict.Code, "命令已被受理过（commandId 冲突），请刷新后重试")
	case strings.Contains(msg, "is not queued"):
		return errs.New(errs.ErrT3StateConflict.Code, "该排队消息已不在队列（可能已开始或被取消），请刷新后重试")
	case strings.Contains(msg, "Runtime request") && strings.Contains(msg, "was not found"):
		return errs.ErrT3ThreadItemNotFound
	}
	return nil
}

// t3RequestConflictError 运行时请求已决（pending 之外）的固定 cause 形态：
// `Runtime request <id> is <status>.`。
func t3RequestConflictError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "Runtime request") && strings.Contains(msg, " is ")
}

// ------ Stop 与队列 ------

// t3ConfigSupportsThreadStop 判定 server.getConfig 响应是否声明
// environment.capabilities.threadStop（INT-173：thread.stop 已是公开命令，
// 旧版服务端既不声明也不认识它）。与结算能力「必须支持否则报错」语义不同，
// 这里是能力选择：支持走 thread.stop 新路，缺失走 run.interrupt 回退——
// 回退路径在新旧环境都成立，所以响应解析失败同样按缺失处理。
func t3ConfigSupportsThreadStop(configValue json.RawMessage) bool {
	var config struct {
		Environment struct {
			Capabilities struct {
				ThreadStop *bool `json:"threadStop"`
			} `json:"capabilities"`
		} `json:"environment"`
	}
	if err := json.Unmarshal(configValue, &config); err != nil {
		return false
	}
	return config.Environment.Capabilities.ThreadStop != nil &&
		*config.Environment.Capabilities.ThreadStop
}

// StopThread 停止线程（INT-173，能力驱动的两条路径）：
//   - 环境在 server.getConfig 声明 capabilities.threadStop（新上游）：派发
//     公开命令 thread.stop，对齐官方客户端「未显式指定 run 时优先
//     thread.stop」——由服务端选择目标、挂起队列并停止委派任务，本地不再
//     自选 run。回执只表示受理：受理即 Stopped=true；run_id 在此路径没有
//     自选 run 可填，留缺省。
//   - 能力缺失（旧上游）：保留既有回退——可信 shell 本地选 run 后发
//     run.interrupt(holdQueue=true)。
//
// 两条路径共同的前置：先读 HTTP 投影——线程真的不存在必须回到 40418
// （投影 404 → errT3ThreadNotFound，基线语义；凭证被拒/不可达也在此按既有
// 分类）。「无目标 → Stopped=false」的判定只对回退路径有意义：thread.stop
// 路径由服务端判定有无活可停，本地不做前置结论。
func (s *T3ThreadsService) StopThread(ctx context.Context, userID, envID, threadID string) (*T3ThreadStopResult, error) {
	env, err := s.loadEnvForWrite(ctx, userID, envID)
	if err != nil {
		return nil, err
	}

	ctx, cancel := s.t3WriteBudget(ctx)
	defer cancel()

	token, err := s.bearerToken(env)
	if err != nil {
		return nil, errs.New(errs.ErrT3Upstream.Code, "环境凭证不可用，请重新配对")
	}

	// 投影在先（两条路径共用）：404 → 40418，不被 shell 守卫拦成 50202。
	// 这里只做存在性/分类；回退路径的 run 选择基于等待之后的新鲜信息（见下）。
	if _, err := s.fetchProjectionForWrite(ctx, env, token, threadID); err != nil {
		return nil, s.upstreamError(err)
	}

	session, err := dialT3RPC(ctx, wsBaseOf(env.Address), token)
	if err != nil {
		return nil, s.upstreamError(err)
	}
	defer session.Close()

	// 能力分支依据：getConfig 调用失败按上游错误处理（旧上游也有 getConfig，
	// 调用失败不是「旧环境」的证据）；响应里没有 threadStop 声明才算缺失。
	configValue, err := session.call(ctx, t3RPCServerGetConfig, map[string]any{})
	if err != nil {
		return nil, s.upstreamError(err)
	}
	if t3ConfigSupportsThreadStop(configValue) {
		// thread.stop 受理即成功：服务端负责选目标、挂队列、停委派任务
		// （无可停目标时也正常回执，不本地前置判「无事可做」）。不必先
		// 解析出 run 才发，带 commandId/threadId 即可。
		if _, err := s.dispatchT3Command(ctx, session, threadID, map[string]any{
			"type":      "thread.stop",
			"commandId": newT3CommandId(),
			"threadId":  threadID,
		}); err != nil {
			return nil, err
		}
		return &T3ThreadStopResult{Stopped: true}, nil
	}

	// 回退路径（旧上游无 threadStop 能力）：保留本地自选 run 的既有口径。
	shell, _, err := s.trustedShell(ctx, env, token, threadID)
	if err != nil {
		return nil, err
	}

	// shell.activeRunId 是新鲜的可中断活动 run（preparing/starting/running）；
	// 为空时典型是 waiting——它只有投影里看得到，所以回读一次投影再决策。
	// 投影在这里之后读，与运行态同一时间基准。
	var fresh *t3ThreadProjection
	if shell.ActiveRunID == nil || *shell.ActiveRunID == "" {
		fetched, freshErr := s.fetchProjectionForWrite(ctx, env, token, threadID)
		if freshErr != nil {
			return nil, s.upstreamError(freshErr)
		}
		fresh = fetched
	}
	runID := selectStopRunID(shell, fresh)
	if runID == "" {
		return &T3ThreadStopResult{Stopped: false}, nil
	}

	if _, err := s.dispatchT3Command(ctx, session, threadID, map[string]any{
		"type":      "run.interrupt",
		"commandId": newT3CommandId(),
		"threadId":  threadID,
		"runId":     runID,
		"holdQueue": true,
	}); err != nil {
		return nil, err
	}
	return &T3ThreadStopResult{Stopped: true, RunId: api.Ptr(runID)}, nil
}

// selectStopRunID 是 Stop 回退路径（旧上游无 threadStop 能力）的本地 run
// 选择，对齐官方客户端显式指定 run 时的口径：
//   - shell.activeRunId 优先：它是可信活跃面按 isInterruptibleRunForShell
//     （preparing/starting/running）派生的，比任何投影都新鲜；
//   - 其次看投影里的活动 run（含 waiting——上游 shell 的可中断集合不含
//     waiting，只有投影里能看到）；
//   - 都没有时看 shell 的可中断后台工作（t3HasInterruptibleBackgroundWork，
//     与「占完成位」是两个口径）：根 run 已结束但还有可停止的后台工作时，
//     T3 的 Stop 接受对最近 run 的中断。PR watch 不算可中断后台（与
//     interruptThreadTurn 不含 pullRequests 一致）。
//
// projection 只在 shell 没有 activeRunId 时传入（调用方按需回读）。
func selectStopRunID(shell *t3ThreadShell, projection *t3ThreadProjection) string {
	if shell.ActiveRunID != nil && *shell.ActiveRunID != "" {
		return *shell.ActiveRunID
	}
	if projection != nil {
		if active := resolveT3ActiveRun(projection.Runs); active != nil {
			return active.ID
		}
	}
	if shell.LatestRunID != nil && t3HasInterruptibleBackgroundWork(shell.PendingBackgroundTasks) {
		return *shell.LatestRunID
	}
	return ""
}

// ResumeQueue 下发 queue.resume。
func (s *T3ThreadsService) ResumeQueue(ctx context.Context, userID, envID, threadID string) (*T3DispatchResult, error) {
	return s.simpleQueueCommand(ctx, userID, envID, threadID, map[string]any{
		"type":      "queue.resume",
		"commandId": newT3CommandId(),
		"threadId":  threadID,
	})
}

// SteerQueuedRun 把排队消息上浮为活动运行的转向输入。targetRunId 由服务端
// 解析为当前活动 run；无活动 run → 40916；会话不支持 → 50202（明确报告，
// 不自动降级重试）。
func (s *T3ThreadsService) SteerQueuedRun(ctx context.Context, userID, envID, threadID, runID string) (*T3DispatchResult, error) {
	if runID == "" {
		return nil, errs.ErrInvalidParams
	}
	env, err := s.loadEnvForWrite(ctx, userID, envID)
	if err != nil {
		return nil, err
	}

	ctx, cancel := s.t3WriteBudget(ctx)
	defer cancel()

	token, err := s.bearerToken(env)
	if err != nil {
		return nil, errs.New(errs.ErrT3Upstream.Code, "环境凭证不可用，请重新配对")
	}
	if status, probeErr := s.http.probeSession(ctx, env.Address, token); status != T3EnvironmentStatusOK {
		return nil, errs.New(errs.ErrT3Upstream.Code, probeErr)
	}

	session, err := dialT3RPC(ctx, wsBaseOf(env.Address), token)
	if err != nil {
		return nil, s.upstreamError(err)
	}
	defer session.Close()

	projection, err := s.fetchProjectionForWrite(ctx, env, token, threadID)
	if err != nil {
		return nil, s.upstreamError(err)
	}
	active := resolveT3ActiveRun(projection.Runs)
	if active == nil {
		return nil, errs.New(errs.ErrT3StateConflict.Code, "当前没有可转向的活动运行")
	}
	if queue := deriveT3ThreadQueue(projection); !queue.CanSteer {
		return nil, errs.New(errs.ErrT3Upstream.Code, "该 provider 会话不支持把排队消息转为转向输入")
	}

	sequence, err := s.dispatchT3Command(ctx, session, threadID, map[string]any{
		"type":        "queued-message.promote-to-steer",
		"commandId":   newT3CommandId(),
		"threadId":    threadID,
		"queuedRunId": runID,
		"targetRunId": active.ID,
	})
	if err != nil {
		return nil, err
	}
	return &T3DispatchResult{Sequence: sequence}, nil
}

// CancelQueuedRun 下发 queued-run.cancel。
func (s *T3ThreadsService) CancelQueuedRun(ctx context.Context, userID, envID, threadID, runID string) (*T3DispatchResult, error) {
	if runID == "" {
		return nil, errs.ErrInvalidParams
	}
	return s.simpleQueueCommand(ctx, userID, envID, threadID, map[string]any{
		"type":      "queued-run.cancel",
		"commandId": newT3CommandId(),
		"threadId":  threadID,
		"runId":     runID,
	})
}

// EditQueuedRun 下发 queued-run.edit。attachments 三态（nil=保留原附件，
// []=清空，非空=整组替换），不得把缺省反序列化成空数组。
func (s *T3ThreadsService) EditQueuedRun(ctx context.Context, userID, envID, threadID, runID string, text string, attachments *[]T3ChatAttachment) (*T3DispatchResult, error) {
	if runID == "" {
		return nil, errs.ErrInvalidParams
	}
	// 与发送相同：正文与附件不能同时为空。attachments==nil 表示保留原附件，
	// 允许把误写入的占位正文清回空串。
	if strings.TrimSpace(text) == "" && attachments != nil && len(*attachments) == 0 {
		return nil, errs.ErrInvalidParams
	}
	command := map[string]any{
		"type":      "queued-run.edit",
		"commandId": newT3CommandId(),
		"threadId":  threadID,
		"runId":     runID,
		"text":      text,
	}
	if attachments != nil {
		normalized, err := normalizeT3AttachmentInput(*attachments)
		if err != nil {
			return nil, err
		}
		command["attachments"] = normalized
	}
	return s.simpleQueueCommand(ctx, userID, envID, threadID, command)
}

// ReorderQueuedRun 下发 queued-run.reorder；beforeRunId 为 nil 表示移到队尾。
func (s *T3ThreadsService) ReorderQueuedRun(ctx context.Context, userID, envID, threadID, runID string, beforeRunID *string) (*T3DispatchResult, error) {
	if runID == "" {
		return nil, errs.ErrInvalidParams
	}
	command := map[string]any{
		"type":        "queued-run.reorder",
		"commandId":   newT3CommandId(),
		"threadId":    threadID,
		"runId":       runID,
		"beforeRunId": beforeRunID, // nil → JSON null（移到队尾）
	}
	return s.simpleQueueCommand(ctx, userID, envID, threadID, command)
}

// simpleQueueCommand 探测+拨号后发送单条队列命令。
func (s *T3ThreadsService) simpleQueueCommand(ctx context.Context, userID, envID, threadID string, command map[string]any) (*T3DispatchResult, error) {
	env, err := s.loadEnvForWrite(ctx, userID, envID)
	if err != nil {
		return nil, err
	}

	ctx, cancel := s.t3WriteBudget(ctx)
	defer cancel()

	token, err := s.bearerToken(env)
	if err != nil {
		return nil, errs.New(errs.ErrT3Upstream.Code, "环境凭证不可用，请重新配对")
	}
	if status, probeErr := s.http.probeSession(ctx, env.Address, token); status != T3EnvironmentStatusOK {
		return nil, errs.New(errs.ErrT3Upstream.Code, probeErr)
	}

	session, err := dialT3RPC(ctx, wsBaseOf(env.Address), token)
	if err != nil {
		return nil, s.upstreamError(err)
	}
	defer session.Close()

	sequence, err := s.dispatchT3Command(ctx, session, threadID, command)
	if err != nil {
		return nil, err
	}
	return &T3DispatchResult{Sequence: sequence}, nil
}
