package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/google/uuid"
)

// fs_thread / fs_environment 适配：只操作已连接环境；T3 的 scope/operate 守卫
// 继续由现有 service 执行，不扩大远端权限。

func (s *BotService) execThreadRead(ctx context.Context, call *model.BotToolCall) (string, error) {
	userID := s.botUserIDOf(call.BotID)
	var args botThreadArgs
	if err := json.Unmarshal([]byte(call.ArgumentsJSON), &args); err != nil {
		return "", errs.ErrInvalidParams
	}
	switch call.Action {
	case "list":
		list, err := s.threads.List(ctx, userID)
		if err != nil {
			return "", err
		}
		result, err := botSlimThreadList(list, &args)
		if err != nil {
			return "", err
		}
		return botMarshalResult(result)
	case "get":
		detail, err := s.threads.Detail(ctx, userID, args.EnvID, args.ThreadID)
		if err != nil {
			return "", err
		}
		result, err := botSlimThreadDetail(detail, &args)
		if err != nil {
			return "", err
		}
		return botMarshalResult(result)
	case "item":
		item, err := s.threads.ItemDetail(ctx, userID, args.EnvID, args.ThreadID, args.ItemID)
		if err != nil {
			return "", err
		}
		return botMarshalResult(botSlimThreadItem(item))
	case "composer_options":
		opts, err := s.threads.ComposerOptions(ctx, userID, args.EnvID)
		if err != nil {
			return "", err
		}
		// 只透出启动器需要的项目与模型选择，不透传 T3 全量配置。
		return botMarshalResult(map[string]interface{}{"projects": opts.Projects})
	case "workspace_options":
		opts, err := s.threads.WorkspaceOptions(ctx, userID, args.EnvID, args.ProjectID, args.Query, args.Cursor)
		if err != nil {
			return "", err
		}
		return botMarshalResult(opts)
	}
	return "", errs.New(errs.ErrInvalidParams.Code, "fs_thread 不支持该读取动作")
}

func (s *BotService) execEnvironmentRead(ctx context.Context, call *model.BotToolCall) (string, error) {
	userID := s.botUserIDOf(call.BotID)
	switch call.Action {
	case "list":
		envs, err := s.threads.List(ctx, userID)
		if err != nil {
			return "", err
		}
		out := make([]map[string]interface{}, 0, len(envs.Environments))
		for _, env := range envs.Environments {
			out = append(out, map[string]interface{}{
				"id":     env.Id,
				"name":   env.Name,
				"status": string(env.Status),
				"access": string(env.Access),
			})
		}
		return botMarshalResult(map[string]interface{}{"environments": out})
	case "projects":
		projects, err := s.bindings.DiscoverProjects(ctx, userID)
		if err != nil {
			return "", err
		}
		return botMarshalResult(map[string]interface{}{"projects": projects})
	}
	return "", errs.New(errs.ErrInvalidParams.Code, "fs_environment 不支持该动作")
}

// runThreadWrite 在 worker 内执行 fs_thread 写操作。T3 的 command/thread/
// message ID 在受理时由服务端生成（execution_context），不让模型指定幂等身份。
func (s *BotService) runThreadWrite(ctx context.Context, call *model.BotToolCall) (interface{}, error) {
	userID := s.botUserIDOf(call.BotID)
	var execCtx botCallExecContext
	_ = json.Unmarshal([]byte(call.ExecutionContextJSON), &execCtx)

	var args botThreadArgs
	if err := json.Unmarshal([]byte(call.ArgumentsJSON), &args); err != nil {
		return nil, errs.ErrInvalidParams
	}
	switch call.Action {
	case "launch":
		req := &T3CreateThreadRequest{
			ProjectId: args.ProjectID,
			Message:   botThreadMessage(args.Text),
		}
		if title := strings.TrimSpace(args.Title); title != "" {
			req.Title = &title
		}
		if execCtx.ThreadID != "" {
			req.ThreadId = &execCtx.ThreadID
		}
		// 复用 launchSent 语义：结果只表示启动 RPC 的结果，不代表远端完成。
		result, _, err := s.threads.CreateThreadForAgent(ctx, userID, args.EnvID, req)
		return result, err
	case "send":
		req := &T3SendMessageRequest{Text: args.Text}
		if execCtx.CommandID != "" {
			req.CommandId = &execCtx.CommandID
		}
		if execCtx.MessageID != "" {
			req.MessageId = &execCtx.MessageID
		}
		return s.threads.SendMessage(ctx, userID, args.EnvID, args.ThreadID, req)
	case "stop":
		return s.threads.StopThread(ctx, userID, args.EnvID, args.ThreadID)
	case "settle":
		return s.threads.SettleThread(ctx, userID, args.EnvID, args.ThreadID)
	case "unsettle":
		return s.threads.UnsettleThread(ctx, userID, args.EnvID, args.ThreadID)
	case "resume_queue":
		return s.threads.ResumeQueue(ctx, userID, args.EnvID, args.ThreadID)
	}
	return nil, errs.New(errs.ErrInvalidParams.Code, "fs_thread 不支持该写动作")
}

// botCallExecContext 是调用记录 execution_context 的统一形态：服务端生成的
// T3 幂等身份 + 受理时解析的授权范围。
type botCallExecContext struct {
	ScopeKind string   `json:"scope_kind,omitempty"`
	ScopeID   string   `json:"scope_id,omitempty"`
	EnvID     string   `json:"env_id,omitempty"`
	ThreadID  string   `json:"thread_id,omitempty"`
	CommandID string   `json:"command_id,omitempty"`
	MessageID string   `json:"message_id,omitempty"`
	Fields    []string `json:"fields,omitempty"`
	// Param 与授权规则的参数值级约束一致（如 source=github）：always-allow
	// 重建规则时必须有它，否则后续调用哈希永远对不上。
	Param string `json:"param,omitempty"`
}

func botThreadMessage(text string) api.CreateT3ThreadRequest_Message {
	return api.CreateT3ThreadRequest_Message{Text: text}
}

// resolveThreadScope 建卡前解析 fs_thread 写操作范围。
func (s *BotService) resolveThreadScope(prepared *botPreparedCall, raw map[string]json.RawMessage) {
	envID := rawString(raw["env_id"])
	switch prepared.Action {
	case "launch":
		prepared.ScopeKind = botScopeThreadLaunch
		prepared.ScopeID = rawString(raw["project_id"])
		prepared.EnvironmentID = &envID
	case "send":
		prepared.ScopeKind = botScopeThread
		prepared.ScopeID = rawString(raw["thread_id"])
		prepared.EnvironmentID = &envID
	case "stop":
		prepared.ScopeKind = botScopeThreadStop
		prepared.ScopeID = rawString(raw["thread_id"])
		prepared.EnvironmentID = &envID
	case "settle", "unsettle":
		// 结算/撤销结算按线程授权：把线程收进历史区或恢复，影响的是这一个线程。
		prepared.ScopeKind = botScopeThreadSettle
		prepared.ScopeID = rawString(raw["thread_id"])
		prepared.EnvironmentID = &envID
	case "resume_queue":
		// 恢复队列同样按线程授权。
		prepared.ScopeKind = botScopeThreadQueue
		prepared.ScopeID = rawString(raw["thread_id"])
		prepared.EnvironmentID = &envID
	}
}

// buildThreadExecIDs 在受理时为写操作生成 T3 幂等身份（不让模型指定）。
func buildThreadExecIDs(action string) (threadID, commandID, messageID string) {
	switch action {
	case "launch":
		return uuid.NewString(), "", ""
	case "send":
		return "", uuid.NewString(), uuid.NewString()
	}
	return "", "", ""
}

const (
	botThreadPageDefault = 50
	botThreadPageMax     = 100
	botThreadItemDefault = 50
	botThreadItemMax     = 200
	botThreadTextPreview = 500
)

// botThreadPage 校验线程列表分页：服务端强制页长上限，从机制上排除「一次拉全量」。
func botThreadPage(page, pageSize int) (int, int, error) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = botThreadPageDefault
	}
	if pageSize > botThreadPageMax {
		return 0, 0, errs.New(errs.ErrInvalidParams.Code, fmt.Sprintf("page_size 最大 %d", botThreadPageMax))
	}
	return page, pageSize, nil
}

// botSlimThreadList 过滤、稳定分页并回报覆盖范围。默认排除归档线程（归档
// 不属于巡检范围）；include_archived=true 才纳入。settled 默认排除已结算
// 线程（settle 是把线程收进历史区，与归档同属「不在默认巡检面」）：省略或
// settled=false 只返回未结算线程，settled=true 只返回已结算线程。env_id 收窄
// 到单环境（不在已连接环境里就报错，不静默返回空）；query 对线程 id/标题/
// 项目名/项目 id/环境名做不区分大小写的包含匹配。过滤后按 updated_at 降序、
// id 升序排序，保证同一 filter 下分页稳定。
func botSlimThreadList(list *T3ThreadListResponse, args *botThreadArgs) (map[string]interface{}, error) {
	page, pageSize, err := botThreadPage(args.Page, args.PageSize)
	if err != nil {
		return nil, err
	}
	var updatedAfter time.Time
	if raw := strings.TrimSpace(args.UpdatedAfter); raw != "" {
		parsed, parseErr := time.Parse(time.RFC3339, raw)
		if parseErr != nil {
			return nil, errs.New(errs.ErrInvalidParams.Code, "updated_after 需要 RFC3339 时间")
		}
		updatedAfter = parsed
	}
	includeArchived := args.IncludeArchived != nil && *args.IncludeArchived

	envID := strings.TrimSpace(args.EnvID)
	query := strings.ToLower(strings.TrimSpace(args.Query))
	envNames := make(map[string]string, len(list.Environments))
	for i := range list.Environments {
		envNames[list.Environments[i].Id] = list.Environments[i].Name
	}
	if envID != "" {
		if _, ok := envNames[envID]; !ok {
			return nil, errs.New(errs.ErrInvalidParams.Code, "env_id 不属于已连接环境: "+envID)
		}
	}

	filtered := make([]T3ThreadSummary, 0, len(list.Items))
	for _, thread := range list.Items {
		if !includeArchived && thread.Status == api.T3ThreadSummaryStatusArchived {
			continue
		}
		if envID != "" && thread.EnvironmentId != envID {
			continue
		}
		if args.ProjectID != "" && thread.ProjectId != args.ProjectID {
			continue
		}
		// 默认不含已结算线程（与 settle 描述、帮助文本一致）：省略 settled
		// 等价 settled=false，已结算的要显式 settled=true 才看得到。
		wantSettled := args.Settled != nil && *args.Settled
		if thread.Settled != wantSettled {
			continue
		}
		if args.Status != "" && string(thread.Status) != args.Status {
			continue
		}
		if !updatedAfter.IsZero() {
			when, parseErr := time.Parse(time.RFC3339, thread.UpdatedAt)
			if parseErr != nil || !when.After(updatedAfter) {
				continue
			}
		}
		if query != "" && !botThreadMatchesQuery(&thread, query, envNames) {
			continue
		}
		filtered = append(filtered, thread)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].UpdatedAt != filtered[j].UpdatedAt {
			return filtered[i].UpdatedAt > filtered[j].UpdatedAt
		}
		return filtered[i].Id < filtered[j].Id
	})

	total := len(filtered)
	// offset = (page-1)*page_size，作为 offset 字段随响应自证实际切片起点；
	// 排序键 updated_at 降序 + id 升序，同一 filter 下同一次调用内稳定。
	// 防溢出：page 可能极大，(page-1)*pageSize 相加前先判断边界。
	start := total
	if pageSize > 0 && page-1 <= total/pageSize {
		start = (page - 1) * pageSize
	}
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	items := make([]map[string]interface{}, 0, end-start)
	for i := start; i < end; i++ {
		items = append(items, botSlimThreadSummary(&filtered[i]))
	}

	// 覆盖范围：某环境读不到时如实报告，不把「未读到」说成「不存在」，
	// 也不宣称全部正常。env_id 收窄时只报告该环境的覆盖。
	coverage := make([]map[string]interface{}, 0, len(list.Environments))
	incomplete := make([]string, 0, 2)
	for i := range list.Environments {
		env := &list.Environments[i]
		if envID != "" && env.Id != envID {
			continue
		}
		entry := map[string]interface{}{
			"id":     env.Id,
			"name":   env.Name,
			"status": string(env.Status),
		}
		if env.Error != nil && *env.Error != "" {
			entry["error"] = *env.Error
		}
		if env.Status != T3EnvironmentStatusOK {
			incomplete = append(incomplete, env.Name)
		}
		coverage = append(coverage, entry)
	}
	out := map[string]interface{}{
		"kind":             "threads",
		"items":            items,
		"total_items":      total,
		"returned_items":   len(items),
		"page":             page,
		"page_size":        pageSize,
		"offset":           start,
		"has_more":         end < total,
		"environments":     coverage,
		"include_archived": includeArchived,
	}
	if len(incomplete) > 0 {
		out["incomplete_environments"] = incomplete
		out["note"] = "部分环境未能完整读取，结果不代表全部环境的完整范围"
	}
	return out, nil
}

// botThreadMatchesQuery 对线程 id/标题/项目名/项目 id/环境名做不区分大小写的
// 包含匹配；query 须已 ToLower。任一字段命中即保留该线程。
func botThreadMatchesQuery(thread *T3ThreadSummary, query string, envNames map[string]string) bool {
	haystacks := []string{
		thread.Id,
		thread.Title,
		thread.ProjectName,
		thread.ProjectId,
		envNames[thread.EnvironmentId],
	}
	for _, hay := range haystacks {
		if strings.Contains(strings.ToLower(hay), query) {
			return true
		}
	}
	return false
}

func botSlimThreadSummary(t *T3ThreadSummary) map[string]interface{} {
	out := map[string]interface{}{
		"id":     t.Id,
		"title":  t.Title,
		"status": string(t.Status),
		// run_status 是 T3 原始线程状态：completed/failed/cancelled 在归一化
		// status 里都折叠成 idle，终态区分看这里；但 status 不是 idle 时线程
		// 还没结束（后台 run/工作仍在），run_status 只是最近一次根 run 的状态。
		"run_status":     t.RunStatus,
		"settled":        t.Settled,
		"updated_at":     t.UpdatedAt,
		"environment_id": t.EnvironmentId,
		"project_id":     t.ProjectId,
		// pull_requests 恒为数组（无 PR 即空）；watching 标记该 PR 是否挂着监听。
		"pull_requests": t.PullRequests,
	}
	if t.ProjectName != "" {
		out["project_name"] = t.ProjectName
	}
	if t.ParentThreadId != nil {
		out["parent_thread_id"] = *t.ParentThreadId
	}
	return out
}

// botSlimThreadDetail 返回线程详情：运行态/元数据 + 最近条目（带 item id）
// + 完整历史的分页入口。条目正文用 fs_thread item 按 id 补读。
func botSlimThreadDetail(detail *T3ThreadDetail, args *botThreadArgs) (map[string]interface{}, error) {
	itemLimit := args.ItemLimit
	if itemLimit <= 0 {
		itemLimit = botThreadItemDefault
	}
	if itemLimit > botThreadItemMax {
		return nil, errs.New(errs.ErrInvalidParams.Code, fmt.Sprintf("item_limit 最大 %d", botThreadItemMax))
	}
	offset := args.ItemOffset
	if offset < 0 {
		offset = 0
	}

	all := detail.Items
	total := len(all)
	if offset > total {
		offset = total
	}
	// 偏移从最新一条往前数：offset=0 是最新的 item_limit 条。
	end := total - offset
	if end < 0 {
		end = 0
	}
	start := end - itemLimit
	if start < 0 {
		start = 0
	}
	items := make([]map[string]interface{}, 0, end-start)
	for i := start; i < end; i++ {
		items = append(items, botSlimThreadItemSummary(&all[i]))
	}
	return map[string]interface{}{
		"thread":           botSlimThreadSummary(&detail.Thread),
		"queue":            detail.Queue,
		"activity":         detail.Activity,
		"access":           string(detail.Access),
		"items":            items,
		"total_items":      total,
		"item_offset":      offset,
		"item_limit":       itemLimit,
		"next_item_offset": offset + (end - start),
		"has_more_items":   start > 0,
		"item_note":        "条目按时间升序（从旧到新）；此处是最近的切片，向前翻用 item_offset=next_item_offset，正文用 fs_thread action=item item_id=<id>",
	}, nil
}

// botSlimThreadItemSummary 是详情内条目列表的精简形状：给 id 与短摘要，
// 正文（text）走 fs_thread item 按需补读。
func botSlimThreadItemSummary(item *T3ThreadItem) map[string]interface{} {
	out := map[string]interface{}{
		"id":         item.Id,
		"type":       string(item.Type),
		"status":     item.Status,
		"updated_at": item.UpdatedAt,
	}
	if item.Summary != "" {
		out["summary"] = item.Summary
	}
	if item.DetailAvailable != nil && *item.DetailAvailable {
		out["detail_available"] = true
	}
	if item.Inherited != nil && *item.Inherited {
		out["inherited"] = true
	}
	if item.Text != "" {
		out["text_preview"] = truncateBotRunes(item.Text, botThreadTextPreview)
	}
	botSlimItemAttachChildThreadID(out, item)
	return out
}

func truncateBotRunes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + "…"
}

// botSlimItemAttachChildThreadID 把非空的子线程 ID 带进精简条目：子代理条目
// 只有这里能拿到子线程 ID，是模型压缩后重新进入子线程（fs_thread get）的
// 唯一入口。精简列表与补读条目共用同一段判定，不复制两遍。
func botSlimItemAttachChildThreadID(out map[string]interface{}, item *T3ThreadItem) {
	if item.ChildThreadId != nil && *item.ChildThreadId != "" {
		out["child_thread_id"] = *item.ChildThreadId
	}
}

func botSlimThreadItem(item *T3ThreadItem) map[string]interface{} {
	out := map[string]interface{}{
		"id":         item.Id,
		"type":       string(item.Type),
		"status":     item.Status,
		"updated_at": item.UpdatedAt,
	}
	if item.Summary != "" {
		out["summary"] = item.Summary
	}
	if item.Text != "" {
		out["text"] = item.Text
	}
	botSlimItemAttachChildThreadID(out, item)
	return out
}
