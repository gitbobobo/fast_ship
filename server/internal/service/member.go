package service

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/selflog"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// 邀请有效期的缺省与边界（小时）：缺省 3 天，上限 30 天。
const (
	inviteExpiresHoursDefault = 72
	inviteExpiresHoursMin     = 1
	inviteExpiresHoursMax     = 720
)

// MemberService 成员与邀请管理（INT-129，管理员面）：成员列表、改角色、
// 邀请的创建/列表/撤销与注册前校验。邀请明文只在 CreateInvite 返回值里出现
// 一次，库里只存 SHA-256。
type MemberService struct {
	userRepo   *repository.UserRepository
	inviteRepo *repository.InviteRepository
	logger     *slog.Logger
}

func NewMemberService(userRepo *repository.UserRepository, inviteRepo *repository.InviteRepository, logger *slog.Logger) *MemberService {
	if logger == nil {
		logger = slog.Default()
	}
	return &MemberService{userRepo: userRepo, inviteRepo: inviteRepo, logger: logger}
}

func (s *MemberService) log(ctx context.Context) *slog.Logger {
	l := selflog.LoggerOr(ctx, s.logger)
	if selflog.LoggerFromContext(ctx) != nil {
		return l.With(slog.String("module", "member"))
	}
	return l
}

// List 全部成员，created_at 升序（首个用户=最早的 admin 排最前）。
func (s *MemberService) List(ctx context.Context) ([]MemberResponse, error) {
	users, err := s.userRepo.ListAll()
	if err != nil {
		return nil, errs.ErrInternal
	}
	out := make([]MemberResponse, 0, len(users))
	for i := range users {
		out = append(out, newUserResponse(&users[i]))
	}
	return out, nil
}

// UpdateRole 改成员角色。目标不存在 40400；role 取值非法 40001；把最后一个
// admin 降级 40931——判定在事务内（count(role='admin')=1 且目标当前是 admin），
// 读-改-写整体提交或回滚。
func (s *MemberService) UpdateRole(ctx context.Context, targetUserID string, req *UpdateMemberRoleRequest) (*MemberResponse, error) {
	role := string(req.Role)
	if role != model.UserRoleAdmin && role != model.UserRoleMember {
		return nil, errs.New(errs.ErrInvalidParams.Code, "role 只能是 admin 或 member")
	}

	err := s.userRepo.Transaction(func(tx *gorm.DB) error {
		target, err := s.userRepo.FindByIDTx(tx, targetUserID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.ErrUserNotFound
			}
			return err
		}
		if target.Role == model.UserRoleAdmin && role == model.UserRoleMember {
			admins, err := s.userRepo.CountAdminsTx(tx)
			if err != nil {
				return err
			}
			if admins <= 1 {
				return errs.ErrLastAdmin
			}
		}
		return s.userRepo.UpdateRoleTx(tx, targetUserID, role)
	})
	if err != nil {
		var appErr *errs.AppError
		if errors.As(err, &appErr) {
			return nil, appErr
		}
		return nil, errs.ErrInternal
	}

	target, err := s.userRepo.FindByID(targetUserID)
	if err != nil {
		return nil, errs.ErrInternal
	}
	s.log(ctx).Info("修改成员角色", slog.String("target_user_id", targetUserID), slog.String("role", role))
	resp := newUserResponse(target)
	return &resp, nil
}

// ListInvites 全部邀请，created_at 倒序；不含 token 明文。
func (s *MemberService) ListInvites(ctx context.Context) ([]InviteResponse, error) {
	invites, err := s.inviteRepo.List()
	if err != nil {
		return nil, errs.ErrInternal
	}
	out := make([]InviteResponse, 0, len(invites))
	for i := range invites {
		out = append(out, newInviteResponse(&invites[i]))
	}
	return out, nil
}

// CreateInvite 签发一次性邀请：明文 token（GenerateApiKeyRaw 随机串）只随本次
// 响应返回一次，库里只存 SHA-256；url 由前端用 public_url 或 origin 拼接，
// 服务端不回 url。expires_in_hours 省略默认 72，允许 1..720。
func (s *MemberService) CreateInvite(ctx context.Context, adminUserID string, req *CreateInviteRequest) (*CreatedInviteResponse, error) {
	hours := inviteExpiresHoursDefault
	if req.ExpiresInHours != nil {
		hours = *req.ExpiresInHours
	}
	if hours < inviteExpiresHoursMin || hours > inviteExpiresHoursMax {
		return nil, errs.New(errs.ErrInvalidParams.Code,
			"expires_in_hours 必须在 1..720 之间（省略默认 72）")
	}

	raw, err := GenerateApiKeyRaw()
	if err != nil {
		return nil, errs.ErrInternal
	}
	now := time.Now()
	invite := &model.Invite{
		ID:        uuid.NewString(),
		TokenHash: HashApiKey(raw),
		CreatedBy: adminUserID,
		ExpiresAt: now.Add(time.Duration(hours) * time.Hour),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.inviteRepo.Create(invite); err != nil {
		return nil, errs.ErrInternal
	}

	s.log(ctx).Info("创建注册邀请", slog.String("invite_id", invite.ID), slog.String("created_by", adminUserID))
	out := newCreatedInviteResponse(invite, raw)
	return &out, nil
}

// RevokeInvite 撤销邀请（置 revoked_at）。幂等：已撤销仍成功；不存在 40432。
func (s *MemberService) RevokeInvite(ctx context.Context, id string) error {
	if _, err := s.inviteRepo.FindByID(id); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrInviteNotFound
		}
		return errs.ErrInternal
	}
	if err := s.inviteRepo.Revoke(id, time.Now()); err != nil {
		return errs.ErrInternal
	}
	s.log(ctx).Info("撤销注册邀请", slog.String("invite_id", id))
	return nil
}

// ValidateInvite 注册前校验：有效返回过期时间；不存在/已过期/已撤销/已使用
// 统一 40432，不区分原因（注册页只显示「无效或已过期」）。
func (s *MemberService) ValidateInvite(ctx context.Context, token string) (*api.ValidateInvite200JSONResponseBody_Data, error) {
	invite, err := s.inviteRepo.FindByTokenHash(HashApiKey(token))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrInviteNotFound
		}
		return nil, errs.ErrInternal
	}
	if !inviteUsable(invite, time.Now()) {
		return nil, errs.ErrInviteNotFound
	}
	expires := api.JSONTime(invite.ExpiresAt)
	return &api.ValidateInvite200JSONResponseBody_Data{ExpiresAt: expires}, nil
}

// inviteUsable 邀请当前是否可用于注册：未撤销、未使用、未过期。
func inviteUsable(invite *model.Invite, now time.Time) bool {
	return invite.RevokedAt == nil &&
		invite.UsedAt == nil &&
		now.Before(invite.ExpiresAt)
}

// newUserResponse 用户行的 API 投影（Member 与 User 同形；角色取库内现值）。
// auth.go 的 toUserResponse 也委托这里，保持单一事实源。
func newUserResponse(user *model.User) MemberResponse {
	role := user.Role
	if role == "" {
		// 极旧中间形态的 NULL 角色按 member 投影。
		role = model.UserRoleMember
	}
	return MemberResponse{
		Id:        user.ID,
		Username:  user.Username,
		Email:     user.Email,
		AvatarUrl: user.AvatarURL,
		Role:      api.UserRole(role),
		Timezone:  user.Timezone,
		CreatedAt: api.JSONTime(user.CreatedAt),
		UpdatedAt: api.JSONTime(user.UpdatedAt),
	}
}

func newInviteResponse(invite *model.Invite) InviteResponse {
	return InviteResponse{
		Id:        invite.ID,
		CreatedBy: invite.CreatedBy,
		ExpiresAt: api.JSONTime(invite.ExpiresAt),
		RevokedAt: api.JSONTimePtr(invite.RevokedAt),
		UsedAt:    api.JSONTimePtr(invite.UsedAt),
		UsedBy:    textPtr(invite.UsedBy),
		CreatedAt: api.JSONTime(invite.CreatedAt),
	}
}

func newCreatedInviteResponse(invite *model.Invite, token string) CreatedInviteResponse {
	base := newInviteResponse(invite)
	return CreatedInviteResponse{
		CreatedAt: base.CreatedAt,
		CreatedBy: base.CreatedBy,
		ExpiresAt: base.ExpiresAt,
		Id:        base.Id,
		RevokedAt: base.RevokedAt,
		UsedAt:    base.UsedAt,
		UsedBy:    base.UsedBy,
		Token:     token,
	}
}

// textPtr 空串折成 nil（未使用者投影为 null）。
func textPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
