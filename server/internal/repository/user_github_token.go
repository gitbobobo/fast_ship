package repository

import (
	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

// UserGitHubTokenRepository 管理用户级 GitHub Token 库。
// 引用计数不走数据库外键（projects→user_github_tokens 无外键），被引用检查
// 由 FindReferencingProjects 在应用层完成，删除前必须调用。
type UserGitHubTokenRepository struct {
	db *gorm.DB
}

func NewUserGitHubTokenRepository(db *gorm.DB) *UserGitHubTokenRepository {
	return &UserGitHubTokenRepository{db: db}
}

func (r *UserGitHubTokenRepository) Create(token *model.UserGitHubToken) error {
	return r.db.Create(token).Error
}

func (r *UserGitHubTokenRepository) FindByID(id, userID string) (*model.UserGitHubToken, error) {
	var token model.UserGitHubToken
	err := r.db.Where("id = ? AND user_id = ?", id, userID).First(&token).Error
	if err != nil {
		return nil, err
	}
	return &token, nil
}

func (r *UserGitHubTokenRepository) FindByFingerprint(userID, fingerprint string) (*model.UserGitHubToken, error) {
	var token model.UserGitHubToken
	err := r.db.Where("user_id = ? AND token_fingerprint = ?", userID, fingerprint).First(&token).Error
	if err != nil {
		return nil, err
	}
	return &token, nil
}

func (r *UserGitHubTokenRepository) ListByUserID(userID string) ([]model.UserGitHubToken, error) {
	var tokens []model.UserGitHubToken
	err := r.db.Where("user_id = ?", userID).Order("created_at ASC").Find(&tokens).Error
	return tokens, err
}

func (r *UserGitHubTokenRepository) Update(token *model.UserGitHubToken) error {
	return r.db.Save(token).Error
}

// UpdateDetection 只写检测列，并以加载时的指纹为条件：密钥已被替换或行已删除时
// 影响 0 行，调用方丢弃这次检测结果，避免整行 Save 把旧密文写回或复活已删记录。
func (r *UserGitHubTokenRepository) UpdateDetection(token *model.UserGitHubToken, expectedFingerprint string) error {
	return r.db.Model(&model.UserGitHubToken{}).
		Where("id = ? AND token_fingerprint = ?", token.ID, expectedFingerprint).
		Updates(map[string]interface{}{
			"detection_status": token.DetectionStatus,
			"detection_type":   token.DetectionType,
			"detection_scopes": token.DetectionScopes,
			"detection_login":  token.DetectionLogin,
			"detection_detail": token.DetectionDetail,
			"detected_at":      token.DetectedAt,
		}).Error
}

func (r *UserGitHubTokenRepository) Delete(id, userID string) error {
	return r.db.Where("id = ? AND user_id = ?", id, userID).Delete(&model.UserGitHubToken{}).Error
}

// DeleteIfUnreferenced 在同一条 DELETE 里排除仍被任一用途引用的行，避免先查引用
// 再删之间插入新引用。返回影响行数：0 表示仍被引用或行已不存在。
func (r *UserGitHubTokenRepository) DeleteIfUnreferenced(id, userID string) (int64, error) {
	res := r.db.Where(`id = ? AND user_id = ? AND NOT EXISTS (
		SELECT 1 FROM projects
		WHERE user_id = ? AND (public_github_token_id = ? OR code_github_token_id = ?)
	)`, id, userID, userID, id, id).Delete(&model.UserGitHubToken{})
	return res.RowsAffected, res.Error
}

// ReferencingProject 只取引用展示所需的两列，不整行加载。
type ReferencingProject struct {
	ID   string `gorm:"column:id"`
	Name string `gorm:"column:name"`
}

// FindReferencingProjects 返回两个用途中任一引用了该 Token 的项目（id+name，按创建时间倒序）。
// 删除 Token 前调用：非空即被引用，返回 ErrGitHubTokenInUse。
func (r *UserGitHubTokenRepository) FindReferencingProjects(tokenID, userID string) ([]ReferencingProject, error) {
	var projects []ReferencingProject
	err := r.db.Model(&model.Project{}).
		Select("id, name").
		Where("user_id = ? AND (public_github_token_id = ? OR code_github_token_id = ?)", userID, tokenID, tokenID).
		Order("created_at DESC").
		Scan(&projects).Error
	return projects, err
}

// FindReferencedByAny 批量统计一组 Token 各被多少项目引用（两个用途合计），
// 供列表页一次查询填齐 referenced_projects 计数与引用项目名。
func (r *UserGitHubTokenRepository) ListReferencingProjectsForTokens(tokenIDs []string, userID string) (map[string][]ReferencingProject, error) {
	result := make(map[string][]ReferencingProject, len(tokenIDs))
	if len(tokenIDs) == 0 {
		return result, nil
	}
	var rows []struct {
		TokenID string `gorm:"column:token_id"`
		ID      string `gorm:"column:id"`
		Name    string `gorm:"column:name"`
	}
	err := r.db.Model(&model.Project{}).
		Select("public_github_token_id AS token_id, id, name").
		Where("user_id = ? AND public_github_token_id IN ?", userID, tokenIDs).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	var codeRows []struct {
		TokenID string `gorm:"column:token_id"`
		ID      string `gorm:"column:id"`
		Name    string `gorm:"column:name"`
	}
	err = r.db.Model(&model.Project{}).
		Select("code_github_token_id AS token_id, id, name").
		Where("user_id = ? AND code_github_token_id IN ?", userID, tokenIDs).
		Scan(&codeRows).Error
	if err != nil {
		return nil, err
	}
	// 同一项目引用同一 Token 两个用途时去重，引用项目列表按项目唯一。
	seen := make(map[string]map[string]struct{}, len(tokenIDs))
	appendRow := func(tokenID, projectID, name string) {
		if result[tokenID] == nil {
			result[tokenID] = nil
			seen[tokenID] = make(map[string]struct{})
		}
		if _, dup := seen[tokenID][projectID]; dup {
			return
		}
		seen[tokenID][projectID] = struct{}{}
		result[tokenID] = append(result[tokenID], ReferencingProject{ID: projectID, Name: name})
	}
	for _, row := range rows {
		appendRow(row.TokenID, row.ID, row.Name)
	}
	for _, row := range codeRows {
		appendRow(row.TokenID, row.ID, row.Name)
	}
	return result, nil
}
