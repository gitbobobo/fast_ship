/** 登录成功后允许回跳的地址：只接受站内绝对路径。 */
export function safeNextPath(raw: string | null | undefined): string | null {
  // `//evil.example.com/x` 是协议相对 URL，`https://…` 是外站地址：都当没有 next。
  if (!raw || !raw.startsWith("/") || raw.startsWith("//")) return null;
  return raw;
}

/** 没有（或不允许）next 时的落点。 */
export const DEFAULT_POST_LOGIN_PATH = "/dashboard";
