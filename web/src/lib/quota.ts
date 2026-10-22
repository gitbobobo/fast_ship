// 套餐额度（INT-109/INT-130）的纯展示逻辑：
// 有效剩余、额度池分组、账号三段分组与排序、用量分档配色与重置/恢复倒计时文案。

// errorText 的实现在 api/client.ts（ApiRequestError 旁边），这里沿用既有导出入口。
export { errorText } from "@/lib/api/client";

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
 * 窗口用量明细（悬浮详情面板用）：「9.7 / 10」；只有 limit 时显示
 * 「限额 10」；used 单独存在无法说明占比，与行内版一致不展示，返回 null。
 */
export function quotaWindowUsageText(w: QuotaWindow): string | null {
  if (w.used != null && w.limit != null) {
    return `${formatQuotaAmount(w.used, w.unit)} / ${formatQuotaAmount(w.limit, w.unit)}`;
  }
  if (w.limit != null) return `限额 ${formatQuotaAmount(w.limit, w.unit)}`;
  return null;
}

function formatCountdown(resetAt: string, now: number, verb: string): string {
  const diffMs = new Date(resetAt).getTime() - now;
  if (Number.isNaN(diffMs) || diffMs <= 0) return `即将${verb}`;

  const minutes = Math.floor(diffMs / 60_000);
  if (minutes < 1) return `即将${verb}`;

  const days = Math.floor(minutes / (60 * 24));
  const hours = Math.floor((minutes % (60 * 24)) / 60);
  const mins = minutes % 60;

  if (days > 0) {
    return hours > 0
      ? `${days} 天 ${hours} 小时后${verb}`
      : `${days} 天后${verb}`;
  }
  if (hours > 0) {
    return mins > 0
      ? `${hours} 小时 ${mins} 分后${verb}`
      : `${hours} 小时后${verb}`;
  }
  return `${mins} 分钟后${verb}`;
}

/**
 * 窗口重置倒计时（reset_at 是 UTC RFC3339）：
 * 「45 分钟后重置」「3 小时 12 分后重置」「2 天 5 小时后重置」；
 * 已过期显示「即将重置」（等下一轮抓取补上新的 reset_at）。
 */
export function formatResetCountdown(resetAt: string, now: number): string {
  return formatCountdown(resetAt, now, "重置");
}

/** 已用尽账号的恢复倒计时：与重置同一粒度，结尾动词换「恢复」。 */
export function formatRecoveryCountdown(resetAt: string, now: number): string {
  return formatCountdown(resetAt, now, "恢复");
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

/* ------------------------- INT-130：额度池与账号分组 ------------------------- */

/**
 * 一个额度池的展示汇总：name 为 null 表示 QuotaWindow.pool 缺省
 * （绝大多数供应商只有一个池，前端不显示池 tab）。
 */
export interface QuotaPool {
  name: string | null;
  windows: QuotaWindow[];
  /** 池内 used_percent 最大的窗口（受限窗口），决定主数字与加粗行；全为 null 时为 null */
  limiting: QuotaWindow | null;
  /** 有效剩余 = 100 − max(used_percent)，下限 0；算不出为 null */
  remaining: number | null;
  /** remaining === 0 */
  exhausted: boolean;
}

function summarizePool(name: string | null, windows: QuotaWindow[]): QuotaPool {
  let limiting: QuotaWindow | null = null;
  for (const w of windows) {
    if (w.used_percent == null) continue;
    if (limiting == null || w.used_percent > (limiting.used_percent ?? -1)) {
      limiting = w;
    }
  }
  const remaining =
    limiting?.used_percent == null
      ? null
      : Math.max(0, 100 - limiting.used_percent);
  return { name, windows, limiting, remaining, exhausted: remaining === 0 };
}

/**
 * 按窗口顺序把 windows 归并成额度池：同名相邻/分散都合并，
 * 池顺序按首次出现保持；无 pool 字段的窗口归入唯一 name=null 的池。
 */
export function quotaPools(windows: QuotaWindow[]): QuotaPool[] {
  const order: (string | null)[] = [];
  const byName = new Map<string | null, QuotaWindow[]>();
  for (const w of windows) {
    // 契约上空串 = 无需分组（服务端 omitempty 使其通常整个缺省）
    const name = w.pool || null;
    let bucket = byName.get(name);
    if (!bucket) {
      bucket = [];
      byName.set(name, bucket);
      order.push(name);
    }
    bucket.push(w);
  }
  return order.map((name) => summarizePool(name, byName.get(name) ?? []));
}

/** 默认选中的池：第一个未用尽（= 正在消耗）；全用尽时回退第一个池。 */
export function defaultQuotaPool(pools: QuotaPool[]): QuotaPool | null {
  return pools.find((pool) => !pool.exhausted) ?? pools[0] ?? null;
}

/** 账号的卡片主数字 = 默认池的有效剩余（未用尽池优先）。 */
export function accountRemaining(account: QuotaAccount): number | null {
  return defaultQuotaPool(quotaPools(account.windows))?.remaining ?? null;
}

export type QuotaAccountGroup = "attention" | "usable" | "exhausted";

export function quotaAccountGroup(account: QuotaAccount): QuotaAccountGroup {
  if (
    account.status === "credential_invalid" ||
    account.status === "provider_error"
  ) {
    return "attention";
  }
  const pools = quotaPools(account.windows);
  if (pools.length > 0 && pools.every((pool) => pool.exhausted)) {
    return "exhausted";
  }
  return "usable";
}

/**
 * 池的恢复时间：池内所有用尽窗口（used_percent >= 100，这些窗口全重置后
 * 池才不再用尽）里最晚的 reset_at；任一用尽窗口缺 reset_at 或无法解析时
 * 该池算不出恢复时间，返回 null。未用尽的池不参与，同样返回 null。
 */
function poolRecoveryAt(pool: QuotaPool): string | null {
  let latest: string | null = null;
  let latestMs = -Infinity;
  for (const w of pool.windows) {
    if ((w.used_percent ?? 0) < 100) continue;
    const resetAt = w.reset_at ?? null;
    const ms = resetAt ? new Date(resetAt).getTime() : NaN;
    if (Number.isNaN(ms)) return null;
    if (ms > latestMs) {
      latestMs = ms;
      latest = resetAt;
    }
  }
  return latest;
}

/**
 * 已用尽账号的恢复时间 = 各池恢复时间里最早的一个
 * （任一池先恢复账号就能继续用）；所有池都算不出时为 null，
 * 界面显示「恢复时间未知」。
 */
export function accountRecoveryAt(account: QuotaAccount): string | null {
  let earliest: string | null = null;
  let earliestMs = Infinity;
  for (const pool of quotaPools(account.windows)) {
    const at = poolRecoveryAt(pool);
    if (at == null) continue;
    const ms = new Date(at).getTime();
    if (ms < earliestMs) {
      earliest = at;
      earliestMs = ms;
    }
  }
  return earliest;
}

/** 可用段排序：有效剩余降序；算不出剩余（未抓取/窗口无百分比）排最后。 */
export function sortUsableAccounts(accounts: QuotaAccount[]): QuotaAccount[] {
  return [...accounts].sort((a, b) => {
    const ra = accountRemaining(a);
    const rb = accountRemaining(b);
    if (ra == null && rb == null) return 0;
    if (ra == null) return 1;
    if (rb == null) return -1;
    return rb - ra;
  });
}

/** 已用尽段排序：恢复时间升序；恢复时间未知的排最后。 */
export function sortExhaustedAccounts(accounts: QuotaAccount[]): QuotaAccount[] {
  return [...accounts].sort((a, b) => {
    const ta = accountRecoveryAt(a);
    const tb = accountRecoveryAt(b);
    if (ta == null && tb == null) return 0;
    if (ta == null) return 1;
    if (tb == null) return -1;
    // 后端是 RFC3339Nano（可带小数秒），字符串序不等于时间序，比较时间戳
    return new Date(ta).getTime() - new Date(tb).getTime();
  });
}
