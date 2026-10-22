// 用户时区（INT-138）：浏览器时区探测、首登自动写入、设置页的候选列表与
// 「该时区下的当前时间」格式化。时区的权威值在用户资料上（空串 = 未声明，
// 机器人按部署配置回退），前端只负责探测与展示。
import { authApi } from "@/lib/api/auth";

/** 浏览器 IANA 时区名（如 Asia/Shanghai）；取不到时返回空串，不抛错。 */
export function browserTimezone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "";
  } catch {
    return "";
  }
}

/**
 * 首登把浏览器时区写进用户资料：用户已声明时区时原样返回，否则调
 * PUT /api/auth/me 只写 timezone 并返回更新后的 user。失败静默返回原 user
 * ——这是登录后的顺手补齐，不该用错误 toast 打断跳转。
 */
export async function syncBrowserTimezone(user: User): Promise<User> {
  if (user.timezone) return user;
  const timezone = browserTimezone();
  if (!timezone) return user;
  try {
    const res = await authApi.updateMe({ timezone });
    return res.data;
  } catch {
    return user;
  }
}

/** Intl.supportedValuesOf 不可用（老浏览器 / 精简 ICU）时的常用时区兜底。 */
const FALLBACK_TIMEZONES = [
  "UTC",
  "Asia/Shanghai",
  "Asia/Tokyo",
  "Asia/Singapore",
  "Europe/London",
  "Europe/Berlin",
  "America/New_York",
  "America/Los_Angeles",
];

/**
 * 常用时区：固定排在候选列表最前，省掉在 400 多项里滚动。本机时区与当前值
 * 另外插到更前面。
 */
const COMMON_TIMEZONES = [
  "UTC",
  "Asia/Shanghai",
  "Asia/Tokyo",
  "Asia/Singapore",
  "Asia/Seoul",
  "Europe/London",
  "Europe/Berlin",
  "Europe/Paris",
  "America/New_York",
  "America/Los_Angeles",
];

/**
 * IANA 的历史别名（backward-compat links）：与规范名称解析结果相同，列在下拉
 * 里只是干扰项。当前值本身若落在这里会被单独插回，不会因此选不中。
 */
const LEGACY_TIMEZONE_ALIASES = new Set([
  "Asia/Saigon",
  "Asia/Rangoon",
  "Asia/Calcutta",
  "Asia/Katmandu",
  "Asia/Chongqing",
  "Asia/Harbin",
  "Asia/Kashgar",
  "Asia/Macao",
  "Asia/Tel_Aviv",
  "Asia/Ulan_Bator",
  "Europe/Kiev",
  "America/Godthab",
  "Pacific/Ponape",
  "Pacific/Truk",
  "Pacific/Samoa",
  "Africa/Asmera",
  "Atlantic/Faeroe",
  "Australia/Canberra",
  "US/Eastern",
  "US/Central",
  "US/Mountain",
  "US/Pacific",
]);

// 候选列表一次可达数百项，逐个构造 Intl 太贵：按名称缓存偏移标签。
const offsetLabelCache = new Map<string, string>();

/**
 * 时区当前的 UTC 偏移标签（如 UTC+8）；Intl 不支持 shortOffset 或名称非法时
 * 返回空串，调用方只显示名称。
 */
function offsetLabel(timeZone: string): string {
  const cached = offsetLabelCache.get(timeZone);
  if (cached !== undefined) return cached;
  const label = readOffsetLabel(timeZone);
  offsetLabelCache.set(timeZone, label);
  return label;
}

/** 实际读一次偏移；名称非法或环境不支持 shortOffset 时返回空串。 */
function readOffsetLabel(timeZone: string): string {
  try {
    const parts = new Intl.DateTimeFormat("en-US", {
      timeZone,
      timeZoneName: "shortOffset",
    }).formatToParts(new Date());
    // ICU 给的是 GMT+8，统一成 UTC+8：用户一眼能对上「每天 9 点」的偏移。
    return (parts.find((p) => p.type === "timeZoneName")?.value ?? "").replace(
      /^GMT/,
      "UTC",
    );
  } catch {
    return "";
  }
}

/** 下拉项文案：`Asia/Shanghai (UTC+8)`；拼不出偏移时只有名称。 */
function timezoneLabel(timeZone: string): string {
  const offset = offsetLabel(timeZone);
  return offset ? `${timeZone} (${offset})` : timeZone;
}

/**
 * 时区候选列表（设置页下拉用）：优先 Intl.supportedValuesOf 的全量 IANA 列表，
 * 不可用时退回内置常用列表；当前值不在候选里且非空时插到首位，保证它可选中、
 * 收起态也能显示。
 */
export function timezoneOptions(
  current: string,
): { value: string; label: string }[] {
  const intl = Intl as typeof Intl & {
    supportedValuesOf?: (key: string) => string[];
  };
  const names =
    typeof intl.supportedValuesOf === "function"
      ? intl.supportedValuesOf("timeZone")
      : FALLBACK_TIMEZONES;
  // 顺序：当前值 → 本机时区 → 常用时区 → 其余（保持 IANA 的字母序）。
  // supportedValuesOf 不含 UTC（它只列地区时区），但「跟随部署配置」不等于
  // UTC，缺了这项用户就选不到 UTC，所以固定补在最前。
  const head = [current, browserTimezone(), ...COMMON_TIMEZONES, "UTC"].filter(
    (name): name is string => Boolean(name),
  );
  const values: string[] = [];
  for (const name of head) {
    if (!values.includes(name)) values.push(name);
  }
  for (const name of names) {
    if (LEGACY_TIMEZONE_ALIASES.has(name) || values.includes(name)) continue;
    values.push(name);
  }
  return values.map((value) => ({ value, label: timezoneLabel(value) }));
}

/**
 * 某时刻在指定时区的本地时间文本，如 `2026-10-22 13:05 (UTC+8)`。
 * timeZone 为空时用浏览器时区（用户未声明时区时的展示口径）。
 */
export function formatTimeInZone(date: Date, timeZone: string): string {
  const zone = timeZone || browserTimezone() || "UTC";
  let clock: string;
  try {
    const parts = new Intl.DateTimeFormat("en-US", {
      timeZone: zone,
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
      hour: "2-digit",
      minute: "2-digit",
      hourCycle: "h23",
    }).formatToParts(date);
    const get = (type: string) =>
      parts.find((p) => p.type === type)?.value ?? "";
    clock = `${get("year")}-${get("month")}-${get("day")} ${get("hour")}:${get("minute")}`;
  } catch {
    return date.toLocaleString();
  }
  const offset = offsetLabel(zone);
  return offset ? `${clock} (${offset})` : clock;
}
