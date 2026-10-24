package model

import "time"

type Project struct {
	ID          string `gorm:"type:text;primaryKey" json:"id"`
	UserID      string `gorm:"type:text;not null;index" json:"user_id"`
	Name        string `gorm:"type:text;not null" json:"name"`
	Description string `gorm:"type:text" json:"description"`
	GithubOwner string `gorm:"type:text" json:"github_owner"`
	GithubRepo  string `gorm:"type:text" json:"github_repo"`
	// CodeGithubOwner/CodeGithubRepo 代码仓库（PR 审查用）：未单独配置时为空，
	// 审查经 CodeRepo() 回落到公开仓库；与公开仓库（Issue 同步/发版）互不影响。
	CodeGithubOwner     string `gorm:"column:code_github_owner;type:text" json:"code_github_owner"`
	CodeGithubRepo      string `gorm:"column:code_github_repo;type:text" json:"code_github_repo"`
	PublicGitHubTokenID string `gorm:"column:public_github_token_id;type:text" json:"-"`
	CodeGitHubTokenID   string `gorm:"column:code_github_token_id;type:text" json:"-"`
	// 代码集成引用（INT-166）：可空，NULL/空串 = 内置 github-default（现有
	// GitHub App 链路）。写入接线由后续任务完成，本期只随响应带出。
	PublicIntegrationID string `gorm:"column:public_integration_id;type:text" json:"-"`
	CodeIntegrationID   string `gorm:"column:code_integration_id;type:text" json:"-"`
	// 搁置（INT-165）：两列可空，存量行为 NULL。「搁置中」的判定见 IsShelved；
	// 到期自动恢复没有定时任务，由读取侧按 now 判定，SQL 过滤处用等价条件。
	ShelvedAt    *time.Time `gorm:"type:timestamp" json:"shelved_at"`
	ShelvedUntil *time.Time `gorm:"type:timestamp" json:"shelved_until"`
	CreatedAt    time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt    time.Time  `gorm:"not null" json:"updated_at"`

	User User `gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE" json:"-"`
}

func (Project) TableName() string {
	return "projects"
}

// IsGitHubConfigured 检查项目是否已关联 GitHub 仓库（owner、repo 均非空）。
// 仓库已关联但未配公开仓库 Token 时，公开仓库操作单独按 PublicGitHubTokenID 判定。
// 两个引用列不建外键（避免删用户时 RESTRICT 与 CASCADE 冲突），引用一致性由应用层保证。
func (p *Project) IsGitHubConfigured() bool {
	return p.GithubOwner != "" && p.GithubRepo != ""
}

// IsShelved 判断项目在给定时刻是否处于搁置中：shelved_at 非空且（无截止即
// 无限期，或截止晚于 now）。到期自动恢复没有定时任务，边界落在调用时刻——
// 截止恰好等于 now 视为已到期。repository 层的 SQL 过滤与本判定同口径，
// 两处必须同步修改。
func (p *Project) IsShelved(now time.Time) bool {
	if p.ShelvedAt == nil {
		return false
	}
	return p.ShelvedUntil == nil || p.ShelvedUntil.After(now)
}

// CodeRepo 返回 PR 审查仓库的候选值：两个 code 字段都非空时用显式配置，
// 否则回落公开仓库。它是「这个项目会审查哪个仓库」的唯一来源——写入审查
// 设置时用它生成 project_review_settings.repo_full_name 快照。
//
// 运行侧判定（触发反查、同步过滤、rerun、记录列表）读的是设置行的快照：
// 同仓库唯一索引就建在那一列上，判定与约束必须用同一列。两者的读取口径见
// docs/internal/reviews.md「审查仓库的读取口径」，并由
// service.TestReviewChainReadsReviewRepoFromSettingsSnapshot 机器校验。
func (p *Project) CodeRepo() (owner, repo string) {
	if p.CodeGithubOwner != "" && p.CodeGithubRepo != "" {
		return p.CodeGithubOwner, p.CodeGithubRepo
	}
	return p.GithubOwner, p.GithubRepo
}

// CodeRepoFullName 返回 "owner/repo" 形式的审查仓库（未配置 code 字段即公开仓库）。
func (p *Project) CodeRepoFullName() string {
	owner, repo := p.CodeRepo()
	return owner + "/" + repo
}

// UniqueIndex: user_id + name
// 通过 GORM 的 CreateIndex 或 AutoMigrate 时手动添加
