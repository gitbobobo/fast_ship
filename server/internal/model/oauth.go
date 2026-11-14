package model

import "time"

// OAuth 2.1 授权服务（INT-222）的数据模型。全部面向公开客户端（PKCE 强制），
// 不存任何 client_secret；access/refresh/code 令牌一律只落 SHA-256 哈希。

// OAuthClient 动态注册（RFC 7591）的公开客户端。client_id 是服务端生成的
// 随机串，同时充当主键；redirect_uris 是 JSON 数组文本（注册时校验过的
// https 或回环地址）。
type OAuthClient struct {
	ClientID        string    `gorm:"type:text;primaryKey;column:client_id" json:"client_id"`
	ClientName      string    `gorm:"type:text;not null" json:"client_name"`
	RedirectURIs    string    `gorm:"type:text;not null" json:"redirect_uris"`
	CreatedByUserID string    `gorm:"type:text;index" json:"created_by_user_id"`
	CreatedAt       time.Time `gorm:"not null" json:"created_at"`
}

// OAuthAuthorization 用户对客户端的授权总账：一个 (user, client) 一行，
// 重授权覆盖 scopes。吊销授权时置 revoked_at 并同时吊销名下全部令牌。
// 客户端名展示走 ListAuthorizations 的 join，不建 gorm 关联（client_id 是
// 非主键唯一键，关联方向易被误建外键）。
type OAuthAuthorization struct {
	ID        string     `gorm:"type:text;primaryKey" json:"id"`
	UserID    string     `gorm:"type:text;not null;uniqueIndex:uniq_oauth_authorizations_user_client" json:"user_id"`
	ClientID  string     `gorm:"type:text;not null;uniqueIndex:uniq_oauth_authorizations_user_client" json:"client_id"`
	Scopes    string     `gorm:"type:text;not null" json:"scopes"`
	CreatedAt time.Time  `gorm:"not null" json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at"`
}

// OAuthAuthorizationCode 授权码（10 分钟有效、单次使用）：只存哈希；
// used_at 原子置位即消费，重放走条件更新栅栏拒绝。
type OAuthAuthorizationCode struct {
	ID              string     `gorm:"type:text;primaryKey" json:"id"`
	CodeHash        string     `gorm:"type:text;not null;uniqueIndex" json:"code_hash"`
	UserID          string     `gorm:"type:text;not null;index" json:"user_id"`
	ClientID        string     `gorm:"type:text;not null;index" json:"client_id"`
	AuthorizationID string     `gorm:"type:text;not null" json:"authorization_id"`
	RedirectURI     string     `gorm:"type:text;not null" json:"redirect_uri"`
	Scope           string     `gorm:"type:text;not null" json:"scope"`
	CodeChallenge   string     `gorm:"type:text;not null" json:"code_challenge"`
	ExpiresAt       time.Time  `gorm:"not null" json:"expires_at"`
	UsedAt          *time.Time `json:"used_at"`
	CreatedAt       time.Time  `gorm:"not null" json:"created_at"`
}

// OAuthAccessToken MCP 资源访问令牌（fsm_ 前缀，1 小时）：token_version 是
// 签发时用户 token_version 的快照，改密后经版本比对立即失效。
type OAuthAccessToken struct {
	ID              string     `gorm:"type:text;primaryKey" json:"id"`
	TokenHash       string     `gorm:"type:text;not null;uniqueIndex" json:"token_hash"`
	UserID          string     `gorm:"type:text;not null;index" json:"user_id"`
	ClientID        string     `gorm:"type:text;not null;index" json:"client_id"`
	AuthorizationID string     `gorm:"type:text;not null;index" json:"authorization_id"`
	Scope           string     `gorm:"type:text;not null" json:"scope"`
	TokenVersion    int        `gorm:"not null" json:"token_version"`
	ExpiresAt       time.Time  `gorm:"not null" json:"expires_at"`
	RevokedAt       *time.Time `json:"revoked_at"`
	CreatedAt       time.Time  `gorm:"not null" json:"created_at"`
}

// OAuthRefreshToken OAuth refresh 令牌（fsn_ 前缀，30 天）。轮换 = 条件吊销
// 旧行 + 事务内插入新行（RowsAffected 栅栏），没有后继指针列。
type OAuthRefreshToken struct {
	ID              string     `gorm:"type:text;primaryKey" json:"id"`
	TokenHash       string     `gorm:"type:text;not null;uniqueIndex" json:"token_hash"`
	UserID          string     `gorm:"type:text;not null;index" json:"user_id"`
	ClientID        string     `gorm:"type:text;not null;index" json:"client_id"`
	AuthorizationID string     `gorm:"type:text;not null;index" json:"authorization_id"`
	Scope           string     `gorm:"type:text;not null" json:"scope"`
	TokenVersion    int        `gorm:"not null" json:"token_version"`
	ExpiresAt       time.Time  `gorm:"not null" json:"expires_at"`
	RevokedAt       *time.Time `json:"revoked_at"`
	CreatedAt       time.Time  `gorm:"not null" json:"created_at"`
}

// TableName 固定表名：gorm 默认会把 OAuth 拆成 o_auth_*，这里统一为
// oauth_*，与发现文档、日志检索语义一致。
func (OAuthClient) TableName() string            { return "oauth_clients" }
func (OAuthAuthorization) TableName() string     { return "oauth_authorizations" }
func (OAuthAuthorizationCode) TableName() string { return "oauth_authorization_codes" }
func (OAuthAccessToken) TableName() string       { return "oauth_access_tokens" }
func (OAuthRefreshToken) TableName() string      { return "oauth_refresh_tokens" }
