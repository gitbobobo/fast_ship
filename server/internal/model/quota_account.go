package model

import "time"

// QuotaAccount 是一个 AI 编码服务订阅账号（INT-109）：整组凭据的 AES-256-GCM
// 密文 + 脱敏尾号 + 最近一次成功抓取的快照原文与调度状态。
// 凭据不存明文指纹——允许同用户重复添加相同凭据；credential_hint 只含末 4 位。
// last_fetched_at / last_success_at 都只记最近一次「成功」抓取（= 快照数据时间），
// 失败不触碰；next_retry_at 是下一次计划抓取的绝对时间，成功后始终落值
// （避免 ListDue 把健康账号拖进失败账号的退避唤醒）；NULL 表示从未抓取过，
// 或凭据刚被替换而清空退避——两种情形 ListDue 都视为立即到期。
type QuotaAccount struct {
	ID                   string     `gorm:"type:text;primaryKey" json:"id"`
	UserID               string     `gorm:"type:text;not null;index" json:"user_id"`
	Provider             string     `gorm:"type:text;not null" json:"provider"`
	Label                string     `gorm:"type:text;not null" json:"label"`
	CredentialsEncrypted []byte     `gorm:"type:blob;not null" json:"-"`
	CredentialHint       string     `gorm:"type:text;not null" json:"credential_hint"`
	PlanName             string     `gorm:"type:text;not null" json:"plan_name"`
	Status               string     `gorm:"type:text;not null" json:"status"`
	LastError            string     `gorm:"type:text;not null" json:"last_error"`
	LastFetchedAt        *time.Time `json:"last_fetched_at"`
	LastSuccessAt        *time.Time `json:"last_success_at"`
	SnapshotJSON         string     `gorm:"type:text;not null" json:"-"`
	FailureCount         int        `gorm:"not null" json:"failure_count"`
	NextRetryAt          *time.Time `json:"next_retry_at"`
	CreatedAt            time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt            time.Time  `gorm:"not null" json:"updated_at"`

	User User `gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE" json:"-"`
}

func (QuotaAccount) TableName() string {
	return "quota_accounts"
}

// 抓取状态取值。credential_invalid 提示用户换凭据，provider_error 是接口异常；
// pending 表示已保存但还没有成功抓取过。
const (
	QuotaStatusOK                = "ok"
	QuotaStatusCredentialInvalid = "credential_invalid"
	QuotaStatusProviderError     = "provider_error"
	QuotaStatusPending           = "pending"
)
