package model

import "time"

// ProjectT3Binding 记录 Fast Ship 项目在某个 T3 环境中绑定的 T3 项目（INT-89）。
//
// 绑定是显式关系：用户（或经 Agent 辅助）选定 T3 项目后落库，不做路径推断。
// 主键 (project_id, environment_id)：每个环境至多绑一个 T3 项目；同一 Fast Ship
// 项目可跨多个环境各绑一个（例如公司机与家用机各绑同名仓库）。
// T3ProjectTitle/WorkspaceRoot 是绑定成功那一刻的快照：环境不可达时列表仍能
// 展示绑定内容；快照与最新发现不一致（T3 项目被删/改名/挪路径）即失效绑定。
type ProjectT3Binding struct {
	ProjectID      string    `gorm:"type:text;primaryKey" json:"project_id"`
	EnvironmentID  string    `gorm:"type:text;primaryKey" json:"environment_id"`
	T3ProjectID    string    `gorm:"type:text;not null" json:"t3_project_id"`
	T3ProjectTitle string    `gorm:"type:text;not null" json:"t3_project_title"`
	WorkspaceRoot  string    `gorm:"type:text;not null" json:"workspace_root"`
	CreatedAt      time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt      time.Time `gorm:"not null" json:"updated_at"`

	Project     Project       `gorm:"foreignKey:ProjectID;constraint:OnDelete:CASCADE" json:"-"`
	Environment T3Environment `gorm:"foreignKey:EnvironmentID;constraint:OnDelete:CASCADE" json:"-"`
}

func (ProjectT3Binding) TableName() string {
	return "project_t3_bindings"
}
