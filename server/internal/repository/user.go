package repository

import (
	"errors"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

type UserRepository struct {
	db *gorm.DB
}

func NewUserRepository(db *gorm.DB) *UserRepository {
	return &UserRepository{db: db}
}

func (r *UserRepository) Create(user *model.User) error {
	return r.db.Create(user).Error
}

func (r *UserRepository) FindByID(id string) (*model.User, error) {
	var user model.User
	err := r.db.Where("id = ?", id).First(&user).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// ListByIDs 一次查询多个用户，返回 id -> User 映射，用于协作区批量解析作者信息。
func (r *UserRepository) ListByIDs(ids []string) (map[string]model.User, error) {
	result := make(map[string]model.User, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	var users []model.User
	if err := r.db.Where("id IN ?", ids).Find(&users).Error; err != nil {
		return nil, err
	}
	for _, user := range users {
		result[user.ID] = user
	}
	return result, nil
}

func (r *UserRepository) FindByUsername(username string) (*model.User, error) {
	var user model.User
	err := r.db.Where("username = ?", username).First(&user).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *UserRepository) FindByEmail(email string) (*model.User, error) {
	var user model.User
	err := r.db.Where("email = ?", email).First(&user).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *UserRepository) FindByUsernameOrEmail(login string) (*model.User, error) {
	var user model.User
	err := r.db.Where("username = ? OR email = ?", login, login).First(&user).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

var forbiddenUserUpdateColumns = map[string]bool{
	"password_hash": true,
	"token_version": true,
	"created_at":    true,
}

// UpdateFields 只写入给定列。禁止整行 Save 回写：调用方持有的 user 副本可能
// 早于一次已提交的改密，全量回写会把 token_version/password_hash 覆盖回旧值，
// 复活本该吊销的全部会话。认证安全列（密码、版本、创建时间）只允许各自的
// 专用事务方法写，这里直接拒绝。
func (r *UserRepository) UpdateFields(userID string, fields map[string]any) error {
	for col := range fields {
		if forbiddenUserUpdateColumns[col] {
			return errors.New("UpdateFields: 拒绝写认证安全列 " + col)
		}
	}
	return r.db.Model(&model.User{}).Where("id = ?", userID).Updates(fields).Error
}

// ErrPasswordChanged 表示改密事务因密码已被并发修改而拒绝提交：
// 调用方验证过的旧密码哈希在写时点上已不成立。
var ErrPasswordChanged = errors.New("password changed concurrently")

// UpdatePasswordAndRotateSessions 在同一事务里改密并轮换会话凭证：更新密码哈希、
// token_version 在事务内原子自增并回读、吊销该用户全部未吊销的 refresh token，
// 再写入为当前会话新签发的 refresh token。任一失败整体回滚——已改密的用户
// 不会落在新旧会话都不可用的中间态。并发改密各自拿到不同版本号：自增必须在
// 事务内完成，用调用方预读的 nextVersion 写回会让两次改密停在同一版本。
// expectedHash 是调用方刚通过 bcrypt 校验的旧密码哈希：写路径必须以它为谓词，
// 否则先校验旧密码、另一事务先提交改密的请求会拿着已作废的授权覆盖新密码。
func (r *UserRepository) UpdatePasswordAndRotateSessions(userID, expectedHash, passwordHash string, next *model.RefreshToken) (int, error) {
	now := time.Now()
	var newVersion int
	err := r.db.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&model.User{}).
			Where("id = ? AND password_hash = ?", userID, expectedHash).
			Updates(map[string]any{
				"password_hash": passwordHash,
				"token_version": gorm.Expr("token_version + 1"),
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrPasswordChanged
		}
		if err := tx.Model(&model.User{}).Where("id = ?", userID).Pluck("token_version", &newVersion).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.RefreshToken{}).
			Where("user_id = ? AND revoked_at IS NULL", userID).
			Update("revoked_at", now).Error; err != nil {
			return err
		}
		return tx.Create(next).Error
	})
	return newVersion, err
}

func (r *UserRepository) ExistsByUsername(username string) (bool, error) {
	var count int64
	err := r.db.Model(&model.User{}).Where("username = ?", username).Count(&count).Error
	return count > 0, err
}

func (r *UserRepository) ExistsByEmail(email string) (bool, error) {
	var count int64
	err := r.db.Model(&model.User{}).Where("email = ?", email).Count(&count).Error
	return count > 0, err
}

// Transaction 以短事务执行 fn（INT-129：注册的角色判定与邀请消费、改角色的
// 最后管理员判定都必须整体提交或回滚）。闭包内只用 tx 句柄与显式收句柄的
// Tx 方法，不得触碰池根——单连接池下再借第二条连接会永久自锁，
// 见 docs/internal/database.md。
func (r *UserRepository) Transaction(fn func(tx *gorm.DB) error) error {
	return r.db.Transaction(fn)
}

// Count 全库用户数：注册路径用「库里还没有用户」判定首个用户（首个 = admin）。
func (r *UserRepository) Count() (int64, error) {
	return r.CountTx(r.db)
}

func (r *UserRepository) CountTx(tx *gorm.DB) (int64, error) {
	var count int64
	err := tx.Model(&model.User{}).Count(&count).Error
	return count, err
}

// ListAll 全部成员，按 created_at 升序（首个用户=最早的 admin 排最前）。
func (r *UserRepository) ListAll() ([]model.User, error) {
	var users []model.User
	err := r.db.Order("created_at ASC, id ASC").Find(&users).Error
	return users, err
}

func (r *UserRepository) FindByIDTx(tx *gorm.DB, id string) (*model.User, error) {
	var user model.User
	if err := tx.Where("id = ?", id).First(&user).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *UserRepository) CreateTx(tx *gorm.DB, user *model.User) error {
	return tx.Create(user).Error
}

// UpdateRole 改单个用户的角色；调用方负责最后一个管理员的业务判定。
func (r *UserRepository) UpdateRole(id, role string) error {
	return r.UpdateRoleTx(r.db, id, role)
}

func (r *UserRepository) UpdateRoleTx(tx *gorm.DB, id, role string) error {
	return tx.Model(&model.User{}).Where("id = ?", id).Update("role", role).Error
}

// CountAdmins 当前管理员数量：改角色的最后管理员判定、升级迁移的幂等条件。
func (r *UserRepository) CountAdmins() (int64, error) {
	return r.CountAdminsTx(r.db)
}

func (r *UserRepository) CountAdminsTx(tx *gorm.DB) (int64, error) {
	var count int64
	err := tx.Model(&model.User{}).Where("role = ?", model.UserRoleAdmin).Count(&count).Error
	return count, err
}

// EarliestCreatedID created_at 最早的用户 ID（升级迁移把既有部署的最早用户
// 置为 admin）；无用户时返回 gorm.ErrRecordNotFound。
func (r *UserRepository) EarliestCreatedID() (string, error) {
	return r.EarliestCreatedIDTx(r.db)
}

func (r *UserRepository) EarliestCreatedIDTx(tx *gorm.DB) (string, error) {
	var user model.User
	if err := tx.Order("created_at ASC, id ASC").First(&user).Error; err != nil {
		return "", err
	}
	return user.ID, nil
}
