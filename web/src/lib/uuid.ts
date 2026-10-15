/**
 * UUID v4 唯一入口。crypto.randomUUID 只在安全上下文（HTTPS / localhost）存在，
 * 局域网 HTTP 部署下为 undefined——退回 getRandomValues（非安全上下文可用），
 * 再退 Math.random，任何环境都返回合法 v4，不抛异常。
 * 业务代码不要直接碰 crypto.randomUUID：no-secure-context-crypto 规则会拦。
 */
export function uuid(): string {
  const direct = globalThis.crypto?.randomUUID?.();
  if (direct) return direct;

  const bytes = new Uint8Array(16);
  const cryptoRef = globalThis.crypto;
  if (cryptoRef?.getRandomValues) {
    cryptoRef.getRandomValues(bytes);
  } else {
    for (let i = 0; i < bytes.length; i++) {
      bytes[i] = Math.floor(Math.random() * 256);
    }
  }
  bytes[6] = (bytes[6] & 0x0f) | 0x40;
  bytes[8] = (bytes[8] & 0x3f) | 0x80;
  const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}
