package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/selflog"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type AuthService struct {
	userRepo         *repository.UserRepository
	jwtBlacklistRepo *repository.JWTBlacklistRepository
	refreshTokenRepo *repository.RefreshTokenRepository
	cfg              *config.Config
	logger           *slog.Logger
	// invites 可选依赖（INT-129）：注册时消费邀请用。不注入时注册只认「库里
	// 还没有用户」的首个用户，之后一律拒绝（等同没有邀请可用）；仅 main 注入，
	// 测试可自行注入，避免改动既有构造调用。
	invites *repository.InviteRepository
}

func NewAuthService(userRepo *repository.UserRepository, jwtBlacklistRepo *repository.JWTBlacklistRepository, refreshTokenRepo *repository.RefreshTokenRepository, cfg *config.Config, logger *slog.Logger) *AuthService {
	if logger == nil {
		logger = slog.Default()
	}
	return &AuthService{
		userRepo:         userRepo,
		jwtBlacklistRepo: jwtBlacklistRepo,
		refreshTokenRepo: refreshTokenRepo,
		cfg:              cfg,
		logger:           logger,
	}
}

// SetInviteRepository 注入邀请仓储（INT-129，main 在建表后调用一次）。
func (s *AuthService) SetInviteRepository(repo *repository.InviteRepository) {
	s.invites = repo
}

func (s *AuthService) log(ctx context.Context) *slog.Logger {
	l := selflog.LoggerOr(ctx, s.logger)
	if selflog.LoggerFromContext(ctx) != nil {
		return l.With(slog.String("module", "auth"))
	}
	return l
}

// Register 注册新用户（INT-129 语义）：
//   - 库里还没有用户：创建首个用户并授予 admin，invite_token 即使携带也忽略；
//   - 已有用户：必须携带有效邀请（未使用、未撤销、未过期），新用户固定 member，
//     邀请在同一事务内置为已使用（条件更新未命中即整体失败，防并发重复使用）。
//
// 用户名/邮箱冲突仍在事务外先查给 40900/40901；角色判定与邀请消费在事务内完成。
func (s *AuthService) Register(ctx context.Context, req *RegisterRequest) (*AuthResponse, error) {
	exists, err := s.userRepo.ExistsByUsername(req.Username)
	if err != nil {
		return nil, errs.ErrInternal
	}
	if exists {
		return nil, errs.ErrUsernameExists
	}

	exists, err = s.userRepo.ExistsByEmail(req.Email)
	if err != nil {
		return nil, errs.ErrInternal
	}
	if exists {
		return nil, errs.ErrEmailExists
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, errs.ErrInternal
	}

	user := &model.User{
		ID:           uuid.New().String(),
		Username:     req.Username,
		Email:        req.Email,
		PasswordHash: string(hashedPassword),
	}

	if err := s.userRepo.Transaction(func(tx *gorm.DB) error {
		if n, err := s.userRepo.CountTx(tx); err != nil {
			return err
		} else if n == 0 {
			// 首个用户 = 管理员，忽略邀请参数。
			user.Role = model.UserRoleAdmin
		} else {
			role, err := s.resolveInviteRole(tx, api.Deref(req.InviteToken), user.ID)
			if err != nil {
				return err
			}
			user.Role = role
		}
		return s.userRepo.CreateTx(tx, user)
	}); err != nil {
		var appErr *errs.AppError
		if errors.As(err, &appErr) {
			return nil, appErr
		}
		return nil, errs.ErrInternal
	}

	token, err := s.generateToken(user)
	if err != nil {
		return nil, errs.ErrInternal
	}

	refreshToken, created, err := s.generateRefreshToken(user)
	if err != nil || !created {
		return nil, errs.ErrInternal
	}

	s.log(ctx).Info("用户注册", slog.String("user_id", user.ID), slog.String("username", user.Username), slog.String("role", user.Role))
	return &AuthResponse{
		Token:        token,
		RefreshToken: refreshToken,
		User:         s.toUserResponse(user),
	}, nil
}

// resolveInviteRole 校验邀请并消费（注册事务内调用），返回新用户应得的角色
// （恒为 member）。无效邀请按状态分型：不存在/过期/撤销统一 40432（不泄漏
// 状态）；已被使用 40932。未携带 token 或未注入邀请仓储时按注册关闭拒绝。
func (s *AuthService) resolveInviteRole(tx *gorm.DB, token, userID string) (string, error) {
	if strings.TrimSpace(token) == "" || s.invites == nil {
		return "", errs.ErrRegistrationClosed
	}
	invite, err := s.invites.FindByTokenHashTx(tx, HashApiKey(token))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", errs.ErrInviteNotFound
		}
		return "", err
	}
	now := time.Now()
	if invite.RevokedAt != nil {
		return "", errs.ErrInviteNotFound
	}
	if invite.UsedAt != nil {
		return "", errs.ErrInviteUsed
	}
	if !now.Before(invite.ExpiresAt) {
		return "", errs.ErrInviteNotFound
	}
	consumed, err := s.invites.ConsumeTx(tx, invite.ID, userID, now)
	if err != nil {
		return "", err
	}
	if !consumed {
		// 读时可用、条件更新未命中：并发注册抢先消费，或者刚好在读到写入之间过期。
		// 重读一次分清两者——过期一律 40432，不把它当成「已使用」。
		fresh, readErr := s.invites.FindByTokenHashTx(tx, HashApiKey(token))
		if readErr == nil && fresh.UsedAt == nil && !time.Now().Before(fresh.ExpiresAt) {
			return "", errs.ErrInviteNotFound
		}
		return "", errs.ErrInviteUsed
	}
	return model.UserRoleMember, nil
}

func (s *AuthService) Login(ctx context.Context, req *LoginRequest) (*AuthResponse, error) {
	user, err := s.userRepo.FindByUsernameOrEmail(req.Login)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// 无归属行不写尝试的登录名，避免把探测用标识公开给所有可查系统日志的用户。
			s.log(ctx).Warn("登录失败：用户不存在")
			return nil, errs.ErrLoginFailed
		}
		return nil, errs.ErrInternal
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		s.log(ctx).Warn("登录失败：密码错误", slog.String("user_id", user.ID))
		return nil, errs.ErrLoginFailed
	}

	token, err := s.generateToken(user)
	if err != nil {
		return nil, errs.ErrInternal
	}

	refreshToken, created, err := s.generateRefreshToken(user)
	if err != nil {
		return nil, errs.ErrInternal
	}
	if !created {
		// 密码校验与签发之间该用户刚改过密码：本次登录依据的旧密码已失效。
		return nil, errs.ErrLoginFailed
	}

	s.log(ctx).Info("登录成功", slog.String("user_id", user.ID))
	return &AuthResponse{
		Token:        token,
		RefreshToken: refreshToken,
		User:         s.toUserResponse(user),
	}, nil
}

func (s *AuthService) Logout(jti string, exp time.Time, refreshToken string) error {
	if err := s.jwtBlacklistRepo.Add(jti, exp); err != nil {
		return err
	}

	if refreshToken != "" {
		raw := strings.TrimPrefix(refreshToken, "fsr_")
		tokenHash := HashApiKey(raw)
		rt, err := s.refreshTokenRepo.FindByHash(tokenHash)
		if err == nil {
			_ = s.refreshTokenRepo.Revoke(rt.ID)
		}
	}

	return nil
}

func (s *AuthService) RefreshAccessToken(refreshToken string) (*RefreshResponse, error) {
	raw := strings.TrimPrefix(refreshToken, "fsr_")
	tokenHash := HashApiKey(raw)

	rt, err := s.refreshTokenRepo.FindByHash(tokenHash)
	if err != nil {
		return nil, errs.ErrRefreshTokenInvalid
	}

	user, err := s.userRepo.FindByID(rt.UserID)
	if err != nil {
		return nil, errs.ErrUserNotFound
	}

	token, err := s.generateToken(user)
	if err != nil {
		return nil, errs.ErrInternal
	}

	newRefreshToken, rotated, err := s.rotateRefreshToken(rt)
	if err != nil {
		return nil, errs.ErrInternal
	}
	if !rotated {
		// 旧 refresh token 在校验后被改密扫描（或登出）吊销——不得再补发。
		return nil, errs.ErrRefreshTokenInvalid
	}

	return &RefreshResponse{
		Token:        token,
		RefreshToken: newRefreshToken,
	}, nil
}

func (s *AuthService) GetMe(userID string) (*UserResponse, error) {
	user, err := s.userRepo.FindByID(userID)
	if err != nil {
		return nil, errs.ErrUserNotFound
	}
	resp := s.toUserResponse(user)
	return &resp, nil
}

func (s *AuthService) UpdateProfile(userID string, req *UpdateProfileRequest) (*UserResponse, error) {
	user, err := s.userRepo.FindByID(userID)
	if err != nil {
		return nil, errs.ErrUserNotFound
	}

	fields := map[string]any{}
	if username := api.Deref(req.Username); username != "" && username != user.Username {
		exists, err := s.userRepo.ExistsByUsername(username)
		if err != nil {
			return nil, errs.ErrInternal
		}
		if exists {
			return nil, errs.ErrUsernameExists
		}
		user.Username = username
		fields["username"] = username
	}

	if email := api.Deref(req.Email); email != "" && email != user.Email {
		exists, err := s.userRepo.ExistsByEmail(email)
		if err != nil {
			return nil, errs.ErrInternal
		}
		if exists {
			return nil, errs.ErrEmailExists
		}
		user.Email = email
		fields["email"] = email
	}

	if len(fields) > 0 {
		if err := s.userRepo.UpdateFields(userID, fields); err != nil {
			return nil, errs.ErrInternal
		}
	}

	resp := s.toUserResponse(user)
	return &resp, nil
}

func (s *AuthService) UpdateAvatar(userID string, avatarURL string) (*UserResponse, error) {
	user, err := s.userRepo.FindByID(userID)
	if err != nil {
		return nil, errs.ErrUserNotFound
	}

	if avatarURL != user.AvatarURL {
		user.AvatarURL = avatarURL
		if err := s.userRepo.UpdateFields(userID, map[string]any{"avatar_url": avatarURL}); err != nil {
			return nil, errs.ErrInternal
		}
	}

	resp := s.toUserResponse(user)
	return &resp, nil
}

// UpdatePassword 改密成功后在同一事务里吊销该用户全部 refresh token 并递增
// token_version，使其他会话的 access token 立即失效；同时返回为当前会话
// 新签发的 token 对，让发起改密的会话继续持有有效凭证。
func (s *AuthService) UpdatePassword(ctx context.Context, userID string, req *UpdatePasswordRequest) (*RefreshResponse, error) {
	user, err := s.userRepo.FindByID(userID)
	if err != nil {
		return nil, errs.ErrUserNotFound
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.OldPassword)); err != nil {
		return nil, errs.ErrLoginFailed
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		return nil, errs.ErrInternal
	}

	raw, next, err := s.buildRefreshToken(userID)
	if err != nil {
		return nil, errs.ErrInternal
	}

	// 版本号在事务内自增并回读：并发改密必须各自推进，否则先后两次改密
	// 停在同一版本，前一次换出的 access token 会错误地保持有效。
	// 把刚通过 bcrypt 校验的旧哈希作为写谓词——授权依据在写时点必须
	// 仍然成立，否则并发改密会拿着已作废的旧密码证明覆盖新密码。
	newVersion, err := s.userRepo.UpdatePasswordAndRotateSessions(userID, user.PasswordHash, string(hashedPassword), next)
	if errors.Is(err, repository.ErrPasswordChanged) {
		return nil, errs.ErrLoginFailed
	}
	if err != nil {
		return nil, errs.ErrInternal
	}

	user.TokenVersion = newVersion
	token, err := s.generateToken(user)
	if err != nil {
		return nil, errs.ErrInternal
	}

	s.log(ctx).Info("用户修改密码", slog.String("user_id", userID))
	return &RefreshResponse{
		Token:        token,
		RefreshToken: fmt.Sprintf("fsr_%s", raw),
	}, nil
}

func (s *AuthService) IsTokenBlacklisted(jti string) (bool, error) {
	return s.jwtBlacklistRepo.Exists(jti)
}

// IsTokenVersionCurrent 校验 JWT 的 ver claim 与用户当前 token_version 一致。
// 无 ver 的历史 token 按版本 0 处理；用户已删除或查库失败都视为失效。
func (s *AuthService) IsTokenVersionCurrent(userID string, ver int64) (bool, error) {
	user, err := s.userRepo.FindByID(userID)
	if err != nil {
		return false, err
	}
	return int64(user.TokenVersion) == ver, nil
}

func (s *AuthService) generateToken(user *model.User) (string, error) {
	jti := uuid.New().String()
	claims := jwt.MapClaims{
		"sub":      user.ID,
		"username": user.Username,
		"jti":      jti,
		"iat":      time.Now().Unix(),
		"exp":      time.Now().Add(time.Duration(s.cfg.JWT.ExpireHours) * time.Hour).Unix(),
		"ver":      user.TokenVersion,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(s.cfg.JWT.Secret))
}

func (s *AuthService) toUserResponse(user *model.User) UserResponse {
	return newUserResponse(user)
}

// RoleOf 查用户角色（INT-129）：管理员守卫每请求调用，不缓存。用户已删除时
// 返回空串（调用方按非 admin 处理）。
func (s *AuthService) RoleOf(ctx context.Context, userID string) (string, error) {
	user, err := s.userRepo.FindByID(userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", nil
		}
		return "", err
	}
	return user.Role, nil
}

// GenerateApiKeyRaw 生成 API Key 原始值（Base62 编码的随机字节）
func GenerateApiKeyRaw() (string, error) {
	const charset = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	b := make([]byte, 40)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		if err != nil {
			return "", err
		}
		b[i] = charset[n.Int64()]
	}
	return string(b), nil
}

// HashApiKey 计算 API Key 的 SHA-256 哈希
func HashApiKey(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])
}

// FormatApiKey 添加前缀
func FormatApiKey(raw string) string {
	return fmt.Sprintf("fsk_%s", raw)
}

// generateRefreshToken 仅在用户 token_version 仍是验证时读到的版本时写入；
// created=false 表示签发期间版本已被改密推进，该凭证不得落地。
func (s *AuthService) generateRefreshToken(user *model.User) (string, bool, error) {
	raw, refreshToken, err := s.buildRefreshToken(user.ID)
	if err != nil {
		return "", false, err
	}

	created, err := s.refreshTokenRepo.CreateIfTokenVersion(refreshToken, user.TokenVersion)
	if err != nil {
		return "", false, err
	}
	if !created {
		return "", false, nil
	}

	return fmt.Sprintf("fsr_%s", raw), true, nil
}

func (s *AuthService) rotateRefreshToken(current *model.RefreshToken) (string, bool, error) {
	raw, next, err := s.buildRefreshToken(current.UserID)
	if err != nil {
		return "", false, err
	}

	rotated, err := s.refreshTokenRepo.Rotate(current.ID, next)
	if err != nil {
		return "", false, err
	}
	if !rotated {
		return "", false, nil
	}

	return fmt.Sprintf("fsr_%s", raw), true, nil
}

func (s *AuthService) buildRefreshToken(userID string) (string, *model.RefreshToken, error) {
	raw, err := GenerateApiKeyRaw()
	if err != nil {
		return "", nil, err
	}

	tokenHash := HashApiKey(raw)
	hours := s.cfg.JWT.RefreshExpireHours
	if hours <= 0 {
		hours = 168 // default 7 days
	}

	rt := &model.RefreshToken{
		ID:        uuid.New().String(),
		UserID:    userID,
		TokenHash: tokenHash,
		ExpiresAt: time.Now().Add(time.Duration(hours) * time.Hour),
		CreatedAt: time.Now(),
	}

	return raw, rt, nil
}
