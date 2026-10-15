package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/crypto"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	ghclient "github.com/godbobo/fast_ship/server/internal/pkg/github"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// GitHubTokenService 管理用户级 GitHub Token 库：去重、检测与引用感知的增删改。
// 密文与指纹只在这里落库；项目引用的读写归 ProjectService / ProjectCredentials。
// detect 是注入点，测试替换成 fake；生产走 ghclient.TokenDetector（只读 GET /user）。
type GitHubTokenService struct {
	tokenRepo *repository.UserGitHubTokenRepository
	cfg       *config.Config
	logger    *zap.Logger
	detect    func(ctx context.Context, token string) ghclient.TokenDetection
}

func NewGitHubTokenService(tokenRepo *repository.UserGitHubTokenRepository, cfg *config.Config, logger *zap.Logger) *GitHubTokenService {
	detector := ghclient.NewTokenDetector()
	return &GitHubTokenService{
		tokenRepo: tokenRepo,
		cfg:       cfg,
		logger:    logger,
		detect:    detector.Detect,
	}
}

// githubTokenFingerprint 是明文的 SHA-256 hex：AES-GCM 密文每次都不同，
// 同一用户去重只能按明文指纹。
func githubTokenFingerprint(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// githubTokenPrefix 取明文前 8 个可见字符用于列表辨识；其余不存。
func githubTokenPrefix(plaintext string) string {
	if len(plaintext) > 8 {
		return plaintext[:8]
	}
	return plaintext
}

// normalizeGitHubTokenPlaintext 去掉首尾空白；空白即视为未提供。
// GitHub PAT 不含空格，粘贴带来的空格会让指纹对不上、检测 401。
func normalizeGitHubTokenPlaintext(raw string) (string, *errs.AppError) {
	token := strings.TrimSpace(raw)
	if token == "" {
		return "", errs.ErrInvalidParams
	}
	return token, nil
}

func resetDetection(token *model.UserGitHubToken) {
	token.DetectionStatus = model.TokenDetectionUnchecked
	token.DetectionType = model.TokenTypeUnknown
	token.DetectionScopes = "[]"
	token.DetectionLogin = ""
	token.DetectionDetail = ""
	token.DetectedAt = nil
}

// toGitHubTokenResponse 把存储模型投影为契约类型；检测 scopes 列存 JSON 数组字符串。
func toGitHubTokenResponse(token model.UserGitHubToken, refs []repository.ReferencingProject) api.GitHubToken {
	scopes := []string{}
	if token.DetectionScopes != "" {
		_ = json.Unmarshal([]byte(token.DetectionScopes), &scopes)
	}
	if scopes == nil {
		scopes = []string{}
	}
	referenced := make([]api.GitHubTokenProjectRef, 0, len(refs))
	for _, ref := range refs {
		referenced = append(referenced, api.GitHubTokenProjectRef{Id: ref.ID, Name: ref.Name})
	}
	resp := api.GitHubToken{
		Id:                 token.ID,
		Title:              token.Title,
		TokenPrefix:        token.TokenPrefix,
		DetectionStatus:    api.GitHubTokenDetectionStatus(token.DetectionStatus),
		DetectionTokenType: api.GitHubTokenDetectionTokenType(token.DetectionType),
		DetectionScopes:    scopes,
		DetectionLogin:     token.DetectionLogin,
		DetectionDetail:    token.DetectionDetail,
		DetectedAt:         api.JSONTimePtr(token.DetectedAt),
		ReferencedProjects: referenced,
		CreatedAt:          api.JSONTime(token.CreatedAt),
		UpdatedAt:          api.JSONTime(token.UpdatedAt),
	}
	return resp
}

func (s *GitHubTokenService) List(userID string) ([]api.GitHubToken, error) {
	tokens, err := s.tokenRepo.ListByUserID(userID)
	if err != nil {
		return nil, errs.ErrInternal
	}
	ids := make([]string, len(tokens))
	for i, token := range tokens {
		ids[i] = token.ID
	}
	refsByToken, err := s.tokenRepo.ListReferencingProjectsForTokens(ids, userID)
	if err != nil {
		return nil, errs.ErrInternal
	}
	result := make([]api.GitHubToken, len(tokens))
	for i, token := range tokens {
		result[i] = toGitHubTokenResponse(token, refsByToken[token.ID])
	}
	return result, nil
}

// Create 保存一个新 Token；明文指纹与同用户已有记录相同时直接返回已有记录，
// 不修改其标题。新记录保存后同步做一次检测：检测失败（网络/5xx）或凭证无效
// （401）都不回滚保存——HTTP 仍 200，状态落在 detection_status 里。
func (s *GitHubTokenService) Create(userID string, req *CreateGitHubTokenRequest) (*api.GitHubToken, error) {
	plaintext, appErr := normalizeGitHubTokenPlaintext(req.Token)
	if appErr != nil {
		return nil, appErr
	}
	fingerprint := githubTokenFingerprint(plaintext)
	if existing, err := s.tokenRepo.FindByFingerprint(userID, fingerprint); err == nil {
		return s.respond(existing, false, userID)
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errs.ErrInternal
	}

	now := time.Now().UTC()
	token := &model.UserGitHubToken{
		ID:               uuid.NewString(),
		UserID:           userID,
		Title:            req.Title,
		TokenFingerprint: fingerprint,
		TokenPrefix:      githubTokenPrefix(plaintext),
		DetectionStatus:  model.TokenDetectionUnchecked,
		DetectionType:    model.TokenTypeUnknown,
		DetectionScopes:  "[]",
	}
	var err error
	token.TokenEncrypted, err = crypto.Encrypt([]byte(plaintext), []byte(s.cfg.Encryption.Key))
	if err != nil {
		return nil, errs.ErrInternal
	}
	if err := s.tokenRepo.Create(token); err != nil {
		if isUniqueConstraintError(err) {
			existing, findErr := s.tokenRepo.FindByFingerprint(userID, fingerprint)
			if findErr == nil {
				return s.respond(existing, false, userID)
			}
		}
		return nil, errs.ErrInternal
	}

	updated, appErr := s.runDetection(token, plaintext, now)
	if appErr != nil {
		// 检测通道故障（而非检测结果失败）不阻断保存：记录保持 unchecked。
		s.logger.With(zap.String("user_id", userID)).Warn("github token detection skipped", zap.String("token_id", token.ID), zap.String("error", appErr.Message))
	}
	return s.respond(token, updated, userID)
}

// Update 改标题或替换密钥。改标题只写 title，不重检；替换密钥对新明文先查
// 指纹——撞上同用户另一条记录返回 40914 并点名已有记录，不做隐式合并；
// 替换按加载时的指纹条件写入，并发替换返回 40915。替换即对所有引用项目
// 生效，随后重新检测。
func (s *GitHubTokenService) Update(userID, id string, req *UpdateGitHubTokenRequest) (*api.GitHubToken, error) {
	token, err := s.tokenRepo.FindByID(id, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrGitHubTokenNotFound
		}
		return nil, errs.ErrInternal
	}

	newPlaintext := ""
	replacing := false
	loadedFingerprint := token.TokenFingerprint
	if req.Token != nil {
		normalized, appErr := normalizeGitHubTokenPlaintext(*req.Token)
		if appErr != nil {
			return nil, appErr
		}
		newPlaintext = normalized
		replacing = true
	}
	if replacing {
		fingerprint := githubTokenFingerprint(newPlaintext)
		if fingerprint != loadedFingerprint {
			existing, findErr := s.tokenRepo.FindByFingerprint(userID, fingerprint)
			if findErr == nil {
				return nil, errs.New(errs.ErrGitHubTokenDuplicate.Code,
					errs.ErrGitHubTokenDuplicate.Message+"：已有记录「"+existing.Title+"」")
			} else if !errors.Is(findErr, gorm.ErrRecordNotFound) {
				return nil, errs.ErrInternal
			}
		}
		encrypted, encErr := crypto.Encrypt([]byte(newPlaintext), []byte(s.cfg.Encryption.Key))
		if encErr != nil {
			return nil, errs.ErrInternal
		}
		token.TokenEncrypted = encrypted
		token.TokenFingerprint = fingerprint
		token.TokenPrefix = githubTokenPrefix(newPlaintext)
		// 与新密文同一次写入把检测打回 unchecked，避免列表在检测完成前
		// 用旧 login/status 搭配新 prefix。
		resetDetection(token)
		if err := s.tokenRepo.ReplaceSecret(token, loadedFingerprint); err != nil {
			if errors.Is(err, repository.ErrGitHubTokenConflict) {
				return nil, errs.ErrGitHubTokenConflict
			}
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, errs.ErrGitHubTokenNotFound
			}
			if isUniqueConstraintError(err) {
				existing, findErr := s.tokenRepo.FindByFingerprint(userID, token.TokenFingerprint)
				if findErr == nil {
					return nil, errs.New(errs.ErrGitHubTokenDuplicate.Code,
						errs.ErrGitHubTokenDuplicate.Message+"：已有记录「"+existing.Title+"」")
				}
			}
			return nil, errs.ErrInternal
		}
	}
	if req.Title != nil && *req.Title != "" {
		if err := s.tokenRepo.UpdateTitle(id, userID, *req.Title); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, errs.ErrGitHubTokenNotFound
			}
			return nil, errs.ErrInternal
		}
		token.Title = *req.Title
	}

	if replacing {
		if _, appErr := s.runDetection(token, newPlaintext, time.Now().UTC()); appErr != nil {
			s.logger.With(zap.String("user_id", userID)).Warn("github token detection skipped", zap.String("token_id", token.ID), zap.String("error", appErr.Message))
		}
	}
	return s.respond(token, true, userID)
}

// Delete 仅在无任何项目引用（两个用途都不引用）时删除。
func (s *GitHubTokenService) Delete(userID, id string) error {
	if _, err := s.tokenRepo.FindByID(id, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrGitHubTokenNotFound
		}
		return errs.ErrInternal
	}
	deleted, err := s.tokenRepo.DeleteIfUnreferenced(id, userID)
	if err != nil {
		return errs.ErrInternal
	}
	if deleted == 0 {
		if _, findErr := s.tokenRepo.FindByID(id, userID); findErr != nil {
			if errors.Is(findErr, gorm.ErrRecordNotFound) {
				return nil
			}
			return errs.ErrInternal
		}
		return errs.ErrGitHubTokenInUse
	}
	return nil
}

// Detect 手动刷新一次只读检测并保存结果。
func (s *GitHubTokenService) Detect(userID, id string) (*api.GitHubToken, error) {
	token, err := s.tokenRepo.FindByID(id, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrGitHubTokenNotFound
		}
		return nil, errs.ErrInternal
	}
	plaintext, appErr := s.decrypt(token)
	if appErr != nil {
		return nil, appErr
	}
	if _, appErr := s.runDetection(token, plaintext, time.Now().UTC()); appErr != nil {
		return nil, appErr
	}
	return s.respond(token, true, userID)
}

// runDetection 执行检测并把结果（含失败结果）写回行；返回是否成功落库。
// 检测结果失败（invalid/failed）不是错误——同样更新行并刷新 detected_at；
// 只有检测通道本身异常（如解密失败）才返回 AppError 且不改行。
func (s *GitHubTokenService) runDetection(token *model.UserGitHubToken, plaintext string, now time.Time) (bool, *errs.AppError) {
	fingerprint := token.TokenFingerprint
	result := s.detect(context.Background(), plaintext)

	scopes := result.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	scopesJSON, err := json.Marshal(scopes)
	if err != nil {
		scopesJSON = []byte("[]")
	}
	token.DetectionStatus = result.Status
	token.DetectionType = result.TokenType
	token.DetectionScopes = string(scopesJSON)
	token.DetectionLogin = result.Login
	token.DetectionDetail = result.Detail
	detectedAt := now.UTC()
	token.DetectedAt = &detectedAt

	if err := s.tokenRepo.UpdateDetection(token, fingerprint); err != nil {
		return false, errs.ErrInternal
	}
	return true, nil
}

func (s *GitHubTokenService) decrypt(token *model.UserGitHubToken) (string, *errs.AppError) {
	plaintext, err := crypto.Decrypt(token.TokenEncrypted, []byte(s.cfg.Encryption.Key))
	if err != nil {
		s.logger.With(zap.String("user_id", token.UserID)).Error("decrypt github token failed", zap.String("token_id", token.ID), zap.Error(err))
		return "", errs.New(errs.ErrInternal.Code, "GitHub Token 解密失败，请替换该 Token")
	}
	return string(plaintext), nil
}

// respond 重新读取行（runDetection 后字段可能已更新）并附带引用项目。
func (s *GitHubTokenService) respond(token *model.UserGitHubToken, reload bool, userID string) (*api.GitHubToken, error) {
	if reload {
		fresh, err := s.tokenRepo.FindByID(token.ID, userID)
		if err == nil {
			token = fresh
		}
	}
	refs, err := s.tokenRepo.FindReferencingProjects(token.ID, userID)
	if err != nil {
		return nil, errs.ErrInternal
	}
	resp := toGitHubTokenResponse(*token, refs)
	return &resp, nil
}
