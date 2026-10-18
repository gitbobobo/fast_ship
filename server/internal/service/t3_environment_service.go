package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/crypto"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// T3EnvironmentService 管理用户的 T3 环境连接（配对、改名、删除、探测）。
//
// 凭证链路：配对链接里的一次性 token → /oauth/token 换仅 orchestration:read
// 的 Bearer → AES-256-GCM 加密落库（与 AI Key 同路径）。配对 token 不落库。
// Bearer 过期后无法续期（原 token 已消费），环境进入 repair，需重新配对。
//
// 并发约定：远程兑换（配对码换 Bearer）不持有任何行状态；落库一律走按字段
// 条件 UPDATE（WHERE id + user_id），影响 0 行即视为已被并发删除，不复活记录、
// 不用 Save/upsert 覆盖并发写入。ctx 取消（客户端断开/预算到点）后不落库。
type T3EnvironmentService struct {
	repo     *repository.T3EnvironmentRepository
	cfg      *config.Config
	logger   *zap.Logger
	http     *t3HTTPClient
	shellHub *t3ShellHub
	budgets  t3RemoteBudgets
}

func NewT3EnvironmentService(repo *repository.T3EnvironmentRepository, cfg *config.Config, logger *zap.Logger, shellHub *t3ShellHub) *T3EnvironmentService {
	return &T3EnvironmentService{
		repo:     repo,
		cfg:      cfg,
		logger:   logger,
		http:     newT3HTTPClient(),
		shellHub: shellHub,
		budgets:  defaultT3RemoteBudgets(),
	}
}

func (s *T3EnvironmentService) Create(ctx context.Context, userID string, req *CreateT3EnvironmentRequest) (*T3EnvironmentResponse, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, errs.ErrInvalidParams
	}
	if _, err := s.repo.FindByNameAndUserID(ctx, name, userID); err == nil {
		return nil, errs.ErrT3EnvironmentNameExists
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errs.ErrInternal
	}

	env, err := s.pairEnvironment(ctx, userID, name, req.PairingUrl)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		// 远程兑换成功但请求已取消：一次性配对码已消费，不落库半截记录。
		return nil, errs.ErrInternal
	}
	if err := s.repo.Create(ctx, env); err != nil {
		// 两个同名请求都过了 FindByName 预检后，第二个插入撞 (user_id, name)
		// 唯一索引——按原始错误识别，映射 40912 而非 50000。
		if isUniqueConstraintError(err) {
			return nil, errs.ErrT3EnvironmentNameExists
		}
		return nil, errs.ErrInternal
	}

	resp := s.toResponse(env, T3EnvironmentStatusUnreachable, "尚未探测")
	if status, probeErr := s.Probe(ctx, env); status == T3EnvironmentStatusOK {
		resp.Status = status
		resp.Error = nil
	} else if probeErr != "" {
		resp.Status = status
		resp.Error = api.Ptr(probeErr)
	}
	return resp, nil
}

func (s *T3EnvironmentService) List(ctx context.Context, userID string) ([]T3EnvironmentResponse, error) {
	envs, err := s.repo.ListByUserID(ctx, userID)
	if err != nil {
		return nil, errs.ErrInternal
	}

	resp := make([]T3EnvironmentResponse, len(envs))
	roundCtx, cancel := context.WithTimeout(ctx, s.budgets.Round)
	defer cancel()
	runT3Bounded(roundCtx, len(envs), t3ListConcurrency, s.budgets.PerEnvironment, func(envCtx context.Context, i int) {
		status, probeErr := s.Probe(envCtx, &envs[i])
		resp[i] = *s.toResponse(&envs[i], status, probeErr)
	})
	return resp, nil
}

// Update 指针语义：name 出现才改名，pairing_url 出现才重新配对；都不传 40001。
// 重新配对会覆盖地址与凭证；bearer 失效（repair）的环境借此修复。
//
// 原子性：先完成全部参数校验与远程配对兑换，再构造仅含请求字段的更新集合，
// 一次条件 UPDATE 落库——中途任何失败（坏配对链接、兑换被拒、请求取消）都不
// 产生部分写入（不会出现「名字已改、重配却失败」的混合状态）。只写请求里出现
// 的列：并发改名/重配互不覆盖；环境被并发删除时条件 UPDATE 影响 0 行，返回
// 40417 且不复活记录；并发改名的唯一约束冲突映射 40912。
func (s *T3EnvironmentService) Update(ctx context.Context, userID, id string, req *UpdateT3EnvironmentRequest) (*T3EnvironmentResponse, error) {
	if req.Name == nil && req.PairingUrl == nil {
		return nil, errs.ErrInvalidParams
	}
	env, err := s.repo.FindByIDAndUserID(ctx, id, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrT3EnvironmentNotFound
		}
		return nil, errs.ErrInternal
	}
	newName := env.Name
	if req.Name != nil {
		trimmed := strings.TrimSpace(*req.Name)
		if trimmed == "" {
			return nil, errs.ErrInvalidParams
		}
		if trimmed != env.Name {
			// 先做友好的重复名预检（避免白烧一次性配对码）；真正的并发竞态
			// 由 (user_id, name) 唯一索引兜底，条件 UPDATE 返回的原始错误再识别。
			if existing, findErr := s.repo.FindByNameAndUserID(ctx, trimmed, userID); findErr == nil && existing.ID != env.ID {
				return nil, errs.ErrT3EnvironmentNameExists
			} else if findErr != nil && !errors.Is(findErr, gorm.ErrRecordNotFound) {
				return nil, errs.ErrInternal
			}
		}
		newName = trimmed
	}

	update := &repository.T3EnvironmentUpdate{}
	if newName != env.Name {
		update.Name = &newName
	}
	if req.PairingUrl != nil {
		// pairEnvironment 构造的是完整环境记录，这里只取配对产物列（地址+凭证）；
		// name 不在其中，并发改名不会被旧名覆盖。
		repaired, pairErr := s.pairEnvironment(ctx, userID, newName, *req.PairingUrl)
		if pairErr != nil {
			return nil, pairErr
		}
		if err := ctx.Err(); err != nil {
			// 兑换成功但请求已取消：一次性配对码已消费，不落库半截状态。
			return nil, errs.ErrInternal
		}
		update.Pairing = &repository.T3PairingUpdate{
			Address:              repaired.Address,
			BearerTokenEncrypted: repaired.BearerTokenEncrypted,
			BearerExpiresAt:      repaired.BearerExpiresAt,
			BearerScope:          repaired.BearerScope,
		}
	}

	if update.Name != nil || update.Pairing != nil {
		if err := ctx.Err(); err != nil {
			return nil, errs.ErrInternal
		}
		update.UpdatedAt = time.Now().UTC()
		if err := s.repo.Update(ctx, id, userID, update); err != nil {
			return nil, s.updateRepoError(err)
		}
		env.Name = newName
		env.UpdatedAt = update.UpdatedAt
		if update.Pairing != nil {
			env.Address = update.Pairing.Address
			env.BearerTokenEncrypted = update.Pairing.BearerTokenEncrypted
			env.BearerExpiresAt = update.Pairing.BearerExpiresAt
			env.BearerScope = update.Pairing.BearerScope
			// 凭证/地址已变：清掉旧订阅 trail（INT-116）。cutoff 用落库后的
			// config_revision（SQL 自增产生的新世代）：变更前读出的请求、以及
			// 并发改名后仍带着旧凭证的请求（改名不递增 revision，世代与变更前
			// 相同）一律因 revision < cutoff 被拒；携带新世代的正常请求可以重建
			// entry。Drop 只关旧代 entry，已用新配置建好的新 entry 不会被关掉。
			//
			// 新世代不能在 Go 侧「算」——自增发生在 SQL 里，所以落库后重读一次。
			fresh, reErr := s.repo.FindByIDAndUserID(ctx, id, userID)
			if reErr != nil {
				// 重读失败（典型是与并发 Delete 竞争）退回 env.ConfigRevision + 1：
				// 这是本次自增后新世代的最小可能值，宁可少拒（可能放行一个过期
				// 请求）也不能多拒（把携带新配置的正常请求挡在门外）。
				s.logger.Warn("重配后重读 config_revision 失败，退回最小可能世代做 cutoff",
					zap.String("environment_id", id), zap.Error(reErr))
				env.ConfigRevision++
				s.shellHub.Drop(userID, id, env.ConfigRevision)
			} else {
				env.ConfigRevision = fresh.ConfigRevision
				s.shellHub.Drop(userID, id, env.ConfigRevision)
			}
		}
	}

	status, probeErr := s.Probe(ctx, env)
	return s.toResponse(env, status, probeErr), nil
}

// updateRepoError 把条件 UPDATE 的三类结果翻译成业务错误：行没了（并发删除）
// → 40417，唯一索引冲突（并发重名）→ 40912，其余 → 50000。
func (s *T3EnvironmentService) updateRepoError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errs.ErrT3EnvironmentNotFound
	}
	if isUniqueConstraintError(err) {
		return errs.ErrT3EnvironmentNameExists
	}
	return errs.ErrInternal
}

// Delete 删除环境。
//
// 关于会话吊销：T3 协议的 /api/auth/clients/revoke 需要 access:write scope，
// Fast Ship 的会话没有该 scope，协议上不存在自吊销端点；Bearer 由服务端按
// expires_in 自行过期。因此删除是本地删除，不向上游发起吊销
// （见 docs/internal/threads.md 的安全章节）。
func (s *T3EnvironmentService) Delete(ctx context.Context, userID, id string) error {
	// 存在性检查服务于 40417 语义；顺带留下删除前读到的行——它的
	// config_revision 用于下面的 Drop tombstone。
	env, err := s.repo.FindByIDAndUserID(ctx, id, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrT3EnvironmentNotFound
		}
		return errs.ErrInternal
	}
	if err := ctx.Err(); err != nil {
		return errs.ErrInternal
	}
	if err := s.repo.Delete(ctx, id, userID); err != nil {
		return errs.ErrInternal
	}
	// 删除成功后清掉该环境的订阅 trail（INT-116）。tombstone 用删除前读到的
	// revision + 1：删除后行已不存在，任何在途请求最多携带「删除前读到的
	// revision」（== env.ConfigRevision），< cutoff 成立即被拒；环境 ID 不可
	// 复用，不存在携带更大 revision 的合法请求会撞上这个 tombstone。
	s.shellHub.Drop(userID, id, env.ConfigRevision+1)
	return nil
}

// Probe 探测单个环境状态；返回 (状态, 错误说明)。ctx 来自请求，取消即中止。
func (s *T3EnvironmentService) Probe(ctx context.Context, env *model.T3Environment) (T3EnvironmentStatus, string) {
	token, err := crypto.Decrypt(env.BearerTokenEncrypted, []byte(s.cfg.Encryption.Key))
	if err != nil {
		return T3EnvironmentStatusRepair, "凭证解密失败，请重新配对"
	}
	probeCtx, cancel := context.WithTimeout(ctx, s.budgets.PerEnvironment)
	defer cancel()
	return s.http.probeSession(probeCtx, env.Address, string(token))
}

// pairEnvironment 解析配对链接、换取 Bearer 并构造环境记录（未落库）。
// 远程兑换全程受 ctx（请求上下文）与单调用预算约束：取消后不落库，
// 避免客户端已报失败而配对码被消费、记录却写入的错位。
func (s *T3EnvironmentService) pairEnvironment(ctx context.Context, userID, name, pairingURL string) (*model.T3Environment, error) {
	target, err := resolveT3PairingTarget(pairingURL)
	if err != nil {
		return nil, errs.New(errs.ErrPairingURLInvalid.Code, err.Error())
	}

	exchangeCtx, cancel := context.WithTimeout(ctx, s.budgets.PerEnvironment)
	defer cancel()
	creds, err := s.http.exchangeBearer(exchangeCtx, target.HTTPBaseURL, target.Credential)
	if err != nil {
		return nil, errs.New(errs.ErrT3Upstream.Code, "配对失败: "+shortenT3Error(err))
	}

	encrypted, err := crypto.Encrypt([]byte(creds.AccessToken), []byte(s.cfg.Encryption.Key))
	if err != nil {
		return nil, errs.ErrInternal
	}

	now := time.Now().UTC()
	return &model.T3Environment{
		ID:                   uuid.New().String(),
		UserID:               userID,
		Name:                 name,
		Address:              target.HTTPBaseURL,
		BearerTokenEncrypted: encrypted,
		BearerExpiresAt:      creds.ExpiresAt,
		BearerScope:          creds.Scope,
		CreatedAt:            now,
		UpdatedAt:            now,
	}, nil
}

func (s *T3EnvironmentService) toResponse(env *model.T3Environment, status T3EnvironmentStatus, probeErr string) *T3EnvironmentResponse {
	return newT3EnvironmentResponse(env, status, probeErr)
}
