# Server Pkg / Crypto

AES 加解密工具。用于加密存储敏感数据（如 GitHub Token）。

## Public API

| Export | Type | Description |
|---|---|---|
| `Encrypt(key, plaintext)` | func | AES 加密，返回 base64 编码密文 |
| `Decrypt(key, ciphertext)` | func | AES 解密，接收 base64 编码密文 |

## Internal Structure

| File | Purpose |
|---|---|
| `server/internal/pkg/crypto/aes.go` | AES-GCM 加解密实现 |

## Dependencies

| Depends on | Why |
|---|---|
| `golang.org/x/crypto` | AES 加密算法 |

## Dependents

| Used by | How |
|---|---|
| server-service | 加密/解密 GitHub Token |

## Implementation Notes

- 使用 AES-GCM 模式，每次加密生成随机 Nonce，密文格式为 `Nonce + CipherText`
- 密钥通过配置文件 `encryption.key` 或环境变量 `ENCRYPTION_KEY` 提供
