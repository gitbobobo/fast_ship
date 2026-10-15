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

func (s *AuthService) log(ctx context.Context) *slog.Logger {
	l := selflog.LoggerOr(ctx, s.logger)
	if selflog.LoggerFromContext(ctx) != nil {
		return l.With(slog.String("module", "auth"))
	}
	return l
}

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

	if err := s.userRepo.Create(user); err != nil {
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

	s.log(ctx).Info("用户注册", slog.String("user_id", user.ID), slog.String("username", user.Username))
	return &AuthResponse{
		Token:        token,
		RefreshToken: refreshToken,
		User:         s.toUserResponse(user),
	}, nil
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
	newVersion, err := s.userRepo.UpdatePasswordAndRotateSessions(userID, string(hashedPassword), next)
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
	return UserResponse{
		Id:        user.ID,
		Username:  user.Username,
		Email:     user.Email,
		AvatarUrl: user.AvatarURL,
		CreatedAt: api.JSONTime(user.CreatedAt),
		UpdatedAt: api.JSONTime(user.UpdatedAt),
	}
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
