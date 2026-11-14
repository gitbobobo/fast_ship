package service

import (
	"strings"
	"testing"
)

// MCP 工具层单测（INT-222）：scope 判定（read 可读、write 隐含 read、越权
// 拒绝）与工具入参构造校验。端到端见 router/mcp_rest_test.go。

func mcpTestScopes(projectID string, write bool) string {
	return BuildOAuthScopes([]string{projectID}, write)
}

// read scope：读工具通过、写工具拒绝；write scope：读写都通过（write 隐含 read）。
func TestMCPCheckScope(t *testing.T) {
	const pid = "0f7c3a10-1111-4222-8333-444455556666"

	readOnly := mcpTestScopes(pid, false)
	if err := mcpCheckScope(readOnly, pid, false); err != nil {
		t.Fatalf("read scope 应可读: %v", err)
	}
	if err := mcpCheckScope(readOnly, pid, true); err == nil {
		t.Fatalf("read scope 调写工具应被拒")
	}

	readWrite := mcpTestScopes(pid, true)
	if err := mcpCheckScope(readWrite, pid, false); err != nil {
		t.Fatalf("write scope 应隐含 read: %v", err)
	}
	if err := mcpCheckScope(readWrite, pid, true); err != nil {
		t.Fatalf("write scope 应可写: %v", err)
	}

	// 越权：scope 只覆盖其他项目。
	if err := mcpCheckScope(readWrite, "aaaa3a10-1111-4222-8333-444455556666", false); err == nil {
		t.Fatalf("scope 外项目应拒绝")
	}
	// 空 scope 一律拒绝。
	if err := mcpCheckScope("", pid, false); err == nil {
		t.Fatalf("空 scope 应拒绝读")
	}

	// 文案不含令牌前缀，不泄凭证形态。
	err := mcpCheckScope(readOnly, pid, true)
	if strings.Contains(err.Error(), "fsm_") || strings.Contains(err.Error(), "Bearer") {
		t.Fatalf("scope 错误文案不应含令牌形态: %v", err)
	}
}

// list 入参：page/page_size 缺省与夹取。
func TestMCPListParams(t *testing.T) {
	filters, page, pageSize := mcpListParams(mcpListIssuesIn{ProjectID: "p"})
	if page != 1 || pageSize != 20 {
		t.Fatalf("缺省分页应为 1/20: %d/%d", page, pageSize)
	}
	if filters.State != "" || filters.Query != "" || filters.Label != "" {
		t.Fatalf("缺省过滤器应为空: %+v", filters)
	}

	filters, page, pageSize = mcpListParams(mcpListIssuesIn{
		State: " open ", Query: "登录", Label: "bug", Page: -3, PageSize: 500,
	})
	if filters.State != "open" {
		t.Fatalf("state 应去空白: %q", filters.State)
	}
	if filters.Query != "登录" || filters.Label != "bug" {
		t.Fatalf("过滤器透传错误: %+v", filters)
	}
	if page != 1 || pageSize != 100 {
		t.Fatalf("越界分页应夹取: %d/%d", page, pageSize)
	}
}

// update 入参：全空拒绝；字段映射正确。
func TestMCPUpdateRequest(t *testing.T) {
	if _, err := mcpUpdateRequest(mcpUpdateIssueIn{IssueID: "i"}); err == nil {
		t.Fatalf("全空更新应拒绝")
	}

	req, err := mcpUpdateRequest(mcpUpdateIssueIn{IssueID: "i", Title: " 新标题 ", Body: "正文", Labels: []string{"a", "b"}})
	if err != nil {
		t.Fatalf("合法更新应通过: %v", err)
	}
	if req.Title == nil || *req.Title != "新标题" {
		t.Fatalf("title 应 trim: %v", req.Title)
	}
	if req.Body == nil || *req.Body != "正文" {
		t.Fatalf("body 映射错误: %v", req.Body)
	}
	if len(req.Labels) != 2 {
		t.Fatalf("labels 映射错误: %v", req.Labels)
	}
}

// close 入参：reason 只接受 completed/not_planned；reopen 固定 open。
func TestMCPCloseAndReopenRequest(t *testing.T) {
	if _, err := mcpCloseRequest(mcpCloseIssueIn{IssueID: "i", Reason: "wontfix"}); err == nil {
		t.Fatalf("非法 reason 应拒绝")
	}

	req, err := mcpCloseRequest(mcpCloseIssueIn{IssueID: "i", Reason: "not_planned"})
	if err != nil {
		t.Fatalf("not_planned 应通过: %v", err)
	}
	if req.State == nil || *req.State != "closed" {
		t.Fatalf("close 应置 closed: %v", req.State)
	}
	if req.StateReason == nil || *req.StateReason != "not_planned" {
		t.Fatalf("reason 映射错误: %v", req.StateReason)
	}

	req, err = mcpCloseRequest(mcpCloseIssueIn{IssueID: "i"})
	if err != nil {
		t.Fatalf("缺省关闭应通过: %v", err)
	}
	if req.StateReason != nil {
		t.Fatalf("缺省关闭不应设 reason: %v", req.StateReason)
	}

	reopen := mcpReopenRequest()
	if reopen.State == nil || *reopen.State != "open" {
		t.Fatalf("reopen 应置 open: %v", reopen.State)
	}
	if reopen.StateReason != nil {
		t.Fatalf("reopen 不应带 reason（清空语义）: %v", reopen.StateReason)
	}
}
