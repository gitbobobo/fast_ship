package service

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type memberTestServices struct {
	db          *gorm.DB
	cfg         *config.Config
	authService *AuthService
	members     *MemberService
	inviteRepo  *repository.InviteRepository
	userRepo    *repository.UserRepository
}

func setupMemberTestServices(t *testing.T) *memberTestServices {
	t.Helper()

	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.JWTBlacklist{}, &model.RefreshToken{}, &model.Invite{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}

	cfg := &config.Config{
		JWT: config.JWTConfig{Secret: "test-secret", ExpireHours: 24, RefreshExpireHours: 72},
	}

	userRepo := repository.NewUserRepository(db)
	inviteRepo := repository.NewInviteRepository(db)
	authService := NewAuthService(userRepo, repository.NewJWTBlacklistRepository(db), repository.NewRefreshTokenRepository(db), cfg, nil)
	authService.SetInviteRepository(inviteRepo)

	return &memberTestServices{
		db:          db,
		cfg:         cfg,
		authService: authService,
		members:     NewMemberService(userRepo, inviteRepo, nil),
		inviteRepo:  inviteRepo,
		userRepo:    userRepo,
	}
}

// createAdmin 直接仓储建一个 admin（跳过注册路径，测试前置数据用）。
func (s *memberTestServices) createAdmin(t *testing.T, id string) *model.User {
	t.Helper()
	now := time.Now()
	user := &model.User{
		ID:           id,
		Username:     "admin_" + id,
		Email:        id + "@example.com",
		PasswordHash: "hashed",
		Role:         model.UserRoleAdmin,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.db.Create(user).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}
	return user
}

func mustCreateInvite(t *testing.T, s *memberTestServices, adminID string, hours int) CreatedInviteResponse {
	t.Helper()
	out, err := s.members.CreateInvite(context.Background(), adminID, &CreateInviteRequest{ExpiresInHours: &hours})
	if err != nil {
		t.Fatalf("create invite: %v", err)
	}
	return *out
}

// 库里没有用户时注册：首个用户成为 admin，invite_token 即使携带也被忽略。
func TestRegisterFirstUserBecomesAdmin(t *testing.T) {
	s := setupMemberTestServices(t)
	ctx := context.Background()

	token := "ignored-anyway-" + uuid.NewString()
	reg, err := s.authService.Register(ctx, &RegisterRequest{
		Username:    "first",
		Email:       "first@example.com",
		Password:    "Passw0rd!",
		InviteToken: &token,
	})
	if err != nil {
		t.Fatalf("register first user: %v", err)
	}
	if reg.User.Role != api.UserRoleAdmin {
		t.Fatalf("expected first user role=admin, got %q", reg.User.Role)
	}

	stored, err := s.userRepo.FindByID(reg.User.Id)
	if err != nil {
		t.Fatalf("load user: %v", err)
	}
	if stored.Role != model.UserRoleAdmin {
		t.Fatalf("expected stored role=admin, got %q", stored.Role)
	}
}

// 已有用户后，不带邀请注册被拒（公开注册关闭）。
func TestRegisterWithoutInviteRejected(t *testing.T) {
	s := setupMemberTestServices(t)
	ctx := context.Background()

	if _, err := s.authService.Register(ctx, &RegisterRequest{
		Username: "first", Email: "first@example.com", Password: "Passw0rd!",
	}); err != nil {
		t.Fatalf("register first user: %v", err)
	}

	_, err := s.authService.Register(ctx, &RegisterRequest{
		Username: "second", Email: "second@example.com", Password: "Passw0rd!",
	})
	if err != errs.ErrRegistrationClosed {
		t.Fatalf("expected ErrRegistrationClosed, got %v", err)
	}

	// 未注入邀请仓储时行为等同「有邀请也不认」：裸 AuthService 只走同一库。
	s2 := setupMemberTestServices(t)
	bareAuth := NewAuthService(s2.userRepo,
		repository.NewJWTBlacklistRepository(s2.db),
		repository.NewRefreshTokenRepository(s2.db), s2.cfg, nil)
	if _, err := bareAuth.Register(ctx, &RegisterRequest{
		Username: "bare-first", Email: "bare-first@example.com", Password: "Passw0rd!",
	}); err != nil {
		t.Fatalf("register first user: %v", err)
	}
	admin := s2.createAdmin(t, "admin-no-inject")
	invite := mustCreateInvite(t, s2, admin.ID, 1)
	_, err = bareAuth.Register(ctx, &RegisterRequest{
		Username: "bare-second", Email: "bare-second@example.com", Password: "Passw0rd!", InviteToken: &invite.Token,
	})
	if err != errs.ErrRegistrationClosed {
		t.Fatalf("expected ErrRegistrationClosed without injected invite repo, got %v", err)
	}
}

// 凭邀请注册：新用户是 member，邀请一次性消费；重复使用被拒。
func TestRegisterWithInviteConsumesOnce(t *testing.T) {
	s := setupMemberTestServices(t)
	ctx := context.Background()

	admin := s.createAdmin(t, "admin-1")
	invite := mustCreateInvite(t, s, admin.ID, 72)

	reg, err := s.authService.Register(ctx, &RegisterRequest{
		Username: "invited", Email: "invited@example.com", Password: "Passw0rd!", InviteToken: &invite.Token,
	})
	if err != nil {
		t.Fatalf("register with invite: %v", err)
	}
	if reg.User.Role != api.UserRoleMember {
		t.Fatalf("expected invited user role=member, got %q", reg.User.Role)
	}

	stored, err := s.inviteRepo.FindByID(invite.Id)
	if err != nil {
		t.Fatalf("load invite: %v", err)
	}
	if stored.UsedAt == nil || stored.UsedBy != reg.User.Id {
		t.Fatalf("expected invite consumed by %s, got used_at=%v used_by=%q", reg.User.Id, stored.UsedAt, stored.UsedBy)
	}

	// 同一邀请再注册：已被使用，40932。
	_, err = s.authService.Register(ctx, &RegisterRequest{
		Username: "again", Email: "again@example.com", Password: "Passw0rd!", InviteToken: &invite.Token,
	})
	if err != errs.ErrInviteUsed {
		t.Fatalf("expected ErrInviteUsed on reuse, got %v", err)
	}
}

// 过期与撤销的邀请都不能注册，且与不存在一样统一 40432。
func TestRegisterInviteExpiredOrRevoked(t *testing.T) {
	s := setupMemberTestServices(t)
	ctx := context.Background()
	admin := s.createAdmin(t, "admin-1")

	revoked := mustCreateInvite(t, s, admin.ID, 1)
	if err := s.members.RevokeInvite(ctx, revoked.Id); err != nil {
		t.Fatalf("revoke invite: %v", err)
	}

	expired := mustCreateInvite(t, s, admin.ID, 1)
	if err := s.db.Model(&model.Invite{}).Where("id = ?", expired.Id).
		Update("expires_at", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatalf("expire invite: %v", err)
	}

	for name, invite := range map[string]CreatedInviteResponse{"revoked": revoked, "expired": expired} {
		_, err := s.authService.Register(ctx, &RegisterRequest{
			Username: "u-" + name, Email: name + "@example.com", Password: "Passw0rd!", InviteToken: &invite.Token,
		})
		if err != errs.ErrInviteNotFound {
			t.Fatalf("%s: expected ErrInviteNotFound, got %v", name, err)
		}
	}

	// 撤销幂等：再撤一次仍成功。
	if err := s.members.RevokeInvite(ctx, revoked.Id); err != nil {
		t.Fatalf("revoke twice should stay success: %v", err)
	}
	// 不存在的邀请 ID 撤销报 40432。
	if err := s.members.RevokeInvite(ctx, "no-such-id"); err != errs.ErrInviteNotFound {
		t.Fatalf("expected ErrInviteNotFound for unknown invite id, got %v", err)
	}
}

// ValidateInvite：有效回过期时间；无效/过期/撤销/已用统一 40432。
func TestValidateInvite(t *testing.T) {
	s := setupMemberTestServices(t)
	ctx := context.Background()
	admin := s.createAdmin(t, "admin-1")

	invite := mustCreateInvite(t, s, admin.ID, 48)
	out, err := s.members.ValidateInvite(ctx, invite.Token)
	if err != nil {
		t.Fatalf("validate invite: %v", err)
	}
	if out.ExpiresAt == "" {
		t.Fatalf("expected expires_at in validation response")
	}

	if _, err := s.members.ValidateInvite(ctx, "not-a-real-token"); err != errs.ErrInviteNotFound {
		t.Fatalf("expected ErrInviteNotFound for unknown token, got %v", err)
	}

	if err := s.members.RevokeInvite(ctx, invite.Id); err != nil {
		t.Fatalf("revoke invite: %v", err)
	}
	if _, err := s.members.ValidateInvite(ctx, invite.Token); err != errs.ErrInviteNotFound {
		t.Fatalf("expected ErrInviteNotFound for revoked token, got %v", err)
	}
}

// 改角色：最后一个 admin 不能降级（事务内判定）；目标不存在 40400；非法角色 40001。
func TestUpdateRoleGuards(t *testing.T) {
	s := setupMemberTestServices(t)
	ctx := context.Background()
	admin := s.createAdmin(t, "admin-1")

	// 唯一 admin 降级自己 → 40931。
	if _, err := s.members.UpdateRole(ctx, admin.ID, &UpdateMemberRoleRequest{Role: api.UserRoleMember}); err != errs.ErrLastAdmin {
		t.Fatalf("expected ErrLastAdmin, got %v", err)
	}

	// 两个 admin 时可以降级其中一个。
	second := s.createAdmin(t, "admin-2")
	out, err := s.members.UpdateRole(ctx, second.ID, &UpdateMemberRoleRequest{Role: api.UserRoleMember})
	if err != nil {
		t.Fatalf("demote second admin: %v", err)
	}
	if out.Role != api.UserRoleMember {
		t.Fatalf("expected role=member after demotion, got %q", out.Role)
	}

	// 目标不存在 → 40400；非法取值 → 40001。
	if _, err := s.members.UpdateRole(ctx, "no-such-user", &UpdateMemberRoleRequest{Role: api.UserRoleMember}); err != errs.ErrUserNotFound {
		t.Fatalf("expected ErrUserNotFound, got %v", err)
	}
	if _, err := s.members.UpdateRole(ctx, admin.ID, &UpdateMemberRoleRequest{Role: api.UserRole("owner")}); err == nil {
		t.Fatalf("expected invalid role rejected")
	} else if appErr, ok := err.(*errs.AppError); !ok || appErr.Code != errs.ErrInvalidParams.Code {
		t.Fatalf("expected 40001 for invalid role, got %v", err)
	}
}

// 成员列表按 created_at 升序，含 role。
func TestMemberListOrderedByCreatedAt(t *testing.T) {
	s := setupMemberTestServices(t)
	ctx := context.Background()

	base := time.Now().Add(-time.Hour)
	mk := func(id string, at time.Time, role string) {
		t.Helper()
		if err := s.db.Create(&model.User{
			ID: id, Username: "u_" + id, Email: id + "@example.com", PasswordHash: "x",
			Role: role, CreatedAt: at, UpdatedAt: at,
		}).Error; err != nil {
			t.Fatalf("create user %s: %v", id, err)
		}
	}
	mk("m-late", base.Add(2*time.Minute), model.UserRoleMember)
	mk("m-early", base, model.UserRoleAdmin)
	mk("m-mid", base.Add(time.Minute), model.UserRoleMember)

	items, err := s.members.List(ctx)
	if err != nil {
		t.Fatalf("list members: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 members, got %d", len(items))
	}
	if items[0].Id != "m-early" || items[1].Id != "m-mid" || items[2].Id != "m-late" {
		t.Fatalf("unexpected order: %s, %s, %s", items[0].Id, items[1].Id, items[2].Id)
	}
	if items[0].Role != api.UserRoleAdmin || items[1].Role != api.UserRoleMember {
		t.Fatalf("expected roles projected, got %q/%q", items[0].Role, items[1].Role)
	}
}

// 邀请列表倒序且不回 token 明文。
func TestInviteListExcludesToken(t *testing.T) {
	s := setupMemberTestServices(t)
	ctx := context.Background()
	admin := s.createAdmin(t, "admin-1")

	first := mustCreateInvite(t, s, admin.ID, 1)
	second := mustCreateInvite(t, s, admin.ID, 2)

	items, err := s.members.ListInvites(ctx)
	if err != nil {
		t.Fatalf("list invites: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 invites, got %d", len(items))
	}
	// created_at 倒序：后建的排前面。
	if items[0].Id != second.Id || items[1].Id != first.Id {
		t.Fatalf("expected newest first, got %s then %s", items[0].Id, items[1].Id)
	}
}

// expires_in_hours 边界：省略默认 72，1..720 之外 40001。
func TestCreateInviteExpiresHoursValidation(t *testing.T) {
	s := setupMemberTestServices(t)
	ctx := context.Background()
	admin := s.createAdmin(t, "admin-1")

	// 省略 → 默认 72 小时。
	out, err := s.members.CreateInvite(ctx, admin.ID, &CreateInviteRequest{})
	if err != nil {
		t.Fatalf("create invite default: %v", err)
	}
	expires, err := time.Parse(time.RFC3339Nano, out.ExpiresAt)
	if err != nil {
		t.Fatalf("parse expires_at: %v", err)
	}
	if diff := time.Until(expires); diff < 71*time.Hour || diff > 73*time.Hour {
		t.Fatalf("expected ~72h validity, got %v", diff)
	}

	for _, hours := range []int{0, -1, 721} {
		if _, err := s.members.CreateInvite(ctx, admin.ID, &CreateInviteRequest{ExpiresInHours: &hours}); err == nil {
			t.Fatalf("expected 40001 for expires_in_hours=%d", hours)
		} else if appErr, ok := err.(*errs.AppError); !ok || appErr.Code != errs.ErrInvalidParams.Code {
			t.Fatalf("expected 40001 for expires_in_hours=%d, got %v", hours, err)
		}
	}
}

// ConsumeTx 的谓词带 expires_at：调用方读到「没过期」到真正写入之间过去的时间
// 也在生效（否则会出现过期后仍被消费的注册）。
func TestInviteConsumeRejectsExpiredAtCommitTime(t *testing.T) {
	s := setupMemberTestServices(t)
	admin := s.createAdmin(t, "admin-1")
	invite := mustCreateInvite(t, s, admin.ID, 1)

	// 写时点已经超过有效期（读到与写入之间过期）：谓词必须不命中。
	consumed, err := s.inviteRepo.ConsumeTx(s.db, invite.Id, "u-1", time.Now().Add(2*time.Hour))
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if consumed {
		t.Fatal("expired invite must not be consumed at commit time")
	}
}
