package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/response"
	"github.com/godbobo/fast_ship/server/internal/service"
)

// MemberHandler 成员与邀请管理（INT-129）：列表/改角色/邀请 CRUD 仅管理员
// （路由层 RequireAdmin），邀请注册前校验公开。响应统一信封。
type MemberHandler struct {
	svc *service.MemberService
}

// 列表 data 形状（与契约一致：只有 items，无分页元数据）。
type memberListData struct {
	Items []api.Member `json:"items"`
}

type inviteListData struct {
	Items []api.Invite `json:"items"`
}

func NewMemberHandler(svc *service.MemberService) *MemberHandler {
	return &MemberHandler{svc: svc}
}

// List GET /api/members
func (h *MemberHandler) List(c *gin.Context) {
	items, err := h.svc.List(c.Request.Context())
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, memberListData{Items: items})
}

// UpdateRole PUT /api/members/:uid/role
func (h *MemberHandler) UpdateRole(c *gin.Context) {
	var req api.UpdateMemberRoleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}
	out, err := h.svc.UpdateRole(c.Request.Context(), c.Param("uid"), &req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, out)
}

// ListInvites GET /api/invites
func (h *MemberHandler) ListInvites(c *gin.Context) {
	items, err := h.svc.ListInvites(c.Request.Context())
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, inviteListData{Items: items})
}

// CreateInvite POST /api/invites
func (h *MemberHandler) CreateInvite(c *gin.Context) {
	var req api.CreateInviteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}
	out, err := h.svc.CreateInvite(c.Request.Context(), middleware.GetUserID(c), &req)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, out)
}

// RevokeInvite DELETE /api/invites/:id
func (h *MemberHandler) RevokeInvite(c *gin.Context) {
	if err := h.svc.RevokeInvite(c.Request.Context(), c.Param("id")); err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.SuccessEmpty(c)
}

// ValidateInvite POST /api/auth/invites/validate（公开，注册页提前显示状态）。
// token 走请求体：selflog 记录请求路径，明文 token 一旦进 URL 就能被读回复用。
func (h *MemberHandler) ValidateInvite(c *gin.Context) {
	var req api.ValidateInviteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, 40001, "请求参数无效: "+err.Error())
		return
	}
	if req.Token == "" {
		middleware.HandleAppError(c, errs.ErrInviteNotFound)
		return
	}
	out, err := h.svc.ValidateInvite(c.Request.Context(), req.Token)
	if err != nil {
		middleware.HandleAppError(c, err)
		return
	}
	response.Success(c, out)
}
