package service

import "github.com/godbobo/fast_ship/server/internal/api"

// T3 线程只读集成（INT-82）的契约类型。
//
// 契约真相源是 api/types.gen.go（oapi-codegen 自 api/openapi.yaml 生成），
// 这里按 api_types.go 惯例为 service 层既有名字提供类型别名；字段名以生成
// 类型为准（Id/EnvironmentId/PairingUrl 风格），optional 的 error/status/
// child_thread_id/detail_available 为指针（api.Ptr/Deref/NonEmpty/True 处理）。
type (
	// 环境连接
	T3EnvironmentResponse      = api.T3Environment
	CreateT3EnvironmentRequest = api.CreateT3EnvironmentRequest
	UpdateT3EnvironmentRequest = api.UpdateT3EnvironmentRequest

	// 线程聚合（列表 Data 形状：items + environments）
	T3ThreadListResponse = api.ListT3Threads200JSONResponseBody_Data
	T3ThreadSummary      = api.T3ThreadSummary
	T3ThreadItem         = api.T3ThreadItem
	T3ThreadDetail       = api.T3ThreadDetail

	// T3 客户端操作（INT-85）
	T3ComposerOptions        = api.T3ComposerOptions
	T3WorkspaceOptions       = api.T3WorkspaceOptions
	T3CreateThreadRequest    = api.CreateT3ThreadRequest
	T3ThreadLaunchResult     = api.T3ThreadLaunchResult
	T3SendMessageRequest     = api.SendT3ThreadMessageRequest
	T3DispatchResult         = api.T3DispatchResult
	T3ThreadStopResult       = api.T3ThreadStopResult
	T3EditQueuedRunRequest   = api.EditT3QueuedRunRequest
	T3ReorderQueuedRunRequst = api.ReorderT3QueuedRunRequest
	T3RespondRequest         = api.RespondT3ThreadRequest
	T3AnswerSecretRequest    = api.AnswerT3SecretRequest
	T3ChatAttachment         = api.T3ChatAttachment
	T3ThreadQueue            = api.T3ThreadQueue
	T3ThreadQueueItem        = api.T3ThreadQueueItem
	T3ThreadActivity         = api.T3ThreadActivity

	// 枚举
	T3EnvironmentStatus   = api.T3EnvironmentStatus
	T3EnvironmentAccess   = api.T3EnvironmentAccess
	T3ThreadItemType      = api.T3ThreadItemType
	T3ThreadSummaryStatus = api.T3ThreadSummaryStatus
)

// 枚举值沿用生成常量；service 侧保持既有短名，避免引用点散落 api 包名。
const (
	T3EnvironmentStatusOK          = api.T3EnvironmentStatusOk
	T3EnvironmentStatusUnreachable = api.T3EnvironmentStatusUnreachable
	T3EnvironmentStatusRepair      = api.T3EnvironmentStatusRepair
	T3EnvironmentStatusDegraded    = api.T3EnvironmentStatusDegraded

	T3EnvironmentAccessReadOnly = api.ReadOnly
	T3EnvironmentAccessOperate  = api.Operate

	T3ThreadItemTypeUserMessage      = api.UserMessage
	T3ThreadItemTypeAssistantMessage = api.AssistantMessage
	T3ThreadItemTypeReasoning        = api.Reasoning
	T3ThreadItemTypeCommandExecution = api.CommandExecution
	T3ThreadItemTypeDynamicTool      = api.DynamicTool
	T3ThreadItemTypeFileChange       = api.FileChange
	T3ThreadItemTypeSubagent         = api.Subagent
	T3ThreadTypeError                = api.Error
	T3ThreadItemTypeApprovalRequest  = api.ApprovalRequest
	T3ThreadItemTypeUserInputRequest = api.UserInputRequest
	T3ThreadItemTypeSecretRequest    = api.SecretRequest
)
