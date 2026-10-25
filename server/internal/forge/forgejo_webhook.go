package forge

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// VerifyForgejoSignature Forgejo webhook 入站验签（INT-167 任务 3）：对 raw
// body 做 HMAC-SHA256（key = 集成 webhook secret），与 X-Forgejo-Signature 头
// 比较。该头是纯 hex 摘要，没有 GitHub 的 sha256= 前缀——带任何前缀（含
// sha256=）的值都不是合法 hex，一律按签名无效处理；hex 大小写不敏感，比较经
// hmac.Equal 常量时间完成。
func VerifyForgejoSignature(secret string, body []byte, signatureHeader string) bool {
	if secret == "" || len(body) == 0 {
		return false
	}
	got, err := hex.DecodeString(strings.TrimSpace(signatureHeader))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), got)
}

// SignForgejoSignature 与 VerifyForgejoSignature 对偶：生成纯 hex 头（模拟
// 脚本与测试用）。
func SignForgejoSignature(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
