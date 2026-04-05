package service

import (
	"crypto/sha256"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
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
	cfg              *config.Config
}

func NewAuthService(userRepo *repository.UserRepository, jwtBlacklistRepo *repository.JWTBlacklistRepository, cfg *config.Config) *AuthService {
	return &AuthService{
		userRepo:         userRepo,
		jwtBlacklistRepo: jwtBlacklistRepo,
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

type TokenResponse struct {
	Token string `json:"token"`
}

type UserResponse struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *AuthService) Register(req *RegisterRequest) (*UserResponse, error) {
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

	return &UserResponse{
		ID:        user.ID,
		Username:  user.Username,
		Email:     user.Email,
		CreatedAt: user.CreatedAt,
	}, nil
}

func (s *AuthService) Login(req *LoginRequest) (*TokenResponse, error) {
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

	token, err := s.generateToken(user.ID)
	if err != nil {
		return nil, errs.ErrInternal
	}

	return &TokenResponse{Token: token}, nil
}

func (s *AuthService) Logout(jti string, exp time.Time) error {
	return s.jwtBlacklistRepo.Add(jti, exp)
}

func (s *AuthService) GetMe(userID string) (*UserResponse, error) {
	user, err := s.userRepo.FindByID(userID)
	if err != nil {
		return nil, errs.ErrUserNotFound
	}
	return &UserResponse{
		ID:        user.ID,
		Username:  user.Username,
		Email:     user.Email,
		CreatedAt: user.CreatedAt,
	}, nil
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

	return &UserResponse{
		ID:        user.ID,
		Username:  user.Username,
		Email:     user.Email,
		CreatedAt: user.CreatedAt,
	}, nil
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

func (s *AuthService) generateToken(userID string) (string, error) {
	jti := uuid.New().String()
	claims := jwt.MapClaims{
		"sub": userID,
		"jti": jti,
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(time.Duration(s.cfg.JWT.ExpireHours) * time.Hour).Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(s.cfg.JWT.Secret))
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
