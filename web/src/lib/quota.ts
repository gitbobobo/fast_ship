// 套餐额度（INT-109）的纯展示逻辑：用量分档配色与重置倒计时文案。

import { ApiRequestError } from "@/lib/api/client";

/** 服务端错误展示 ApiResponse.message（如 42900 的冷却说明），其余回退通用文案。 */
export function errorText(err: unknown, fallback: string): string {
  return err instanceof ApiRequestError && err.message ? err.message : fallback;
}

export type QuotaUsageTone = "normal" | "warning" | "danger";

/** used_percent >= 95 危险、>= 80 警告，其余正常；算不出百分比按正常显示。 */
export function quotaUsageTone(usedPercent: number | null): QuotaUsageTone {
  if (usedPercent == null) return "normal";
  if (usedPercent >= 95) return "danger";
  if (usedPercent >= 80) return "warning";
  return "normal";
}

/** 窗口数值的单位展示：count 显示原始数值，usd 带 $，percent 带 %。 */
export function formatQuotaAmount(value: number, unit: QuotaWindow["unit"]): string {
  const text = Number.isInteger(value) ? String(value) : value.toFixed(1);
  if (unit === "usd") return `$${text}`;
  if (unit === "percent") return `${text}%`;
  return text;
}

/**
 * 窗口重置倒计时（reset_at 是 UTC RFC3339）：
 * 「45 分钟后重置」「3 小时 12 分后重置」「2 天 5 小时后重置」；
 * 已过期显示「即将重置」（等下一轮抓取补上新的 reset_at）。
 */
export function formatResetCountdown(resetAt: string, now: number): string {
  const diffMs = new Date(resetAt).getTime() - now;
  if (Number.isNaN(diffMs) || diffMs <= 0) return "即将重置";

  const minutes = Math.floor(diffMs / 60_000);
  if (minutes < 1) return "即将重置";

  const days = Math.floor(minutes / (60 * 24));
  const hours = Math.floor((minutes % (60 * 24)) / 60);
  const mins = minutes % 60;

  if (days > 0) {
    return hours > 0 ? `${days} 天 ${hours} 小时后重置` : `${days} 天后重置`;
  }
  if (hours > 0) {
    return mins > 0 ? `${hours} 小时 ${mins} 分后重置` : `${hours} 小时后重置`;
  }
  return `${mins} 分钟后重置`;
}

/** 表单里还没填的必填凭据字段（values 是 field.key -> 输入值）。 */
export function missingRequiredCredentialFields(
  fields: QuotaCredentialField[],
  values: Record<string, string>,
): QuotaCredentialField[] {
  return fields.filter((f) => f.required && !(values[f.key] ?? "").trim());
}

/** 收集成请求体 credentials：去掉首尾空白，跳过未填的可选字段。 */
export function collectCredentialValues(
  fields: QuotaCredentialField[],
  values: Record<string, string>,
): Record<string, string> {
  const credentials: Record<string, string> = {};
  for (const field of fields) {
    const value = (values[field.key] ?? "").trim();
    if (value) credentials[field.key] = value;
  }
  return credentials;
}

/** 用户在编辑表单里是否动了任一凭据字段（全空 = 不修改凭据）。 */
export function hasAnyCredentialValue(
  fields: QuotaCredentialField[],
  values: Record<string, string>,
): boolean {
  return fields.some((f) => (values[f.key] ?? "").trim() !== "");
}

/**
 * 是否可发起凭据探测：选定供应商且必填凭据齐全。
 * 探测接口只认请求体里的凭据，编辑表单留空表示沿用的场景不能放宽。
 */
export function canProbe(
  provider: QuotaProvider | undefined,
  values: Record<string, string>,
): boolean {
  if (!provider) return false;
  return (
    missingRequiredCredentialFields(provider.credential_fields, values)
      .length === 0
  );
}
