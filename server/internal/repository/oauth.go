package repository

import (
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

// OAuthRepository OAuth 2.1 授权服务（INT-222）的存储面：客户端注册、授权
// 总账、授权码与 access/refresh 令牌。事务闭包内只用 tx 句柄（单连接池规则，
// 见 docs/internal/auth.md）。
type OAuthRepository struct {
	db *gorm.DB
}

func NewOAuthRepository(db *gorm.DB) *OAuthRepository {
	return &OAuthRepository{db: db}
}

// Transaction 单连接池事务边界：闭包内只用 tx 句柄（见 docs/internal/auth.md）。
func (r *OAuthRepository) Transaction(fn func(tx *gorm.DB) error) error {
	return r.db.Transaction(fn)
}

// ---- 客户端（RFC 7591 动态注册） ----

func (r *OAuthRepository) CreateClient(client *model.OAuthClient) error {
	return r.db.Create(client).Error
}

func (r *OAuthRepository) FindClient(clientID string) (*model.OAuthClient, error) {
	var client model.OAuthClient
	err := r.db.Where("client_id = ?", clientID).First(&client).Error
	if err != nil {
		return nil, err
	}
	return &client, nil
}

// ---- 授权总账 ----

// UpsertAuthorizationTx 同一事务里 upsert 该 (user, client) 的授权：重授权
// 覆盖 scopes 并清掉吊销标记。返回总账行 ID（调用方据此挂授权码）。
func (r *OAuthRepository) UpsertAuthorizationTx(tx *gorm.DB, auth *model.OAuthAuthorization) (string, error) {
	var existing model.OAuthAuthorization
	err := tx.Where("user_id = ? AND client_id = ?", auth.UserID, auth.ClientID).First(&existing).Error
	if err == nil {
		res := tx.Model(&model.OAuthAuthorization{}).
			Where("id = ?", existing.ID).
			Updates(map[string]any{"scopes": auth.Scopes, "revoked_at": nil})
		if res.Error != nil {
			return "", res.Error
		}
		return existing.ID, nil
	}
	if err != gorm.ErrRecordNotFound {
		return "", err
	}
	if err := tx.Create(auth).Error; err != nil {
		return "", err
	}
	return auth.ID, nil
}

func (r *OAuthRepository) FindAuthorizationByID(id, userID string) (*model.OAuthAuthorization, error) {
	var auth model.OAuthAuthorization
	err := r.db.Where("id = ? AND user_id = ?", id, userID).First(&auth).Error
	if err != nil {
		return nil, err
	}
	return &auth, nil
}

// AuthorizationWithClient 按授权清单投影取授权 + 客户端名。
type AuthorizationWithClient struct {
	model.OAuthAuthorization
	ClientName string
}

func (r *OAuthRepository) ListAuthorizations(userID string) ([]AuthorizationWithClient, error) {
	var rows []AuthorizationWithClient
	err := r.db.
		Table("oauth_authorizations AS a").
		Select("a.*, c.client_name AS client_name").
		Joins("JOIN oauth_clients AS c ON c.client_id = a.client_id").
		Where("a.user_id = ? AND a.revoked_at IS NULL", userID).
		Order("a.created_at DESC").
		Scan(&rows).Error
	return rows, err
}

// RevokeAuthorization 吊销授权 + 该授权下全部 access/refresh 令牌（即时生效）。
// 返回是否命中（非本人或不存在的 id 返回 false）。
func (r *OAuthRepository) RevokeAuthorization(id, userID string) (bool, error) {
	now := time.Now()
	var hit bool
	err := r.db.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&model.OAuthAuthorization{}).
			Where("id = ? AND user_id = ? AND revoked_at IS NULL", id, userID).
			Update("revoked_at", now)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil
		}
		hit = true
		if err := revokeTokensByAuthorizationTx(tx, id, now); err != nil {
			return err
		}
		return nil
	})
	return hit, err
}

// RevokeAllForUserTx 同一事务里吊销该用户全部 OAuth 授权与令牌（改密联动）。
func (r *OAuthRepository) RevokeAllForUserTx(tx *gorm.DB, userID string) error {
	now := time.Now()
	if err := tx.Model(&model.OAuthAuthorization{}).
		Where("user_id = ? AND revoked_at IS NULL", userID).
		Update("revoked_at", now).Error; err != nil {
		return err
	}
	if err := tx.Model(&model.OAuthAccessToken{}).
		Where("user_id = ? AND revoked_at IS NULL", userID).
		Update("revoked_at", now).Error; err != nil {
		return err
	}
	return tx.Model(&model.OAuthRefreshToken{}).
		Where("user_id = ? AND revoked_at IS NULL", userID).
		Update("revoked_at", now).Error
}

func revokeTokensByAuthorizationTx(tx *gorm.DB, authorizationID string, now time.Time) error {
	if err := tx.Model(&model.OAuthAccessToken{}).
		Where("authorization_id = ? AND revoked_at IS NULL", authorizationID).
		Update("revoked_at", now).Error; err != nil {
		return err
	}
	return tx.Model(&model.OAuthRefreshToken{}).
		Where("authorization_id = ? AND revoked_at IS NULL", authorizationID).
		Update("revoked_at", now).Error
}

// ---- 授权码 ----

// CreateAuthorizationCodeTx 授权提交事务内写码。
func (r *OAuthRepository) CreateAuthorizationCodeTx(tx *gorm.DB, code *model.OAuthAuthorizationCode) error {
	return tx.Create(code).Error
}

func (r *OAuthRepository) FindAuthorizationCode(codeHash string) (*model.OAuthAuthorizationCode, error) {
	var code model.OAuthAuthorizationCode
	err := r.db.Where("code_hash = ?", codeHash).First(&code).Error
	if err != nil {
		return nil, err
	}
	return &code, nil
}

// FindAuthorizationCodeTx 事务内读码（token 事务用它把码换令牌）。
func (r *OAuthRepository) FindAuthorizationCodeTx(tx *gorm.DB, codeHash string) (*model.OAuthAuthorizationCode, error) {
	var code model.OAuthAuthorizationCode
	err := tx.Where("code_hash = ?", codeHash).First(&code).Error
	if err != nil {
		return nil, err
	}
	return &code, nil
}

// ConsumeAuthorizationCodeTx 原子置 used_at：重放时 RowsAffected=0。
func (r *OAuthRepository) ConsumeAuthorizationCodeTx(tx *gorm.DB, id string) (bool, error) {
	now := time.Now()
	res := tx.Model(&model.OAuthAuthorizationCode{}).
		Where("id = ? AND used_at IS NULL", id).
		Update("used_at", now)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// ---- access / refresh 令牌 ----

func (r *OAuthRepository) CreateAccessTokenTx(tx *gorm.DB, token *model.OAuthAccessToken) error {
	return tx.Create(token).Error
}

func (r *OAuthRepository) CreateRefreshTokenTx(tx *gorm.DB, token *model.OAuthRefreshToken) error {
	return tx.Create(token).Error
}

func (r *OAuthRepository) FindAccessTokenByHash(tokenHash string) (*model.OAuthAccessToken, error) {
	var token model.OAuthAccessToken
	err := r.db.Where("token_hash = ? AND expires_at > ? AND revoked_at IS NULL", tokenHash, time.Now()).First(&token).Error
	if err != nil {
		return nil, err
	}
	return &token, nil
}

func (r *OAuthRepository) FindRefreshTokenByHash(tokenHash string) (*model.OAuthRefreshToken, error) {
	var token model.OAuthRefreshToken
	err := r.db.Where("token_hash = ? AND expires_at > ? AND revoked_at IS NULL", tokenHash, time.Now()).First(&token).Error
	if err != nil {
		return nil, err
	}
	return &token, nil
}

// FindRefreshTokenByHashTx 事务内读 refresh token（单连接池规则：事务闭包
// 内不得走池根）。
func (r *OAuthRepository) FindRefreshTokenByHashTx(tx *gorm.DB, tokenHash string) (*model.OAuthRefreshToken, error) {
	var token model.OAuthRefreshToken
	err := tx.Where("token_hash = ? AND expires_at > ? AND revoked_at IS NULL", tokenHash, time.Now()).First(&token).Error
	if err != nil {
		return nil, err
	}
	return &token, nil
}

// RotateRefreshTokenTx 刷新轮换栅栏：先条件吊销旧行（RowsAffected=1 才继续，
// 旧行已被改密扫描/吊销授权抢先动过则中止），再在同一事务内插入新对。顺序
// 不能反过来——先创建会让吊销扫描漏掉两条语句之间落地的新令牌。
func (r *OAuthRepository) RotateRefreshTokenTx(tx *gorm.DB, currentID string, nextAccess *model.OAuthAccessToken, nextRefresh *model.OAuthRefreshToken) (bool, error) {
	now := time.Now()

	res := tx.Model(&model.OAuthRefreshToken{}).
		Where("id = ? AND revoked_at IS NULL", currentID).
		Update("revoked_at", now)
	if res.Error != nil {
		return false, res.Error
	}
	if res.RowsAffected == 0 {
		return false, nil
	}
	if err := tx.Create(nextAccess).Error; err != nil {
		return false, err
	}
	if err := tx.Create(nextRefresh).Error; err != nil {
		return false, err
	}
	return true, nil
}
