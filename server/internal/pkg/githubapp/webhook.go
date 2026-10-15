package githubapp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// webhook 验签（INT-93）。GitHub 约定：`X-Hub-Signature-256: sha256=<hex>`，
// hex 是对原始请求体做 HMAC-SHA256(secret) 的结果。比较必须用常量时间
// （hmac.Equal），密钥为空时拒绝一切请求——不接受未签名流量。

// VerifySignature 校验 webhook 签名。secret 为空、头缺失、算法前缀不是
// sha256= 或摘要不匹配均返回 false。
func VerifySignature(secret string, body []byte, signatureHeader string) bool {
	if secret == "" || len(body) == 0 {
		return false
	}
	header := strings.TrimSpace(signatureHeader)
	const prefix = "sha256="
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return false
	}
	got, err := hex.DecodeString(header[len(prefix):])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), got)
}

// SignSignature 与 VerifySignature 对偶：生成 `sha256=<hex>` 头（模拟脚本与测试用）。
func SignSignature(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
