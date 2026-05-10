package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
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
}

func NewAuthService(userRepo *repository.UserRepository, jwtBlacklistRepo *repository.JWTBlacklistRepository, refreshTokenRepo *repository.RefreshTokenRepository, cfg *config.Config) *AuthService {
	return &AuthService{
		userRepo:         userRepo,
		jwtBlacklistRepo: jwtBlacklistRepo,
		refreshTokenRepo: refreshTokenRepo,
		cfg:              cfg,
	}
}

type RegisterRequest struct {
	Username string `json:"username" binding:"required,min=2,max=50"`
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required,min=8"`
}

type LoginRequest struct {
	Login    string `json:"login" binding:"required"`
	Password string `json:"password" binding:"required"`
}

type UpdateProfileRequest struct {
	Username string `json:"username" binding:"omitempty,min=2,max=50"`
	Email    string `json:"email" binding:"omitempty,email"`
}

type UpdatePasswordRequest struct {
	OldPassword string `json:"old_password" binding:"required"`
	NewPassword string `json:"new_password" binding:"required,min=8"`
}

type AuthResponse struct {
	Token        string       `json:"token"`
	RefreshToken string       `json:"refresh_token"`
	User         UserResponse `json:"user"`
}

type RefreshResponse struct {
	Token        string `json:"token"`
	RefreshToken string `json:"refresh_token"`
}

type RefreshRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

type UserResponse struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (s *AuthService) Register(req *RegisterRequest) (*AuthResponse, error) {
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

	token, err := s.generateToken(user.ID, user.Username)
	if err != nil {
		return nil, errs.ErrInternal
	}

	refreshToken, err := s.generateRefreshToken(user.ID)
	if err != nil {
		return nil, errs.ErrInternal
	}

	return &AuthResponse{
		Token:        token,
		RefreshToken: refreshToken,
		User:         s.toUserResponse(user),
	}, nil
}

func (s *AuthService) Login(req *LoginRequest) (*AuthResponse, error) {
	user, err := s.userRepo.FindByUsernameOrEmail(req.Login)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrLoginFailed
		}
		return nil, errs.ErrInternal
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		return nil, errs.ErrLoginFailed
	}

	token, err := s.generateToken(user.ID, user.Username)
	if err != nil {
		return nil, errs.ErrInternal
	}

	refreshToken, err := s.generateRefreshToken(user.ID)
	if err != nil {
		return nil, errs.ErrInternal
	}

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

	token, err := s.generateToken(user.ID, user.Username)
	if err != nil {
		return nil, errs.ErrInternal
	}

	newRefreshToken, err := s.rotateRefreshToken(rt)
	if err != nil {
		return nil, errs.ErrInternal
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

	if req.Username != "" && req.Username != user.Username {
		exists, err := s.userRepo.ExistsByUsername(req.Username)
		if err != nil {
			return nil, errs.ErrInternal
		}
		if exists {
			return nil, errs.ErrUsernameExists
		}
		user.Username = req.Username
	}

	if req.Email != "" && req.Email != user.Email {
		exists, err := s.userRepo.ExistsByEmail(req.Email)
		if err != nil {
			return nil, errs.ErrInternal
		}
		if exists {
			return nil, errs.ErrEmailExists
		}
		user.Email = req.Email
	}

	if err := s.userRepo.Update(user); err != nil {
		return nil, errs.ErrInternal
	}

	resp := s.toUserResponse(user)
	return &resp, nil
}

func (s *AuthService) UpdatePassword(userID string, req *UpdatePasswordRequest) error {
	user, err := s.userRepo.FindByID(userID)
	if err != nil {
		return errs.ErrUserNotFound
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.OldPassword)); err != nil {
		return errs.ErrLoginFailed
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		return errs.ErrInternal
	}

	user.PasswordHash = string(hashedPassword)
	return s.userRepo.Update(user)
}

func (s *AuthService) IsTokenBlacklisted(jti string) (bool, error) {
	return s.jwtBlacklistRepo.Exists(jti)
}

func (s *AuthService) generateToken(userID, username string) (string, error) {
	jti := uuid.New().String()
	claims := jwt.MapClaims{
		"sub":      userID,
		"username": username,
		"jti":      jti,
		"iat":      time.Now().Unix(),
		"exp":      time.Now().Add(time.Duration(s.cfg.JWT.ExpireHours) * time.Hour).Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(s.cfg.JWT.Secret))
}

func (s *AuthService) toUserResponse(user *model.User) UserResponse {
	return UserResponse{
		ID:        user.ID,
		Username:  user.Username,
		Email:     user.Email,
		CreatedAt: user.CreatedAt,
		UpdatedAt: user.UpdatedAt,
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

func (s *AuthService) generateRefreshToken(userID string) (string, error) {
	raw, refreshToken, err := s.buildRefreshToken(userID)
	if err != nil {
		return "", err
	}

	if err := s.refreshTokenRepo.Create(refreshToken); err != nil {
		return "", err
	}

	return fmt.Sprintf("fsr_%s", raw), nil
}

func (s *AuthService) rotateRefreshToken(current *model.RefreshToken) (string, error) {
	raw, next, err := s.buildRefreshToken(current.UserID)
	if err != nil {
		return "", err
	}

	if err := s.refreshTokenRepo.Rotate(current.ID, next); err != nil {
		return "", err
	}

	return fmt.Sprintf("fsr_%s", raw), nil
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
