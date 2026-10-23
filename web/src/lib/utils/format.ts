export function formatFileSize(bytes: number): string {
  if (bytes === 0) return "0 B";
  const units = ["B", "KB", "MB", "GB"];
  const i = Math.floor(Math.log(bytes) / Math.log(1024));
  return `${(bytes / Math.pow(1024, i)).toFixed(i > 0 ? 1 : 0)} ${units[i]}`;
}

export function formatDate(dateStr: string): string {
  return new Date(dateStr).toLocaleDateString("zh-CN", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  });
}

export function shortSha(sha: string): string {
  return sha.length > 7 ? sha.slice(0, 7) : sha;
}

/**
 * 已用时长（如「3 分 5 秒」「1 小时 2 分」）。now 参数便于测试固定时间。
 */
export function formatElapsed(startIso: string, now: number): string {
  const seconds = Math.max(
    0,
    Math.floor((now - new Date(startIso).getTime()) / 1000),
  );
  if (seconds < 60) return `${seconds} 秒`;
  const minutes = Math.floor(seconds / 60);
  const restSeconds = seconds % 60;
  if (minutes < 60) {
    return restSeconds ? `${minutes} 分 ${restSeconds} 秒` : `${minutes} 分`;
  }
  const hours = Math.floor(minutes / 60);
  const restMinutes = minutes % 60;
  if (hours < 24) {
    return restMinutes ? `${hours} 小时 ${restMinutes} 分` : `${hours} 小时`;
  }
  const days = Math.floor(hours / 24);
  const restHours = hours % 24;
  return restHours ? `${days} 天 ${restHours} 小时` : `${days} 天`;
}

/** 防抖剩余秒数（向上取整，负值归 0）。 */
export function debounceRemainingSeconds(
  fireAtIso: string,
  now: number,
): number {
  return Math.max(
    0,
    Math.ceil((new Date(fireAtIso).getTime() - now) / 1000),
  );
}

export function formatRelativeTime(dateStr: string): string {
  const now = Date.now();
  const diff = now - new Date(dateStr).getTime();
  const minutes = Math.floor(diff / 60000);
  if (minutes < 1) return "刚刚";
  if (minutes < 60) return `${minutes} 分钟前`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours} 小时前`;
  const days = Math.floor(hours / 24);
  if (days < 30) return `${days} 天前`;
  return formatDate(dateStr);
}

/**
 * 未来时间的相对表达（如「30 天后」，限时授权到期用）；已过时间点回退
 * formatRelativeTime（「3 小时前」），超过 30 天用绝对日期。
 */
export function formatRelativeFuture(dateStr: string): string {
  const diff = new Date(dateStr).getTime() - Date.now();
  if (diff <= 0) return formatRelativeTime(dateStr);
  const minutes = Math.floor(diff / 60000);
  if (minutes < 1) return "即将";
  if (minutes < 60) return `${minutes} 分钟后`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours} 小时后`;
  const days = Math.floor(hours / 24);
  if (days < 30) return `${days} 天后`;
  return formatDate(dateStr);
}

/**
 * 到期时间是否已过（限时授权等）；dateStr 缺省视为未过期。
 * 放在 lib 里供组件渲染期调用——组件内直接 Date.now 会被 react-hooks/purity 拦下。
 */
export function isExpiredDate(dateStr?: string | null): boolean {
  if (!dateStr) return false;
  const when = new Date(dateStr).getTime();
  return !Number.isNaN(when) && when <= Date.now();
}

/**
 * 过期态展示的统一判定：单次 Date.now 同时给出 expired 与相对剩余时间，
 * 避免 isExpiredDate 与 formatRelativeFuture 各取一次 now 在过期边界上
 * 一个判 true、一个回退成「刚刚」的不一致行（授权列表曾出现两条同为
 * 过期的授权展示不一致）。
 */
export function expiryDisplay(dateStr?: string | null): {
  expired: boolean;
  /** 未过期时的相对倒计时文案（如「3 小时后」）；过期或缺省为空串。 */
  label: string;
} {
  if (!dateStr) return { expired: false, label: "" };
  const when = new Date(dateStr).getTime();
  if (Number.isNaN(when)) return { expired: false, label: "" };
  const now = Date.now();
  if (when <= now) return { expired: true, label: "" };
  const minutes = Math.floor((when - now) / 60000);
  if (minutes < 1) return { expired: false, label: "即将" };
  if (minutes < 60) return { expired: false, label: `${minutes} 分钟后` };
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return { expired: false, label: `${hours} 小时后` };
  const days = Math.floor(hours / 24);
  if (days < 30) return { expired: false, label: `${days} 天后` };
  return { expired: false, label: formatDate(dateStr) };
}
