import cronstrue from "cronstrue/i18n.js";

import { effectiveTimezone, parseCronExpression } from "@/lib/cron-schedule";
import type { CronError } from "@/lib/cron-schedule";

// 可视化频率层（INT-100）：把「定时」拆成频率选项 + cron 表达式高级模式。
// 服务端只存五字段表达式，本层只做生成、还原、中文/英文描述与错误文案。

export type FrequencyKind =
  | "minutes" // 每隔 N 分钟
  | "hours" // 每隔 N 小时
  | "daily" // 每天
  | "weekly" // 每周（多选星期）
  | "monthly" // 每月（第几天 / 月末）
  | "weekdays" // 工作日
  | "custom"; // 自定义 cron 表达式

export interface Frequency {
  kind: FrequencyKind;
  /** minutes：能整除 60 的间隔。 */
  everyMinutes: number;
  /** hours：能整除 24 的间隔。 */
  everyHours: number;
  /** daily/weekly/monthly/weekdays 的触发时间 "HH:mm"。 */
  time: string;
  /** weekly：选中的星期（0=周日 … 6=周六）。 */
  weekdays: number[];
  /** monthly：每月第几天（1–31）。 */
  dayOfMonth: number;
}

export const MINUTE_STEP_OPTIONS = [1, 2, 5, 10, 15, 20, 30] as const;
export const HOUR_STEP_OPTIONS = [1, 2, 3, 4, 6, 8, 12] as const;

export const defaultFrequency = (): Frequency => ({
  kind: "daily",
  everyMinutes: 10,
  everyHours: 1,
  time: "09:00",
  weekdays: [1],
  dayOfMonth: 1,
});

const TIME_RE = /^(\d{1,2}):(\d{2})$/;

/** 频率 → 五字段表达式；无法生成（如每周未选日、时间非法）返回 null。 */
export function cronFromFrequency(f: Frequency): string | null {
  const needsTime =
    f.kind === "daily" ||
    f.kind === "weekly" ||
    f.kind === "monthly" ||
    f.kind === "weekdays";
  const time = TIME_RE.exec(f.time ?? "");
  const hour = time ? Number(time[1]) : NaN;
  const minute = time ? Number(time[2]) : NaN;
  if (needsTime && (!time || hour > 23 || minute > 59)) return null;
  const m = time ? String(minute) : "0";
  const h = time ? String(hour) : "9";
  switch (f.kind) {
    case "minutes":
      return f.everyMinutes === 1
        ? "* * * * *"
        : `*/${f.everyMinutes} * * * *`;
    case "hours":
      return f.everyHours === 1
        ? "0 * * * *"
        : `0 */${f.everyHours} * * *`;
    case "daily":
      return `${m} ${h} * * *`;
    case "weekly": {
      const days = [...new Set(f.weekdays)].filter((d) => d >= 0 && d <= 6);
      if (days.length === 0) return null;
      return `${m} ${h} * * ${days.sort((a, b) => a - b).join(",")}`;
    }
    case "monthly":
      return `${m} ${h} ${f.dayOfMonth} * *`;
    case "weekdays":
      return `${m} ${h} * * 1-5`;
    case "custom":
      return null;
  }
}

const isStar = (s: string) => s === "*" || s === "?";
const isInt = (s: string) => /^\d+$/.test(s);
/** 与「星号步进」等价的写法：星号/N、0/N（robfig 展开为 0-max）、0-max/N。 */
const stepMatch = (s: string, max: number) => {
  const m = /^(\*|0|0-\d+)\/(\d+)$/.exec(s);
  if (!m) return null;
  if (m[1] !== "*" && m[1] !== "0" && m[1] !== `0-${max}`) return null;
  return Number(m[2]);
};

/**
 * 表达式 → 频率还原。只能还原可视化层能生成的形态（允许 * 与 ? 互换、
 * 星号步进与 N-max 等价的整除步进）；还原不了返回 null（落在自定义）。
 */
export function frequencyFromCron(expression: string): Frequency | null {
  const fields = expression.trim().split(/\s+/);
  if (fields.length !== 5) return null;
  const [mi, ho, dom, mo, dow] = fields;
  if (!isStar(mo)) return null;
  const base = defaultFrequency();

  if (isStar(dom) && isStar(dow)) {
    // 时字段放开：分钟级（*/N）或整点每小时（0 *）
    if (isStar(ho)) {
      if (isStar(mi)) return { ...base, kind: "minutes", everyMinutes: 1 };
      const s = stepMatch(mi, 59);
      if (s !== null && (MINUTE_STEP_OPTIONS as readonly number[]).includes(s)) {
        return { ...base, kind: "minutes", everyMinutes: s };
      }
      // "0 * * * *" = 每小时整点；"5 * * * *" 等不在可视化范围 → null
      return mi === "0" ? { ...base, kind: "hours", everyHours: 1 } : null;
    }
    // 每隔 N 小时：0 */N * * *
    const s = stepMatch(ho, 23);
    if (
      mi === "0" &&
      s !== null &&
      (HOUR_STEP_OPTIONS as readonly number[]).includes(s)
    ) {
      return { ...base, kind: "hours", everyHours: s };
    }
  }

  // 以下都需要「分 时」为单个整数
  if (!isInt(mi) || !isInt(ho)) return null;
  const minute = Number(mi);
  const hour = Number(ho);
  if (minute > 59 || hour > 23) return null;
  const time = `${String(hour).padStart(2, "0")}:${String(minute).padStart(2, "0")}`;

  if (isStar(dom)) {
    if (isStar(dow)) return { ...base, kind: "daily", time };
    if (dow === "1-5") return { ...base, kind: "weekdays", time };
    const days = dow.split(",");
    if (days.every((d) => isInt(d) && Number(d) <= 6)) {
      return {
        ...base,
        kind: "weekly",
        time,
        weekdays: days.map(Number),
      };
    }
    return null;
  }

  // 每月第 N 天
  if (isInt(dom) && Number(dom) >= 1 && Number(dom) <= 31 && isStar(dow)) {
    return { ...base, kind: "monthly", time, dayOfMonth: Number(dom) };
  }
  return null;
}

export type UiLocale = "zh" | "en";

/** 界面语言：跟随浏览器首选语言，zh* → 中文，其余 → 英文。 */
export function uiLocale(): UiLocale {
  const nav = typeof navigator === "undefined" ? null : navigator;
  const lang = (nav?.languages?.[0] ?? nav?.language ?? "").toLowerCase();
  return lang.startsWith("zh") ? "zh" : "en";
}

const DOW_LABELS: Record<UiLocale, string[]> = {
  zh: ["周日", "周一", "周二", "周三", "周四", "周五", "周六"],
  en: ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"],
};

function fmtTime(time: string): string {
  return TIME_RE.test(time) ? time : "09:00";
}

/** 频率的人话描述（不含时区后缀，由调用方拼）。 */
export function describeFrequency(f: Frequency, locale: UiLocale): string {
  const t = fmtTime(f.time);
  const zh = locale === "zh";
  switch (f.kind) {
    case "minutes":
      if (f.everyMinutes === 1) return zh ? "每分钟" : "Every minute";
      return zh ? `每隔 ${f.everyMinutes} 分钟` : `Every ${f.everyMinutes} minutes`;
    case "hours":
      if (f.everyHours === 1) return zh ? "每小时（整点）" : "Every hour (on the hour)";
      return zh
        ? `每隔 ${f.everyHours} 小时（整点）`
        : `Every ${f.everyHours} hours (on the hour)`;
    case "daily":
      return zh ? `每天 ${t}` : `Daily at ${t}`;
    case "weekly": {
      const names = [...new Set(f.weekdays)]
        .filter((d) => d >= 0 && d <= 6)
        .sort((a, b) => (a === 0 ? 7 : a) - (b === 0 ? 7 : b))
        .map((d) => DOW_LABELS[locale][d]);
      if (names.length === 0) return zh ? "每周（未选日）" : "Weekly (no day)";
      if (zh) return `每${names.join("、")} ${t}`;
      return `Every ${names.join(", ")} at ${t}`;
    }
    case "monthly": {
      const skip = f.dayOfMonth >= 29;
      if (zh) return `每月 ${f.dayOfMonth} 日 ${t}${skip ? "（没有该日的月份不运行）" : ""}`;
      return `Monthly on day ${f.dayOfMonth} at ${t}${skip ? " (months without it are skipped)" : ""}`;
    }
    case "weekdays":
      return zh ? `工作日（周一至周五）${t}` : `Weekdays (Mon–Fri) at ${t}`;
    case "custom":
      return "";
  }
}

/**
 * 任意表达式的人话描述：能还原成简单频率就用结构化描述，否则用 cronstrue
 * 描述原表达式；crontab 也描述不了返回 null（由调用方落回错误提示）。
 */
export function describeCronExpression(
  expression: string,
  timezone: string,
  locale: UiLocale,
): string | null {
  const restored = frequencyFromCron(expression);
  const tz = effectiveTimezone(timezone);
  const tzSuffix = locale === "zh" ? `（${tz}）` : ` (${tz})`;
  if (restored) return describeFrequency(restored, locale) + tzSuffix;
  const parsed = parseCronExpression(expression);
  if (!parsed.ok) return null;
  try {
    // cronstrue 不认 `?`（robfig 里 ?=*），先归一化再描述。
    const normalized = expression.trim().replace(/\?/g, "*");
    // dom/dow 均非星在 robfig 是「或」语义；cronstrue 中文译「或者」正确，
    // 英文恒译 "and"（与选项无关），给英文读者补消歧注。判定用解析出的
    // star 标志而非字面星号：*/1、含 * 的列表同样置 star（AND 语义）。
    const orNote =
      !parsed.schedule.dom.star && !parsed.schedule.dow.star && locale === "en"
        ? " (day-of-month OR day-of-week)"
        : "";
    return (
      cronstrue.toString(normalized, {
        locale: locale === "zh" ? "zh_CN" : "en",
      }) +
      orNote +
      tzSuffix
    );
  } catch {
    return null;
  }
}

const FIELD_LABELS: Record<string, { zh: string; en: string }> = {
  minute: { zh: "分钟", en: "minute" },
  hour: { zh: "小时", en: "hour" },
  dom: { zh: "日", en: "day-of-month" },
  month: { zh: "月", en: "month" },
  dow: { zh: "周", en: "day-of-week" },
};

/** 解析错误 → 面向用户的中文/英文提示（指出哪个字段、什么范围）。 */
export function cronErrorMessage(error: CronError, locale: UiLocale): string {
  const zh = locale === "zh";
  const field = error.field ? FIELD_LABELS[error.field][locale] : "";
  const prefix = field ? (zh ? `${field}字段` : `${field} field`) : "";
  switch (error.kind) {
    case "empty":
      return zh ? "请输入 cron 表达式" : "Enter a cron expression";
    case "tz-prefix":
      return zh
        ? "不支持表达式内嵌 TZ=/CRON_TZ= 前缀，请使用独立的时区字段"
        : "Inline TZ=/CRON_TZ= prefixes are not supported; use the timezone field instead";
    case "descriptor":
      return zh
        ? "不支持 @daily、@every 这类描述符，请写五字段表达式"
        : "Descriptors like @daily or @every are not supported; write a 5-field expression";
    case "field-count":
      return zh
        ? `需要 5 个字段（分 时 日 月 周），当前 ${error.count ?? "?"} 个`
        : `Expected 5 fields (minute hour day month weekday), got ${error.count ?? "?"}`;
    case "syntax":
      return zh
        ? `${prefix}「${error.token}」无法解析（支持 * , - / 和 JAN–DEC、SUN–SAT 名称）`
        : `${prefix}: cannot parse "${error.token}" (supports * , - / and JAN–DEC, SUN–SAT names)`;
    case "negative":
      return zh
        ? `${prefix}不允许负数「${error.token}」`
        : `${prefix}: negative value "${error.token}" is not allowed`;
    case "too-many-hyphens":
      return zh
        ? `${prefix}「${error.token}」区间写法不正确（只允许一个 -）`
        : `${prefix}: "${error.token}" is not a valid range (at most one -)`;
    case "too-many-slashes":
      return zh
        ? `${prefix}「${error.token}」步进写法不正确（只允许一个 /）`
        : `${prefix}: "${error.token}" is not a valid step (at most one /)`;
    case "below-min":
      return zh
        ? `${prefix}「${error.token}」超出范围（最小 ${error.min}）`
        : `${prefix}: "${error.token}" is below the minimum (${error.min})`;
    case "above-max":
      return zh
        ? `${prefix}「${error.token}」超出范围（最大 ${error.max}）`
        : `${prefix}: "${error.token}" exceeds the maximum (${error.max})`;
    case "reversed":
      return zh
        ? `${prefix}「${error.token}」区间起点大于终点`
        : `${prefix}: "${error.token}" has a start greater than its end`;
    case "zero-step":
      return zh
        ? `${prefix}「${error.token}」步进必须是正数`
        : `${prefix}: "${error.token}" must have a positive step`;
  }
}
