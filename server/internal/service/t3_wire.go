package service

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
)

// ------ T3 线上形状（宽松解码：未知字段忽略，缺失字段零值） ------

type t3ShellSnapshot struct {
	SchemaVersion    int              `json:"schemaVersion"`
	SnapshotSequence int              `json:"snapshotSequence"`
	Projects         []t3ProjectShell `json:"projects"`
	Threads          []t3ThreadShell  `json:"threads"`
	ArchivedThreads  []t3ThreadShell  `json:"archivedThreads"` // 真实活跃快照恒为空；归档走独立 RPC
}

// t3ArchivedShellSnapshot 是 orchestration.getArchivedShellSnapshot 的返回形状：
// 归档线程在 threads 字段（不是活跃快照的 archivedThreads）。
type t3ArchivedShellSnapshot struct {
	SchemaVersion    int              `json:"schemaVersion"`
	SnapshotSequence int              `json:"snapshotSequence"`
	Projects         []t3ProjectShell `json:"projects"`
	Threads          []t3ThreadShell  `json:"threads"`
}

type t3ProjectShell struct {
	ID                    string            `json:"id"`
	Title                 string            `json:"title"`
	WorkspaceRoot         *string           `json:"workspaceRoot"`
	DefaultModelSelection *t3ModelSelection `json:"defaultModelSelection"`
}

type t3ModelSelection struct {
	InstanceID string          `json:"instanceId"`
	Model      string          `json:"model"`
	Options    []t3ModelOption `json:"options"`
}

type t3ModelOption struct {
	ID    string `json:"id"`
	Value any    `json:"value"`
}

// t3ThreadLineage 线程谱系：relationshipToParent ∈ {fork, subagent, null}。
type t3ThreadLineage struct {
	ParentThreadID       string  `json:"parentThreadId"`
	RelationshipToParent *string `json:"relationshipToParent"`
	RootThreadID         string  `json:"rootThreadId"`
}

// t3PendingRuntimeRequest 是 shell 上的待用户处理请求摘要（审批/用户输入等）。
type t3PendingRuntimeRequest struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	CreatedAt string `json:"createdAt"`
}

// t3PendingBackgroundTask 是 shell 上根 run 结束后仍在跑的后台工作摘要
// （contracts orchestrationV2.ts OrchestrationV2PendingBackgroundTask）：
// {taskId, kind, description?, childThreadId?}，kind ∈ {subagent, command,
// monitor, background_task}；未知 kind 上游回退 background_task，kind 缺省
// （kinds 出现前的旧 roster）同样按 background_task 加载。这里只解码 kind。
type t3PendingBackgroundTask struct {
	TaskID string `json:"taskId"`
	Kind   string `json:"kind"`
}

type t3ThreadShell struct {
	ID                    string                   `json:"id"`
	ProjectID             string                   `json:"projectId"`
	Title                 string                   `json:"title"`
	ProviderInstance      string                   `json:"providerInstanceId"`
	ModelSelection        t3ModelSelection         `json:"modelSelection"`
	Branch                *string                  `json:"branch"`
	Status                string                   `json:"status"`
	ActivityRunStatus     *string                  `json:"activityRunStatus"`
	PendingRuntimeRequest *t3PendingRuntimeRequest `json:"pendingRuntimeRequest"`
	// 根 run 结束后仍在跑的后台工作（上游 optional，缺省即无）。缺这层解码
	// 时 shell.status=completed 会被误读成静止——后台 subagent/monitor 还会
	// 唤醒代理继续（G2）。
	PendingBackgroundTasks []t3PendingBackgroundTask `json:"pendingBackgroundTasks"`
	Lineage                *t3ThreadLineage          `json:"lineage"`
	PullRequests           []json.RawMessage         `json:"pullRequests"`
	LinkedPullReq          *json.RawMessage          `json:"linkedPullRequest"`
	BranchPullReq          *json.RawMessage          `json:"branchPullRequest"`
	ArchivedAt             *string                   `json:"archivedAt"`
	SettledOverride        *string                   `json:"settledOverride"`
	LatestRunID            *string                   `json:"latestRunId"`
	LatestRunStartedAt     *string                   `json:"latestRunStartedAt"`
	ActiveRunID            *string                   `json:"activeRunId"`
	UpdatedAt              string                    `json:"updatedAt"`
}

// t3AppThread 是 HTTP 投影 thread 字段的真实形状（OrchestrationV2AppThread，
// contracts orchestrationV2.ts:363）：只有线程事实，没有运行态——
// status/activityRunStatus/pendingRuntimeRequest 只存在于 ThreadShell
// （orchestrationV2.ts:1927），由 WS 快照侧的 threadShellFromProjection 派生。
// 这里只解码归一化用到的字段，未知字段忽略。
type t3AppThread struct {
	ID                     string           `json:"id"`
	ProjectID              string           `json:"projectId"`
	Title                  string           `json:"title"`
	ActiveProviderThreadId *string          `json:"activeProviderThreadId"`
	Lineage                *t3ThreadLineage `json:"lineage"`
	ArchivedAt             *string          `json:"archivedAt"`
	UpdatedAt              string           `json:"updatedAt"`
}

// t3ThreadProjection 是 HTTP 快照端点返回的完整投影。
// 时间线以 visibleTurnItems 为准：fork 线程的继承前缀只在这里，
// 回滚/取消的条目也已被上游按其可见性规则排除。
// 队列/活动派生与请求条目补齐另读 runs/messages/providerSessions/
// providerThreads/providerTurns/runtimeRequests/turnItems（contracts
// orchestrationV2.ts OrchestrationV2ThreadProjection）。
type t3ThreadProjection struct {
	Thread           t3AppThread          `json:"thread"`
	Runs             []t3Run              `json:"runs"`
	Messages         []t3Message          `json:"messages"`
	ProviderSessions []t3ProviderSession  `json:"providerSessions"`
	ProviderThreads  []t3ProviderThread   `json:"providerThreads"`
	ProviderTurns    []t3ProviderTurn     `json:"providerTurns"`
	RuntimeRequests  []t3RuntimeRequest   `json:"runtimeRequests"`
	TurnItems        []json.RawMessage    `json:"turnItems"`
	VisibleTurnItems []t3ProjectedTurnRow `json:"visibleTurnItems"`
}

// t3Run 是投影里的 run 行（只解码派生队列/活动用到的字段）。
type t3Run struct {
	ID               string  `json:"id"`
	Ordinal          int     `json:"ordinal"`
	Status           string  `json:"status"`
	QueuePosition    *int    `json:"queuePosition"`
	QueueHeld        *bool   `json:"queueHeld"`
	UserMessageID    string  `json:"userMessageId"`
	ProviderThreadID *string `json:"providerThreadId"`
	ActiveAttemptID  *string `json:"activeAttemptId"`
}

// t3Message 是投影里的会话消息行（排队消息的正文/附件来源）。
type t3Message struct {
	ID                  string            `json:"id"`
	Text                string            `json:"text"`
	Attachments         []json.RawMessage `json:"attachments"`
	DelegatedCompletion *json.RawMessage  `json:"delegatedCompletion"`
	NotificationPayload *json.RawMessage  `json:"notification"`
}

// t3TurnCapabilities 是 provider 会话声明的回合能力
// （threadWorkflows.ts 的 capabilities.turns）。
type t3TurnCapabilities struct {
	SupportsQueuedMessages             *bool `json:"supportsQueuedMessages"`
	SupportsActiveSteering             *bool `json:"supportsActiveSteering"`
	SupportsSteeringByInterruptRestart *bool `json:"supportsSteeringByInterruptRestart"`
}

type t3SessionCapabilities struct {
	Turns t3TurnCapabilities `json:"turns"`
}

// t3ProviderSession 是投影里的 provider 会话行（能力与状态）。
type t3ProviderSession struct {
	ID           string                `json:"id"`
	Status       string                `json:"status"`
	Capabilities t3SessionCapabilities `json:"capabilities"`
}

// t3ProviderThread 是 provider 线程行（把 run/thread 关到 provider 会话）。
type t3ProviderThread struct {
	ID                 string  `json:"id"`
	AppThreadID        *string `json:"appThreadId"`
	ProviderSessionID  *string `json:"providerSessionId"`
	ProviderInstanceID *string `json:"providerInstanceId"`
}

// t3ProviderTurn 是 provider 回合行（steer 可行性判定用）。
type t3ProviderTurn struct {
	RunAttemptID *string `json:"runAttemptId"`
	Status       string  `json:"status"`
}

// t3RuntimeRequest 是运行时请求记录（审批/提问条目的状态与应答通道来源）。
type t3RuntimeRequest struct {
	ID                 string          `json:"id"`
	Status             string          `json:"status"`
	ResponseCapability json.RawMessage `json:"responseCapability"`
	Decision           *string         `json:"decision"`
	Answers            map[string]any  `json:"answers"`
}

// t3ShellSnapshots 是同一 WS 会话取回的活跃 + 归档两份 shell 快照。
// 列表与详情共用；两侧都含子代理线程，由调用方决定是否按 lineage 过滤。
// ArchiveErr 记录归档面的读取失败（活跃快照已到手后归档 RPC 超时/出错/
// 响应损坏）：失败不推翻活跃面，Archived 保持零值。两份快照是否齐全由
// ArchiveErr==nil 表达，调用方按各自语义降级——列表保留活跃线程并标环境
// 降级；详情在活跃面也找不到该线程时不得借归档缺失断言它不存在（G1）。
type t3ShellSnapshots struct {
	Active     t3ShellSnapshot
	Archived   t3ArchivedShellSnapshot
	ArchiveErr error
}

// projectNames 合并两份快照的项目表。
func (snaps *t3ShellSnapshots) projectNames() map[string]string {
	names := make(map[string]string, len(snaps.Active.Projects)+len(snaps.Archived.Projects))
	for _, source := range [][]t3ProjectShell{snaps.Active.Projects, snaps.Archived.Projects} {
		for _, project := range source {
			names[project.ID] = project.Title
		}
	}
	return names
}

// findThread 跨活跃/归档快照按线程 ID 找可信 shell。不按 lineage 过滤——
// 详情也要能进入子代理子线程（排除只适用于列表）。返回该线程的 shell 与
// 项目名表；找不到即快照面无此线程，调用方明确失败。
func (snaps *t3ShellSnapshots) findThread(threadID string) (*t3ThreadShell, map[string]string, bool) {
	projectNames := snaps.projectNames()
	for _, source := range [][]t3ThreadShell{snaps.Active.Threads, snaps.Archived.Threads} {
		for i := range source {
			if source[i].ID == threadID {
				return &source[i], projectNames, true
			}
		}
	}
	return nil, projectNames, false
}

// t3ProjectedTurnRow 是投影里的一行可见条目：来源身份 + 条目本体。
type t3ProjectedTurnRow struct {
	Position       int             `json:"position"`
	Visibility     string          `json:"visibility"`
	SourceThreadID string          `json:"sourceThreadId"`
	SourceItemID   string          `json:"sourceItemId"`
	Item           json.RawMessage `json:"item"`
}

// ------ 归一化 ------

// t3IsSubagentThread 与 T3 自身列表口径一致：lineage.relationshipToParent=subagent
// 一律从列表排除（app 侧 delegate_task 子线程与 provider 原生子代理都排除）；
// 详情仍可通过父条目的 child_thread_id 访问。
func t3IsSubagentThread(shell *t3ThreadShell) bool {
	return shell.Lineage != nil && shell.Lineage.RelationshipToParent != nil &&
		*shell.Lineage.RelationshipToParent == "subagent"
}

// t3ThreadStatus 线程运行态 → Fast Ship 状态，语义对齐 T3 Sidebar：
// archivedAt 最优先；其次等待用户（pendingRuntimeRequest，含审批/输入/代答的
// secret）；再看活动运行（activityRunStatus，后台 run 未停时 shell.status 可能
// 已是 completed/idle）；然后是占完成位的后台工作（pendingBackgroundTasks，
// 见下）；最后 shell.status 兜底（旧服务端无 activityRunStatus）。
func t3ThreadStatus(shell *t3ThreadShell) T3ThreadSummaryStatus {
	if shell.ArchivedAt != nil && *shell.ArchivedAt != "" {
		return api.Archived
	}
	if shell.PendingRuntimeRequest != nil {
		return api.Waiting
	}
	if shell.ActivityRunStatus != nil {
		switch *shell.ActivityRunStatus {
		case "preparing", "starting", "running":
			return api.Running
		case "waiting":
			return api.Waiting
		}
	}
	// 后台工作占位（G2）：根 run 结束后仍在跑的后台 subagent/monitor 会唤醒
	// 代理继续（上游 backgroundWorkHoldsCompletion，orchestrationV2PendingBackgroundWork.ts：
	// kind=command 不占完成位——dev server 之类可长跑数小时；其余一切 kind
	// 含未知都占；kind 缺省按 background_task 加载，故空串同样占位）。
	// 存在占位工作 → running：线程还有会唤醒代理的工作，详情保持轮询——
	// 不是「等待输入或审批」。判定置于 pendingRuntimeRequest 与
	// activityRunStatus 之后、shell.status 兜底之前。
	for _, task := range shell.PendingBackgroundTasks {
		if task.Kind != "command" {
			return api.Running
		}
	}
	switch shell.Status {
	case "preparing", "queued", "starting", "running":
		return api.Running
	case "waiting":
		return api.Waiting
	default:
		return api.Idle
	}
}

func normalizeT3ThreadShell(env *model.T3Environment, shell *t3ThreadShell, projectNames map[string]string) T3ThreadSummary {
	branch := ""
	if shell.Branch != nil {
		branch = *shell.Branch
	}
	projectName := ""
	if projectNames != nil {
		projectName = projectNames[shell.ProjectID]
	}
	var parentThreadID *string
	if t3IsSubagentThread(shell) && shell.Lineage.ParentThreadID != "" {
		parentThreadID = api.Ptr(shell.Lineage.ParentThreadID)
	}
	return T3ThreadSummary{
		Id:              shell.ID,
		EnvironmentId:   env.ID,
		EnvironmentName: env.Name,
		ProjectId:       shell.ProjectID,
		ProjectName:     projectName,
		Title:           shell.Title,
		Status:          t3ThreadStatus(shell),
		// settled 是用户维度（在 T3 里收进历史区），与运行态 status 正交：
		// 归档线程 settledOverride 也可能为 settled，筛选语义由前端决定。
		Settled:          api.Deref(shell.SettledOverride) == "settled",
		RunStartedAt:     shell.LatestRunStartedAt,
		Branch:           branch,
		Provider:         shell.ModelSelection.InstanceID,
		Model:            shell.ModelSelection.Model,
		UpdatedAt:        shell.UpdatedAt,
		PullRequestCount: t3PullRequestCount(shell),
		ParentThreadId:   parentThreadID,
	}
}

func t3PullRequestCount(shell *t3ThreadShell) int {
	if len(shell.PullRequests) > 0 {
		return len(shell.PullRequests)
	}
	count := 0
	if shell.LinkedPullReq != nil {
		count++
	}
	if shell.BranchPullReq != nil {
		count++
	}
	return count
}

// normalizeT3ThreadDetail 把 HTTP 投影 + 可信 WS shell 组装成线程详情。
//
// 投影的 thread 是 AppThread（orchestrationV2.ts:363），不带运行态字段；
// 运行态与元数据（title/provider/model/branch/PR 数/project_name）取该线程
// 的 ThreadShell（WS 快照侧由 threadShellFromProjection 派生），与列表共用
// 同一状态映射，不扫描历史 runs。shell 由调用方按线程 ID 在活跃/归档快照中
// 找出（含子代理子线程），找不到时调用方明确失败，不假 idle。
// 审批/提问条目按投影 runtimeRequests 补齐状态；queue/activity 由投影
// runs/messages/会话能力派生（口径对齐 T3 deriveThreadQueueWorkflowState）。
func normalizeT3ThreadDetail(env *model.T3Environment, projection json.RawMessage, shell *t3ThreadShell, projectNames map[string]string) (*T3ThreadDetail, error) {
	parsed, err := decodeT3Projection(projection)
	if err != nil {
		return nil, err
	}

	summary := normalizeT3ThreadShell(env, shell, projectNames)

	items := make([]T3ThreadItem, 0, len(parsed.VisibleTurnItems))
	for _, row := range parsed.VisibleTurnItems {
		item, ok := normalizeT3TurnItem(row.SourceThreadID, row.Item)
		if ok {
			annotateT3ItemOrigin(&item, row)
			items = append(items, item)
		}
	}
	enrichT3RequestItems(items, parsed.RuntimeRequests)
	access := T3EnvironmentAccessReadOnly
	if t3HasOperateScope(env.BearerScope) {
		access = T3EnvironmentAccessOperate
	}
	return &T3ThreadDetail{
		Thread:   summary,
		Items:    items,
		Queue:    *deriveT3ThreadQueue(parsed),
		Activity: *deriveT3ThreadActivity(parsed, shell),
		Access:   access,
	}, nil
}

func decodeT3Projection(projection json.RawMessage) (*t3ThreadProjection, error) {
	var parsed t3ThreadProjection
	if err := json.Unmarshal(projection, &parsed); err != nil {
		return nil, err
	}
	rows := parsed.VisibleTurnItems
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Position < rows[j].Position })
	parsed.VisibleTurnItems = rows
	return &parsed, nil
}

func decodeT3VisibleRows(projection json.RawMessage) ([]t3ProjectedTurnRow, error) {
	parsed, err := decodeT3Projection(projection)
	if err != nil {
		return nil, err
	}
	return parsed.VisibleTurnItems, nil
}

// t3StableItemID 由可见行的来源身份（sourceThreadId + 原始条目 id）合成
// Fast Ship 条目 ID：继承行与本地行的原始 id 可能重复，靠来源线程区分。
func t3StableItemID(sourceThreadID, sourceItemID string) string {
	return sourceThreadID + "::" + sourceItemID
}

// annotateT3ItemOrigin 把可见行的来源/继承标记写到条目上，供前端判断
// 审批/提问/密钥能否在本线程作答。
func annotateT3ItemOrigin(item *T3ThreadItem, row t3ProjectedTurnRow) {
	if item == nil {
		return
	}
	item.SourceThreadId = api.NonEmpty(row.SourceThreadID)
	if row.Visibility == "inherited" {
		item.Inherited = api.True(true)
	}
}

func findT3VisibleRow(rows []t3ProjectedTurnRow, itemID string) (*t3ProjectedTurnRow, bool) {
	for i := range rows {
		if t3StableItemID(rows[i].SourceThreadID, rows[i].SourceItemID) == itemID {
			return &rows[i], true
		}
	}
	return nil, false
}

// normalizeT3TurnItem 把 T3 可见行归一化成 Fast Ship 线程条目（快照形态）。
//
// 映射（详见 docs/internal/threads.md）：
//
//	user_message/assistant_message/reasoning → 同名，text=快照文本；
//	            user_message 另带 attachments[]（图片元数据）
//	proposed_plan → assistant_message（text=markdown）
//	command_execution → text=命令，summary=命令首行；wire 投影已裁掉输出，
//	                    detail_available=true（展开经 getTurnItem 补读完整正文）
//	dynamic_tool/file_search/web_search/todo_list → dynamic_tool；工具类 detail_available=true
//	file_change → 真实 metadata（文件名/增删行数/子操作）；上游 detail 也不提供
//	              diff（专用 diff 读路径不属于本集成），detail_available=false
//	subagent → text=任务，summary=任务首行，child_thread_id 带出；detail_available=true
//	error → text=失败信息，summary=失败类别
//	approval_request/user_input_request → 交互条目（request_id 带出，状态/
//	            应答通道由 enrichT3RequestItems 按投影 runtimeRequests 补齐）
//	secret_request → 密钥请求（label/reason/placeholder/secret_status；
//	            绝不携带密钥值）
//	结构性事件（checkpoint、handoff、fork 标记等）→ 丢弃
//
// 正文不额外截断（上游裁剪如实保留）；summary 是 rune 安全的单行短摘要；
// updated_at 是上游条目的 updatedAt 原样透传（条目自带版本，上游省略即缺省）。
func normalizeT3TurnItem(sourceThreadID string, raw json.RawMessage) (T3ThreadItem, bool) {
	var base struct {
		ID        string `json:"id"`
		Type      string `json:"type"`
		Status    string `json:"status"`
		UpdatedAt string `json:"updatedAt"`
	}
	if err := json.Unmarshal(raw, &base); err != nil || base.ID == "" {
		return T3ThreadItem{}, false
	}

	item := T3ThreadItem{
		Id:        t3StableItemID(sourceThreadID, base.ID),
		Status:    api.NonEmpty(base.Status),
		UpdatedAt: api.NonEmpty(base.UpdatedAt),
	}

	switch base.Type {
	case "user_message":
		var m struct {
			Text        string            `json:"text"`
			Attachments []json.RawMessage `json:"attachments"`
		}
		if !mustDecode(raw, &m) {
			return T3ThreadItem{}, false
		}
		item.Type = T3ThreadItemTypeUserMessage
		item.Text = m.Text
		item.Attachments = normalizeT3ChatAttachments(m.Attachments)
	case "assistant_message":
		var m struct {
			Text string `json:"text"`
		}
		if !mustDecode(raw, &m) {
			return T3ThreadItem{}, false
		}
		item.Type = T3ThreadItemTypeAssistantMessage
		item.Text = m.Text
		item.Summary = firstT3Line(m.Text)
	case "proposed_plan":
		var m struct {
			Markdown string `json:"markdown"`
		}
		if !mustDecode(raw, &m) {
			return T3ThreadItem{}, false
		}
		item.Type = T3ThreadItemTypeAssistantMessage
		item.Text = m.Markdown
		item.Summary = firstT3Line(m.Markdown)
	case "reasoning":
		var m struct {
			Text string `json:"text"`
		}
		if !mustDecode(raw, &m) {
			return T3ThreadItem{}, false
		}
		item.Type = T3ThreadItemTypeReasoning
		item.Text = m.Text
		item.Summary = firstT3Line(m.Text)
	case "command_execution":
		var m struct {
			Input         string `json:"input"`
			OutputOmitted bool   `json:"outputOmitted"`
		}
		if !mustDecode(raw, &m) {
			return T3ThreadItem{}, false
		}
		item.Type = T3ThreadItemTypeCommandExecution
		item.Text = m.Input
		item.Summary = firstT3Line(m.Input)
		item.DetailAvailable = api.True(true)
	case "file_change":
		var m t3FileChangeItem
		if !mustDecode(raw, &m) {
			return T3ThreadItem{}, false
		}
		item.Type = T3ThreadItemTypeFileChange
		item.Summary = t3FileChangeSummary(m.FileName, m.Additions, m.Deletions)
		item.Text = t3FileChangeMetadataText(&m)
	case "subagent":
		var m struct {
			Prompt        string  `json:"prompt"`
			ChildThreadID *string `json:"childThreadId"`
		}
		if !mustDecode(raw, &m) {
			return T3ThreadItem{}, false
		}
		item.Type = T3ThreadItemTypeSubagent
		item.Text = m.Prompt
		item.Summary = firstT3Line(m.Prompt)
		item.ChildThreadId = m.ChildThreadID
		item.DetailAvailable = api.True(true)
	case "error":
		var m struct {
			Failure struct {
				Class   string `json:"class"`
				Message string `json:"message"`
			} `json:"failure"`
		}
		if !mustDecode(raw, &m) {
			return T3ThreadItem{}, false
		}
		item.Type = T3ThreadTypeError
		item.Text = m.Failure.Message
		item.Summary = m.Failure.Class
	case "approval_request":
		// 审批请求：request_id 关联投影 runtimeRequests（状态/应答通道/已定
		// decision 由 Detail/ItemDetail 的 enrich 补齐）；options 用请求自带
		// 的（缺省请求由前端按 Approve/Decline/Always allow this session/
		// Cancel 展示，服务端不自增 acceptAlways）。
		var m struct {
			RequestID   string             `json:"requestId"`
			RequestKind string             `json:"requestKind"`
			Prompt      *string            `json:"prompt"`
			AppName     *string            `json:"appName"`
			Options     []t3ApprovalOption `json:"options"`
		}
		if !mustDecode(raw, &m) || m.RequestID == "" {
			return T3ThreadItem{}, false
		}
		item.Type = T3ThreadItemTypeApprovalRequest
		item.RequestId = api.Ptr(m.RequestID)
		item.RequestKind = api.NonEmpty(m.RequestKind)
		item.Prompt = api.NonEmptyPtr(m.Prompt)
		item.AppName = api.NonEmptyPtr(m.AppName)
		item.Options = normalizeT3ApprovalOptions(m.Options)
		item.Summary = firstT3Line(api.Deref(m.Prompt))
		if item.Summary == "" && item.AppName != nil {
			item.Summary = *item.AppName
		}
	case "user_input_request":
		// 用户输入请求：问题列表原样带出；条目上的 answers（questionAnswer）
		// 先落进来，runtimeRequests 补齐由 enrich 覆盖。
		var m struct {
			RequestID    string                `json:"requestId"`
			Questions    []t3UserInputQuestion `json:"questions"`
			ResponseMode *string               `json:"responseMode"`
			Answers      map[string]any        `json:"answers"`
		}
		if !mustDecode(raw, &m) || m.RequestID == "" {
			return T3ThreadItem{}, false
		}
		item.Type = T3ThreadItemTypeUserInputRequest
		item.RequestId = api.Ptr(m.RequestID)
		item.Questions = normalizeT3UserInputQuestions(m.Questions)
		item.ResponseMode = api.NonEmptyPtr(m.ResponseMode)
		item.Answers = m.Answers
		if len(item.Questions) > 0 {
			item.Summary = firstT3Line(item.Questions[0].Question)
		}
	case "secret_request":
		// 密钥请求：只带「问了什么/答了没有」，绝不携带密钥值。
		var m struct {
			Label        string  `json:"label"`
			Reason       string  `json:"reason"`
			Placeholder  *string `json:"placeholder"`
			SecretStatus string  `json:"secretStatus"`
		}
		if !mustDecode(raw, &m) || m.Label == "" {
			return T3ThreadItem{}, false
		}
		item.Type = T3ThreadItemTypeSecretRequest
		item.Label = api.Ptr(m.Label)
		item.Text = m.Reason
		item.Reason = api.Ptr(m.Reason)
		item.Placeholder = api.NonEmptyPtr(m.Placeholder)
		item.SecretStatus = api.NonEmpty(m.SecretStatus)
		item.Summary = firstT3Line(m.Label)
	case "dynamic_tool", "file_search", "web_search", "todo_list":
		item.Type = T3ThreadItemTypeDynamicTool
		item.Summary = t3ToolItemSummary(base.Type, raw)
		item.Text = t3ToolItemText(base.Type, raw)
		item.DetailAvailable = api.True(base.Type == "dynamic_tool") // 输出经 getTurnItem 补读；搜索/待办快照即全部
	default:
		// 未知/结构性类型：前向兼容地丢弃。
		return T3ThreadItem{}, false
	}
	return item, true
}

// t3FileChangeDetail 是 file_change.changes[] 的子操作（operation/path 等）。
type t3FileChangeDetail struct {
	Operation string  `json:"operation"`
	Path      string  `json:"path"`
	OldPath   *string `json:"oldPath"`
}

// t3FileChangeItem 是 file_change 条目的快照形态：diffStr 仅失败态保留
// （上游截断标注），成功态只有文件名/行数/子操作等元数据。
type t3FileChangeItem struct {
	FileName  string               `json:"fileName"`
	Additions *int                 `json:"additions"`
	Deletions *int                 `json:"deletions"`
	Changes   []t3FileChangeDetail `json:"changes"`
	DiffStr   *string              `json:"diffStr"`
}

func t3FileChangeSummary(fileName string, additions, deletions *int) string {
	if additions == nil && deletions == nil {
		return fileName
	}
	return fmt.Sprintf("%s (+%d/-%d)", fileName, t3IntOrZero(additions), t3IntOrZero(deletions))
}

// t3FileChangeMetadataText 组装文件变更的真实元数据（文件名/增删行数/子操作/
// 失败态截断 diff）。不调用 diff 读路径——那是本集成之外的读面。
func t3FileChangeMetadataText(m *t3FileChangeItem) string {
	var b strings.Builder
	b.WriteString(m.FileName)
	if m.Additions != nil || m.Deletions != nil {
		fmt.Fprintf(&b, "\n+%d/-%d", t3IntOrZero(m.Additions), t3IntOrZero(m.Deletions))
	}
	for _, change := range m.Changes {
		b.WriteString("\n")
		b.WriteString(change.Operation)
		b.WriteString(" ")
		b.WriteString(change.Path)
		if change.OldPath != nil && *change.OldPath != "" {
			b.WriteString(" ← ")
			b.WriteString(*change.OldPath)
		}
	}
	if m.DiffStr != nil && strings.TrimSpace(*m.DiffStr) != "" {
		b.WriteString("\n\n")
		b.WriteString(*m.DiffStr)
	}
	return b.String()
}

// normalizeT3TurnItemFull 把 getTurnItem 补读结果归一化成完整正文条目：
// text 含命令 input+output / subagent prompt+result+progress / 工具全量
// input+output；上游 256KB 截断后缀原样保留（如实显示）。响应里
// detail_available=false——已无可再取的正文。updated_at 同快照形态，取
// 补读条目自带的 updatedAt 原样透传。
func normalizeT3TurnItemFull(sourceThreadID string, raw json.RawMessage) (T3ThreadItem, bool) {
	var base struct {
		ID        string `json:"id"`
		Type      string `json:"type"`
		Status    string `json:"status"`
		UpdatedAt string `json:"updatedAt"`
	}
	if err := json.Unmarshal(raw, &base); err != nil || base.ID == "" {
		return T3ThreadItem{}, false
	}

	item := T3ThreadItem{
		Id:        t3StableItemID(sourceThreadID, base.ID),
		Status:    api.NonEmpty(base.Status),
		UpdatedAt: api.NonEmpty(base.UpdatedAt),
	}

	switch base.Type {
	case "command_execution":
		var m struct {
			Input  string  `json:"input"`
			Output *string `json:"output"`
		}
		if !mustDecode(raw, &m) {
			return T3ThreadItem{}, false
		}
		item.Type = T3ThreadItemTypeCommandExecution
		item.Text = t3CommandFullText(m.Input, m.Output)
		item.Summary = firstT3Line(m.Input)
	case "dynamic_tool":
		var m t3DynamicToolFullItem
		if !mustDecode(raw, &m) {
			return T3ThreadItem{}, false
		}
		item.Type = T3ThreadItemTypeDynamicTool
		item.Summary = t3ToolItemSummary(base.Type, raw)
		item.Text = t3ToolFullText(&m)
	case "subagent":
		var m struct {
			Prompt        string  `json:"prompt"`
			Result        *string `json:"result"`
			Progress      *string `json:"progress"`
			ChildThreadID *string `json:"childThreadId"`
		}
		if !mustDecode(raw, &m) {
			return T3ThreadItem{}, false
		}
		item.Type = T3ThreadItemTypeSubagent
		item.Text = t3SubagentFullText(m.Prompt, m.Result, m.Progress)
		item.Summary = firstT3Line(m.Prompt)
		item.ChildThreadId = m.ChildThreadID
	default:
		// 其余类型 detail_available=false，不会走到这里；防御性拒绝。
		return T3ThreadItem{}, false
	}
	return item, true
}

func t3CommandFullText(input string, output *string) string {
	text := input
	if output != nil && strings.TrimSpace(*output) != "" {
		text += "\n\n--- 输出 ---\n\n" + *output
	}
	return text
}

func t3SubagentFullText(prompt string, result, progress *string) string {
	text := prompt
	if result != nil && strings.TrimSpace(*result) != "" {
		text += "\n\n--- 结果 ---\n\n" + *result
	}
	if progress != nil && strings.TrimSpace(*progress) != "" {
		text += "\n\n--- 进度 ---\n\n" + *progress
	}
	return text
}

// t3DynamicToolFullItem 是 getTurnItem 补读返回的 dynamic_tool 形态：
// 真实 input/output（上游各自限界），无 wire 层的省略标注。
type t3DynamicToolFullItem struct {
	ToolName *string         `json:"toolName"`
	Input    json.RawMessage `json:"input"`
	Output   json.RawMessage `json:"output"`
}

// t3ToolFullText 组装 dynamic_tool 的全量负载 JSON（真实 input/output，
// 无省略标注）。
func t3ToolFullText(m *t3DynamicToolFullItem) string {
	payload := map[string]any{"item_type": "dynamic_tool"}
	if m.ToolName != nil {
		payload["tool_name"] = *m.ToolName
	}
	payload["input"] = t3RawOrEmpty(m.Input)
	payload["output"] = t3RawOrEmpty(m.Output)
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "dynamic_tool"
	}
	return string(encoded)
}

// t3ToolItemText 提取工具条目的核心负载，输出干净的 JSON 文本
// （输入/输出/结果字段，剥离 id/status 等信封字段）。
func t3ToolItemText(itemType string, raw json.RawMessage) string {
	payload := map[string]any{"item_type": itemType}
	switch itemType {
	case "dynamic_tool":
		var m struct {
			ToolName      *string         `json:"toolName"`
			Input         json.RawMessage `json:"input"`
			Output        json.RawMessage `json:"output"`
			OutputOmitted bool            `json:"outputOmitted"`
		}
		if err := json.Unmarshal(raw, &m); err == nil {
			if m.ToolName != nil {
				payload["tool_name"] = *m.ToolName
			}
			payload["input"] = t3RawOrEmpty(m.Input)
			payload["output"] = t3RawOrEmpty(m.Output)
			payload["output_omitted"] = m.OutputOmitted
		}
	case "file_search":
		var m struct {
			Pattern *string         `json:"pattern"`
			Results json.RawMessage `json:"results"`
		}
		if err := json.Unmarshal(raw, &m); err == nil {
			if m.Pattern != nil {
				payload["pattern"] = *m.Pattern
			}
			payload["results"] = t3RawOrEmpty(m.Results)
		}
	case "web_search":
		var m struct {
			Patterns json.RawMessage `json:"patterns"`
			Results  json.RawMessage `json:"results"`
		}
		if err := json.Unmarshal(raw, &m); err == nil {
			payload["patterns"] = t3RawOrEmpty(m.Patterns)
			payload["results"] = t3RawOrEmpty(m.Results)
		}
	case "todo_list":
		var m struct {
			Steps       json.RawMessage `json:"steps"`
			Explanation *string         `json:"explanation"`
		}
		if err := json.Unmarshal(raw, &m); err == nil {
			payload["steps"] = t3RawOrEmpty(m.Steps)
			if m.Explanation != nil {
				payload["explanation"] = *m.Explanation
			}
		}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return itemType
	}
	return string(encoded)
}

func t3RawOrEmpty(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return raw
}

func t3ToolItemSummary(itemType string, raw json.RawMessage) string {
	switch itemType {
	case "dynamic_tool":
		var m struct {
			ToolName *string `json:"toolName"`
		}
		if err := json.Unmarshal(raw, &m); err == nil && m.ToolName != nil && *m.ToolName != "" {
			return *m.ToolName
		}
		return "dynamic_tool"
	case "file_search":
		return "file_search"
	case "web_search":
		return "web_search"
	case "todo_list":
		return "todo_list"
	default:
		return itemType
	}
}

func mustDecode(raw json.RawMessage, target any) bool {
	return json.Unmarshal(raw, target) == nil
}

func t3IntOrZero(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

// firstT3Line 取首个非空行并按 rune 截断（不切断多字节字符）。
func firstT3Line(text string) string {
	if idx := strings.IndexByte(text, '\n'); idx >= 0 {
		text = text[:idx]
	}
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if len(runes) > 300 {
		runes = runes[:300]
	}
	return string(runes)
}
