package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/service"
)

// AttachPullRequest 关联 GitHub PR 到 Issue；PR 元信息由服务端拉取，请求体只带 URL。
func (h *IssueHandler) AttachPullRequest(c *gin.Context) {
	issueID := c.Param("iid")
	userID := middleware.GetUserID(c)

	var req service.AttachIssuePullRequestRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		middleware.HandleAppError(c, errs.ErrInvalidParams)
		return
	}

	result, err := h.issueService.AttachIssuePullRequest(issueID, userID, req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}

// DetachPullRequest 解除 Issue 与某个 PR 的关联。
func (h *IssueHandler) DetachPullRequest(c *gin.Context) {
	issueID := c.Param("iid")
	linkID := c.Param("id")
	userID := middleware.GetUserID(c)

	if err := h.issueService.DetachIssuePullRequest(issueID, userID, linkID); err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.SuccessEmpty(c)
}

// SyncPullRequests 重新拉取该 Issue 下全部关联 PR 的最新状态并返回。
func (h *IssueHandler) SyncPullRequests(c *gin.Context) {
	issueID := c.Param("iid")
	userID := middleware.GetUserID(c)

	result, err := h.issueService.SyncIssuePullRequests(issueID, userID)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}

	response.Success(c, result)
}
