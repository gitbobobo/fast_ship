package model

// IssueWorkflowBucket 是 workflow_status 的分桶归类（INT-239）：把「未设置/
// 待办/进行中/已完成」的归并规则收在 model 层供服务层 switch 使用；SQL 侧的
// 分桶谓词唯一出处见 repository/workflow_census.go，两者口径必须一致
// （空串与缺省同为未设置）。未识别取值独立成 Unknown，不冒充 Unset。
type IssueWorkflowBucket string

const (
	IssueWorkflowBucketUnset      IssueWorkflowBucket = "unset"
	IssueWorkflowBucketTodo       IssueWorkflowBucket = "todo"
	IssueWorkflowBucketInProgress IssueWorkflowBucket = "in_progress"
	IssueWorkflowBucketDone       IssueWorkflowBucket = "done"
	IssueWorkflowBucketUnknown    IssueWorkflowBucket = "unknown"
)

// IssueWorkflowBucketOf 把 workflow_status 归入四桶：空串为 Unset，三个合法
// 取值各归本桶，其余未识别取值返回 Unknown——调用方据此跳过状态副作用，
// 而不是当成未设置重置时间戳。
func IssueWorkflowBucketOf(status IssueWorkflowStatus) IssueWorkflowBucket {
	switch status {
	case "":
		return IssueWorkflowBucketUnset
	case IssueWorkflowStatusTodo:
		return IssueWorkflowBucketTodo
	case IssueWorkflowStatusInProgress:
		return IssueWorkflowBucketInProgress
	case IssueWorkflowStatusDone:
		return IssueWorkflowBucketDone
	default:
		return IssueWorkflowBucketUnknown
	}
}
