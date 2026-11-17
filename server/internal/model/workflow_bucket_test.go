package model

import "testing"

func TestIssueWorkflowBucketOf(t *testing.T) {
	tests := []struct {
		name   string
		status IssueWorkflowStatus
		want   IssueWorkflowBucket
	}{
		{"空串归未设置", "", IssueWorkflowBucketUnset},
		{"todo 归待办", IssueWorkflowStatusTodo, IssueWorkflowBucketTodo},
		{"in_progress 归进行中", IssueWorkflowStatusInProgress, IssueWorkflowBucketInProgress},
		{"done 归已完成", IssueWorkflowStatusDone, IssueWorkflowBucketDone},
		{"未识别取值独立成桶，不冒充未设置", "bogus", IssueWorkflowBucketUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IssueWorkflowBucketOf(tt.status); got != tt.want {
				t.Fatalf("IssueWorkflowBucketOf(%q) = %q, want %q", tt.status, got, tt.want)
			}
		})
	}
}
