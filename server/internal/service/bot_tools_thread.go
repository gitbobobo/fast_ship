package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
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
		// INT-156：页内逐条补 activity 与末条回复摘要——巡检过去要为「线程
		// 有没有回话」逐线程 get，现在 list 一次带回。空页直接跳过。
		if items, ok := result["items"].([]map[string]interface{}); ok && len(items) > 0 {
			s.botEnrichThreadListItems(ctx, userID, items)
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
		// 透出启动器需要的项目（含 default_model_selection）与归一化的模型
		// 目录，不透传 T3 全量 config/settings；项目标题来自配对的 T3 服务/
		// 仓库，进围栏（INT-136）。providers 供 launch 的 model_selection 直接
		// 回填；not_settable 如实列出机器人不能指定的配置项（INT-159）。
		notSettable := botThreadNotSettable
		botToolsetVer := int64(0)
		// 读发起本次调用的请求所冻结的工具集版本，不读 bots 表活值：
		// 另一条对话线先激活新版本会把行版本推进，本会话仍冻结旧 schema
		// 时 not_settable 标注会缺失（代码审查）。
		if req, err := s.requests.FindByID(call.RequestID); err == nil {
			botToolsetVer = req.ToolsetVersion
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			// 查询失败按 0 处理（旧会话的 not_settable 版本标注会缺，模型
			// 传了新字段仍被受理拒，方向安全），但要留痕便于排查 DB 抖动
			// （代码审查：与 matchAuthorization 的降级留痕同口径）。
			s.logger.Warn("bot 工具集版本查询失败，按未升级处理",
				zap.String("bot_id", call.BotID), zap.Error(err))
		}
		if botToolsetVer > 0 && botToolsetVer < botToolsetVersion {
			// 冻结旧工具集的会话也看得到 providers 目录：如实标注这些字段
			// 本版本不可用，避免模型被引导去传会被受理拒绝的参数（云端审查）。
			notSettable = append(append([]map[string]string{}, botThreadNotSettable...),
				map[string]string{
					"field":  "model_selection/runtime_mode/workspace",
					"reason": fmt.Sprintf("本会话工具集 v%d 不含这些字段（传了会被受理拒绝）；压缩升级到 v%d 后可用", botToolsetVer, botToolsetVersion),
				})
		}
		return botMarshalResult(map[string]interface{}{
			"projects":     opts.Projects,
			"providers":    botSlimComposerProviders(opts.Config),
			"not_settable": notSettable,
		})
	case "workspace_options":
		opts, err := s.threads.WorkspaceOptions(ctx, userID, args.EnvID, args.ProjectID, args.Query, args.Cursor)
		if err != nil {
			return "", err
		}
		// 远程 VCS 引用名与工作树路径同属仓库侧可控文本，进围栏（INT-136）。
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
		// 可选执行配置（INT-159）：未指定时不落任何字段，沿用项目默认。
		if err := botThreadLaunchConfig(&args, req); err != nil {
			return nil, err
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
	// ExecConfig 是 launch 执行配置的规范化 JSON（与摘要同口径）：
	// always-allow 重建规则时写进 constraints_json，授权管理面按值展示。
	ExecConfig string `json:"exec_config,omitempty"`
	// Schedule 是 fs_schedule 确认卡的服务端预览（受理时算好，避免前端重复
	// 实现 cron 解析）。
	Schedule *botScheduleCallPreview `json:"schedule,omitempty"`
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
		// 执行配置参与授权（代码审查 P2）：同一「始终允许」只覆盖相同配置
		// 的 launch——换模型/运行模式/工作区都重新确认。字段集合绑定出现的
		// 字段名，取值摘要绑定具体值：只绑字段拦不住 runtime_mode 从
		// approval-required 换成 full-access 这类同字段不同值的提权。
		prepared.FieldsConstraint = presentFields(raw, "model_selection", "runtime_mode", "workspace")
		if len(prepared.FieldsConstraint) > 0 {
			// 只有携带配置时才写摘要：不带配置的 launch 字段集合与取值摘要
			// 均为空，命中升级前已存在的裸规则（Param 为空），存量授权用户
			// 不会因本次升级被无谓重新确认（代码审查 Note）。
			prepared.ParamConstraint = "exec=" + botThreadExecConfigDigest(raw)
			// 规范化配置随调用账本进授权约束：摘要让规则哈希可区分，但
			// 只有摘要管理面与审计无法看出规则覆盖哪套配置。
			prepared.ExecConfigJSON = string(botThreadExecConfigObject(raw))
		}
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

// botThreadExecConfigDigest 摘要 launch 的执行配置取值（参数级授权约束）：
// 三个配置字段按固定顺序把**规范化后**的 JSON 写入哈希——键序、空白与
// options 数组顺序等纯序列化差异不得产生新授权规则（代码审查：语义相同
// 的配置序列化不同会被重复弹确认）；语义取值差异（含显式默认值与省略的
// 差别）都让规则哈希不同、重新确认。
func botThreadExecConfigDigest(raw map[string]json.RawMessage) string {
	h := sha256.New()
	for _, key := range []string{"model_selection", "runtime_mode", "workspace"} {
		h.Write([]byte{0})
		if v, ok := raw[key]; ok && string(v) != "null" {
			h.Write(botCanonicalExecJSON(v))
		}
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

// botThreadExecConfigObject 把三个执行配置字段归一成单个规范 JSON 对象
// （键按名序、值与摘要同口径归一化）——随授权约束落库，授权管理面能
// 按值展示一条规则覆盖哪套配置，而不是只有一个不可辨认的摘要。
func botThreadExecConfigObject(raw map[string]json.RawMessage) json.RawMessage {
	cfg := map[string]json.RawMessage{}
	for _, key := range []string{"model_selection", "runtime_mode", "workspace"} {
		if v, ok := raw[key]; ok && string(v) != "null" {
			cfg[key] = json.RawMessage(botCanonicalExecJSON(v))
		}
	}
	out, err := json.Marshal(cfg)
	if err != nil {
		return json.RawMessage("{}")
	}
	return out
}

// botCanonicalExecJSON 把配置字段反序列化后重新规范化序列化：对象键按名
// 排序（Go Marshal map 的既有行为）、空白剥离；非法 JSON 透原样（受理侧
// shape 校验先于执行，不会走到这里）。
func botCanonicalExecJSON(v json.RawMessage) []byte {
	var parsed interface{}
	if err := json.Unmarshal(v, &parsed); err != nil {
		return v
	}
	out, err := json.Marshal(botCanonicalValue(parsed))
	if err != nil {
		return v
	}
	return out
}

// botCanonicalValue 递归归一化：model_selection.options 数组语义无序，
// 按元素的规范化字节排序后参与哈希。
func botCanonicalValue(v interface{}) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		for k, e := range t {
			// null 叶子递归丢弃：执行侧 null 与缺省同口径按空值处理，
			// 摘要里保留会把语义相同的配置拆成两条授权规则（代码审查）。
			if e == nil {
				delete(t, k)
				continue
			}
			// 空数组与缺省等价（options:[] 执行侧不进 wire）；空串可选字段
			// 经 NonEmptyPtr 同样按缺省处理——唯 value 例外：options[]
			// 的 value:"" 会原样透传上游，与省略语义不同，不能丢（r27）。
			if arr, ok := e.([]interface{}); ok && len(arr) == 0 {
				delete(t, k)
				continue
			}
			if s, ok := e.(string); ok && strings.TrimSpace(s) == "" && k != "value" {
				delete(t, k)
				continue
			}
			t[k] = botCanonicalValue(e)
		}
		if opts, ok := t["options"].([]interface{}); ok {
			sort.SliceStable(opts, func(i, j int) bool {
				bi, _ := json.Marshal(opts[i])
				bj, _ := json.Marshal(opts[j])
				return string(bi) < string(bj)
			})
		}
		return t
	case []interface{}:
		for i, e := range t {
			t[i] = botCanonicalValue(e)
		}
		return t
	case string:
		// 执行侧（botThreadLaunchConfig）对字符串字段统一 TrimSpace，摘要
		// 按同一口径：仅首尾空白差异的取值命中同一条授权规则（代码审查）。
		return strings.TrimSpace(t)
	default:
		return v
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
// settled=false 只返回未结算线程，settled=true 只返回已结算线程。该默认是
// 服务端行为、对所有工具集版本生效，实际生效的筛选以 settled_filter 随结果
// 回带（附 settled_filter_note 说明），冻结在旧 schema 的会话也能自证口径。
// env_id 收窄到单环境（不在已连接环境里就报错，不静默返回空）；query 对线程
// id/标题/项目名/项目 id/环境名做不区分大小写的包含匹配。过滤后按 updated_at
// 降序、id 升序排序，保证同一 filter 下分页稳定。
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
	// 默认不含已结算线程（与 settle 描述、帮助文本一致）：省略 settled 等价
	// settled=false，已结算的要显式 settled=true 才看得到。这是服务端行为，
	// 对所有工具集版本生效——冻结会话的 schema 仍是旧口径，所以实际生效的
	// 筛选要随结果回带（settled_filter），让任何版本的模型都能自证看到的是
	// 哪一侧。
	wantSettled := args.Settled != nil && *args.Settled

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
		// 默认不含已结算线程：省略 settled 等价 settled=false，已结算的要显式
		// settled=true 才看得到。
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
		// 实际生效的结算筛选随结果回带：旧版冻结 schema 只写了 settled=true
		// 的含义，模型省略 settled 时靠这个字段（与配套说明）知道自己看到的
		// 只是未结算一侧，不会把 total_items 误读成全部线程。
		"settled_filter":      wantSettled,
		"settled_filter_note": "settled_filter 标明本次实际生效的结算筛选：false=默认只返回未结算线程，true=只返回已结算线程（要看历史区传 settled=true）",
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

// botSlimThreadSummary 是线程摘要投影：列表项与详情的 thread 字段共用。
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
	// 线程实际生效的执行配置（INT-159）：launch 不传时用项目默认，回读
	// 确认生效值看这里；provider/model 缺失说明上游未上报。
	if t.Provider != "" {
		out["provider"] = t.Provider
	}
	if t.Model != "" {
		out["model"] = t.Model
	}
	if t.Branch != "" {
		out["branch"] = t.Branch
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
// 正文（text）走 fs_thread item 按需补读。summary/text_preview 是外部内容，
// 进不可信围栏（INT-136）。
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

// fs_thread list 补详情的预算（INT-156）：并发 4、单条 10s，整页再封 25s
// 总闸——巡检只需要「线程有没有回话、还在不在跑」，预算到点放弃单条而不
// 是拖死整页。
const (
	botThreadListEnrichConcurrency = 4
	botThreadListEnrichPerItem     = 10 * time.Second
	botThreadListEnrichTotal       = 25 * time.Second
	// botThreadLastReplyChars 是 last_reply 的字符上限（truncateBotRunes 截断）。
	botThreadLastReplyChars = 300
)

// botEnrichThreadListItems 给一页线程摘要逐条补 activity 与 last_reply（INT-156）：
// last_reply 从详情条目尾部往前找第一条正文非空的 assistant_message。单条失败
// 置 detail_unavailable=true 不中断整页；被预算杀掉的（排队没轮到、或 Detail
// 途中到期）归 detail_skipped=true——「真读不到」与「预算耗尽」必须可分
// （云端复审 P2，回调入口与 err 路径各查一次 ctx）；无助手回复省略
// last_reply；activity 是瘦身投影（botSlimThreadActivity），平静线程只占一个空对象。
func (s *BotService) botEnrichThreadListItems(ctx context.Context, userID string, items []map[string]interface{}) {
	ctx, cancel := context.WithTimeout(ctx, botThreadListEnrichTotal)
	defer cancel()
	runT3Bounded(ctx, len(items), botThreadListEnrichConcurrency, botThreadListEnrichPerItem, func(ctx context.Context, i int) {
		item := items[i]
		if ctx.Err() != nil {
			item["detail_skipped"] = true
			return
		}
		envID, _ := item["environment_id"].(string)
		threadID, _ := item["id"].(string)
		detail, err := s.threads.Detail(ctx, userID, envID, threadID)
		if err != nil {
			// 失败也要分流：本协程预算（单条 10s 或整页总闸）到点杀掉的
			// 读取归 detail_skipped——读到一半被杀不等于详情真读不到。
			if ctx.Err() != nil {
				item["detail_skipped"] = true
			} else {
				item["detail_unavailable"] = true
			}
			return
		}
		item["activity"] = botSlimThreadActivity(detail.Activity)
		for j := len(detail.Items) - 1; j >= 0; j-- {
			if it := &detail.Items[j]; it.Type == api.AssistantMessage && it.Text != "" {
				item["last_reply"] = truncateBotRunes(it.Text, botThreadLastReplyChars)
				break
			}
		}
	})
}

// botSlimThreadActivity 把详情 activity 投影成只含非零信号的瘦身对象：平静线程
// 是空对象（列表大头是平静线程，全字段常驻会把字符增量翻倍）；有活动 run、
// 待应答请求、后台工作或停止中时才出现对应键。
func botSlimThreadActivity(a T3ThreadActivity) map[string]interface{} {
	out := map[string]interface{}{}
	if a.ActiveRunId != nil && *a.ActiveRunId != "" {
		out["active_run_id"] = *a.ActiveRunId
	}
	if a.ActiveRunStatus != nil && *a.ActiveRunStatus != "" {
		out["active_run_status"] = *a.ActiveRunStatus
	}
	if a.HasPendingRequests {
		out["has_pending_requests"] = true
	}
	if a.PendingBackgroundTaskCount > 0 {
		out["pending_background_task_count"] = a.PendingBackgroundTaskCount
	}
	if a.Stopping {
		out["stopping"] = true
	}
	return out
}

// botSlimItemAttachChildThreadID 把非空的子线程 ID 带进精简条目：子代理条目
// 只有这里能拿到子线程 ID，是模型压缩后重新进入子线程（fs_thread get）的
// 唯一入口。精简列表与补读条目共用同一段判定，不复制两遍。
func botSlimItemAttachChildThreadID(out map[string]interface{}, item *T3ThreadItem) {
	if item.ChildThreadId != nil && *item.ChildThreadId != "" {
		out["child_thread_id"] = *item.ChildThreadId
	}
}

// botSlimThreadItem 是条目正文投影；INT-148 的子线程入口与 INT-136 的围栏投影独立保留。
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

// botThreadLaunchConfig 校验并把 launch 可选执行配置（INT-159）写进 REST
// 请求：形状与 T3CreateThreadRequest 一致，缺省不落任何字段（沿用项目
// 默认）。model_selection 缺 instance_id/model、runtime_mode 非枚举值、
// workspace 缺 type 或非枚举值都是调用错误——透给上游会变成难以归因的
// RPC 失败，受理侧就拒掉（botPrepareToolCall 同样调它做受理校验）。
func botThreadLaunchConfig(args *botThreadArgs, req *T3CreateThreadRequest) error {
	if ms := args.ModelSelection; ms != nil {
		instanceID := strings.TrimSpace(ms.InstanceID)
		modelSlug := strings.TrimSpace(ms.Model)
		if instanceID == "" || modelSlug == "" {
			return errs.New(errs.ErrInvalidParams.Code, "model_selection 需要 instance_id 与 model（取值见 composer_options 的 providers）")
		}
		sel := &api.T3ModelSelection{InstanceId: instanceID, Model: modelSlug}
		seenOpt := map[string]bool{}
		for _, opt := range ms.Options {
			optID := strings.TrimSpace(opt.ID)
			if optID == "" {
				return errs.New(errs.ErrInvalidParams.Code, "model_selection.options[].id 不能为空")
			}
			// 重复 id 受理即拒：授权摘要按规范化字节排序 options、执行按原
			// 序透传——[{effort:low},{effort:high}] 与反序命中同一规则但上游
			// 按 last-wins 取值可能不同（代码审查：摘要与执行须同口径）。
			if seenOpt[optID] {
				return errs.New(errs.ErrInvalidParams.Code, "model_selection.options[].id 重复 "+optID)
			}
			seenOpt[optID] = true
			// T3 契约 value 只收 string|boolean（T3ModelOption）；
			// 缺省/null/数字/对象在受理侧拒掉，不透给上游变成 RPC 失败。
			// 字符串 value 与其他字段同口径 TrimSpace：授权摘要对叶子值
			// 统一 trim，执行侧不 trim 会让「 high 」与「high」在执行上
			// 是两个取值却共用一条授权规则（代码审查）。
			value := opt.Value
			if s, ok := value.(string); ok {
				value = strings.TrimSpace(s)
			}
			switch value.(type) {
			case string, bool:
			default:
				return errs.New(errs.ErrInvalidParams.Code, "model_selection.options["+optID+"].value 需要字符串或布尔值")
			}
			sel.Options = append(sel.Options, api.T3ModelOption{Id: optID, Value: value})
		}
		req.ModelSelection = sel
	}
	if mode := strings.TrimSpace(args.RuntimeMode); mode != "" {
		switch mode {
		case "approval-required", "auto-accept-edits", "auto", "full-access":
		default:
			return errs.New(errs.ErrInvalidParams.Code, "未知的 runtime_mode "+mode)
		}
		m := api.CreateT3ThreadRequestRuntimeMode(mode)
		req.RuntimeMode = &m
	}
	if ws := args.Workspace; ws != nil {
		wsType := strings.TrimSpace(ws.Type)
		switch wsType {
		case "root", "worktree", "existing_worktree":
		case "":
			return errs.New(errs.ErrInvalidParams.Code, "workspace 需要 type（root/worktree/existing_worktree）；不需要自定义工作区时省略整个 workspace 字段")
		default:
			return errs.New(errs.ErrInvalidParams.Code, "未知的 workspace.type "+wsType)
		}
		baseRef := strings.TrimSpace(ws.BaseRef)
		branch := strings.TrimSpace(ws.Branch)
		worktreePath := strings.TrimSpace(ws.WorktreePath)
		// 按 type 拒掉下游会静默丢弃的字段组合（云端审查）：root 不消费任何
		// 策略字段（INT-85 共识）；worktree 不用 worktree_path；
		// existing_worktree 只用 worktree_path+branch。不拦的话确认卡会显示
		// 实际不生效的字段，与「无效字段不静默忽略」约定相悖。
		switch wsType {
		case "root":
			if baseRef != "" || branch != "" || worktreePath != "" || ws.StartFromOrigin != nil {
				return errs.New(errs.ErrInvalidParams.Code, "workspace.type=root 不接受 base_ref/branch/worktree_path/start_from_origin：项目主检出不需要工作区参数；要新建工作树请改用 worktree，要复用已有目录请改用 existing_worktree")
			}
		case "worktree":
			if worktreePath != "" {
				return errs.New(errs.ErrInvalidParams.Code, "workspace.type=worktree 不接受 worktree_path：worktree 由服务端新建；要复用已有目录请改用 existing_worktree")
			}
		case "existing_worktree":
			if worktreePath == "" {
				return errs.New(errs.ErrInvalidParams.Code, "existing_worktree 需要提供 worktree_path（已有工作树的绝对路径）；不需要指定目录时请改用 worktree")
			}
			if baseRef != "" || ws.StartFromOrigin != nil {
				return errs.New(errs.ErrInvalidParams.Code, "workspace.type=existing_worktree 不接受 base_ref/start_from_origin：已有工作树只按 worktree_path 与可选 branch 进入")
			}
		}
		req.Workspace = &api.T3ThreadWorkspace{
			Type:            api.T3ThreadWorkspaceType(wsType),
			BaseRef:         api.NonEmptyPtr(&baseRef),
			Branch:          api.NonEmptyPtr(&branch),
			WorktreePath:    api.NonEmptyPtr(&worktreePath),
			StartFromOrigin: ws.StartFromOrigin,
		}
	}
	return nil
}

// botThreadLaunchNestedKeys 白名单校验 launch 嵌套对象的键（云端审查）：
// 顶层 knownKeys 只看一层，model_selection.temperature、workspace.path
// 这类不存在的键会被 Unmarshal 静默丢弃——不报错不生效，确认卡上也看不
// 到，与「无效字段不静默忽略」约定相悖，受理侧即拒。
var botThreadLaunchNestedKeys = map[string][]string{
	"model_selection": {"instance_id", "model", "options"},
	"workspace":       {"type", "base_ref", "branch", "worktree_path", "start_from_origin"},
}

func botValidateThreadLaunchNestedKeys(raw map[string]json.RawMessage) error {
	// runtime_mode 显式空串受理即拒：与 workspace.type="" 同一口径，「字段
	// 缺失」与「字段为空」不等价——空串不能让模型误以为指定了某个模式
	//（实则会静默落 full-access 缺省，代码审查）。
	if blob, ok := raw["runtime_mode"]; ok && string(blob) != "null" {
		var mode string
		if err := json.Unmarshal(blob, &mode); err == nil && strings.TrimSpace(mode) == "" {
			return errs.New(errs.ErrInvalidParams.Code, "runtime_mode 不接受空字符串；不需要指定时省略整个字段（缺省为 full-access）")
		}
	}
	for field, allowed := range botThreadLaunchNestedKeys {
		blob, ok := raw[field]
		if !ok {
			continue
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(blob, &obj); err != nil {
			continue // 非对象形状由结构体反序列化报错，不在这里判
		}
		allow := make(map[string]bool, len(allowed))
		for _, k := range allowed {
			allow[k] = true
		}
		for key := range obj {
			if !allow[key] {
				return errs.New(errs.ErrInvalidParams.Code, "launch "+field+" 不接受字段 "+key)
			}
		}
		if field == "model_selection" {
			var opts []map[string]json.RawMessage
			if err := json.Unmarshal(obj["options"], &opts); err == nil {
				for _, opt := range opts {
					for key := range opt {
						if key != "id" && key != "value" {
							return errs.New(errs.ErrInvalidParams.Code, "launch model_selection.options[] 不接受字段 "+key)
						}
					}
				}
			}
		}
	}
	return nil
}

// botThreadNotSettable 是 composer_options 返回的「机器人不可设置」清单
// （INT-159）：这些配置项存在于 T3 启动面但不由机器人决定，如实列出原因
// 而不是让模型猜哪些能传、哪些会被静默忽略。
var botThreadNotSettable = []map[string]string{
	{"field": "interaction_mode", "reason": "固定为 default，T3 不开放其它取值"},
	{"field": "attachments", "reason": "机器人不支持随消息上传图片附件"},
	{"field": "thread_id/command_id/message_id", "reason": "幂等身份由服务端受理时生成，不能由调用方指定"},
}

// botSlimComposerProviders 把 composer-options 的 config.providers（上游
// camelCase）归一化成蛇形模型目录：跳过停用/未安装/不可用的 provider 与
// 缺 slug 的模型，保留选项描述符（id/type/choices/current_value），供
// launch 的 model_selection 直接回填，机器人侧不硬编码模型清单。
func botSlimComposerProviders(config map[string]interface{}) []map[string]interface{} {
	rawProviders, _ := config["providers"].([]interface{})
	out := make([]map[string]interface{}, 0, len(rawProviders))
	for _, raw := range rawProviders {
		provider, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		instanceID, _ := provider["instanceId"].(string)
		if instanceID == "" {
			continue
		}
		if provider["enabled"] == false || provider["installed"] == false ||
			provider["availability"] == "unavailable" {
			continue
		}
		models := botSlimComposerModels(provider["models"])
		if len(models) == 0 {
			continue
		}
		entry := map[string]interface{}{
			"instance_id": instanceID,
			"models":      models,
		}
		if name, _ := provider["displayName"].(string); name != "" {
			entry["name"] = name
		}
		out = append(out, entry)
	}
	return out
}

// botSlimComposerModels 归一化 provider 的 models 数组（slug 必填）。
func botSlimComposerModels(raw any) []map[string]interface{} {
	list, _ := raw.([]interface{})
	out := make([]map[string]interface{}, 0, len(list))
	for _, item := range list {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		slug, _ := m["slug"].(string)
		if slug == "" {
			continue
		}
		// 模型级可用性按 provider 同口径防御性剔除：上游条目若带
		// enabled/availability/deprecated 标记就遵守，没有这些字段时条件
		// 不命中、行为不变（代码审查：上游字段无本地类型 schema 可查）。
		if m["enabled"] == false || m["availability"] == "unavailable" ||
			m["deprecated"] == true {
			continue
		}
		entry := map[string]interface{}{"slug": slug}
		if name, _ := m["name"].(string); name != "" {
			entry["name"] = name
		}
		if sub, _ := m["subProvider"].(string); sub != "" {
			entry["sub_provider"] = sub
		}
		if m["isDefault"] == true {
			entry["is_default"] = true
		}
		if options := botSlimComposerOptionDescriptors(m["capabilities"]); len(options) > 0 {
			entry["options"] = options
		}
		out = append(out, entry)
	}
	return out
}

// botSlimComposerOptionDescriptors 归一化 capabilities.optionDescriptors：
// 保留有 id 的描述符（select 带 choices；boolean 取 true/false），
// current_value 是 T3 该实例的当前默认，choices[].is_default 标记出厂默认。
func botSlimComposerOptionDescriptors(raw any) []map[string]interface{} {
	caps, _ := raw.(map[string]interface{})
	list, _ := caps["optionDescriptors"].([]interface{})
	out := make([]map[string]interface{}, 0, len(list))
	for _, item := range list {
		d, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		id, _ := d["id"].(string)
		if id == "" {
			continue
		}
		// 只保留机器人能回填的类型（select/boolean；缺省 type 但带 choices
		// 的按 select 计）：number 等其它类型 launch 受理不接受，列出来只会
		// 引导模型发起注定被拒的调用（代码审查）。
		typ, _ := d["type"].(string)
		choices := botSlimComposerChoices(d["options"])
		if typ != "select" && typ != "boolean" && (typ != "" || len(choices) == 0) {
			continue
		}
		entry := map[string]interface{}{"id": id}
		if label, _ := d["label"].(string); label != "" {
			entry["label"] = label
		}
		if typ != "" {
			entry["type"] = typ
		}
		if v, ok := d["currentValue"]; ok && v != nil {
			entry["current_value"] = v
		}
		if len(choices) > 0 {
			entry["choices"] = choices
		}
		out = append(out, entry)
	}
	return out
}

// botSlimComposerChoices 归一化 select 描述符的候选值。
func botSlimComposerChoices(raw any) []map[string]interface{} {
	list, _ := raw.([]interface{})
	out := make([]map[string]interface{}, 0, len(list))
	for _, item := range list {
		c, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		id, _ := c["id"].(string)
		if id == "" {
			continue
		}
		entry := map[string]interface{}{"id": id}
		if label, _ := c["label"].(string); label != "" {
			entry["label"] = label
		}
		if c["isDefault"] == true {
			entry["is_default"] = true
		}
		out = append(out, entry)
	}
	return out
}
