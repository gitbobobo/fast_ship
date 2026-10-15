// 五字段 cron（分 时 日 月 周）的解析与下次运行计算：robfig/cron/v3 的忠实移植，
// 与服务端 automationCronParser（Minute|Hour|Dom|Month|Dow）逐项对齐。
//
// 接受集合与 robfig 一致：? 等同 *；月/周字段支持 JAN–DEC / SUN–SAT 名称
// （大小写不敏感）；N/step 展开为 N–max/step；dom 与 dow 同时受限按 OR 匹配，
// 任一含 * 按 AND；dow 合法值 0–6（7 非法）；秒字段、@ 描述符、L/W/#、负数、
// 越界值一律报错。表达式内 TZ=/CRON_TZ= 前缀在解析前显式拒绝（同服务端）。
//
// 时区换算只依赖 Intl：wall→instant 复刻 Go time.Date 的两步归一化（先把墙钟
// 当 UTC 查 offset，落地时刻不属于该 zone 时改用落地时刻的 offset 再换算一次）。
// 已知分叉：robfig 在某日历日整体被跳过的时区（Pacific/Apia 2011-12-30 等）里
// day 循环原地死循环；本实现检测到“时间不前进”后跳到下一个真实午夜，等效于
// 该日不匹配，不挂起页面。

// ---------- 解析 ----------

export type CronFieldName = "minute" | "hour" | "dom" | "month" | "dow";

export type CronErrorKind =
  | "empty"
  | "tz-prefix"
  | "descriptor"
  | "field-count"
  | "syntax"
  | "negative"
  | "too-many-hyphens"
  | "too-many-slashes"
  | "below-min"
  | "above-max"
  | "reversed"
  | "zero-step";

export interface CronError {
  kind: CronErrorKind;
  /** 出错字段；字段数/前缀类错误为 undefined。 */
  field?: CronFieldName;
  /** 出错的原始片段（如 "61"、"5-2"）。 */
  token?: string;
  /** 字段允许范围（below-min/above-max 时使用）。 */
  min?: number;
  max?: number;
  /** 实际字段数（field-count 时使用）。 */
  count?: number;
}

export interface CronFieldSpec {
  bits: ReadonlySet<number>;
  /** 字段含 * 或 ?（影响 dom/dow 的 AND/OR 匹配）。 */
  star: boolean;
}

export interface CronSchedule {
  minute: CronFieldSpec;
  hour: CronFieldSpec;
  dom: CronFieldSpec;
  month: CronFieldSpec;
  dow: CronFieldSpec;
}

interface FieldBounds {
  name: CronFieldName;
  min: number;
  max: number;
  names?: Record<string, number>;
}

const MONTH_NAMES: Record<string, number> = {
  jan: 1, feb: 2, mar: 3, apr: 4, may: 5, jun: 6,
  jul: 7, aug: 8, sep: 9, oct: 10, nov: 11, dec: 12,
};
const DOW_NAMES: Record<string, number> = {
  sun: 0, mon: 1, tue: 2, wed: 3, thu: 4, fri: 5, sat: 6,
};

const FIELD_BOUNDS: FieldBounds[] = [
  { name: "minute", min: 0, max: 59 },
  { name: "hour", min: 0, max: 23 },
  { name: "dom", min: 1, max: 31 },
  { name: "month", min: 1, max: 12, names: MONTH_NAMES },
  { name: "dow", min: 0, max: 6, names: DOW_NAMES },
];

class CronParseError extends Error {
  readonly detail: CronError;
  constructor(detail: CronError) {
    super(detail.kind);
    this.detail = detail;
  }
}

// mustParseInt 对应 Go strconv.Atoi：可选符号 + ASCII 数字；负数单独报错。
function mustParseInt(token: string, field: FieldBounds): number {
  if (!/^[+-]?\d+$/.test(token)) {
    throw new CronParseError({ kind: "syntax", field: field.name, token });
  }
  const n = parseInt(token, 10);
  if (n < 0) {
    throw new CronParseError({ kind: "negative", field: field.name, token });
  }
  return n;
}

function parseIntOrName(token: string, field: FieldBounds): number {
  const lower = token.toLowerCase();
  if (field.names && lower in field.names) return field.names[lower];
  return mustParseInt(token, field);
}

// getRange 对应 robfig 的同名函数： number | number "-" number ["/" number]。
function getRange(expr: string, field: FieldBounds): { bits: Set<number>; star: boolean } {
  const rangeAndStep = expr.split("/");
  const lowAndHigh = rangeAndStep[0].split("-");
  const singleDigit = lowAndHigh.length === 1;

  let start: number;
  let end: number;
  let star = false;
  if (lowAndHigh[0] === "*" || lowAndHigh[0] === "?") {
    // robfig 对 "*/?-x" 这类写法直接按 * 处理，忽略 -x 后缀。
    start = field.min;
    end = field.max;
    star = true;
  } else {
    start = parseIntOrName(lowAndHigh[0], field);
    if (lowAndHigh.length === 1) {
      end = start;
    } else if (lowAndHigh.length === 2) {
      end = parseIntOrName(lowAndHigh[1], field);
    } else {
      throw new CronParseError({ kind: "too-many-hyphens", field: field.name, token: expr });
    }
  }

  let step: number;
  if (rangeAndStep.length === 1) {
    step = 1;
  } else if (rangeAndStep.length === 2) {
    step = mustParseInt(rangeAndStep[1], field);
    // "N/step" 意为 "N-max/step"（仅当左侧是单值而非区间时）。
    if (singleDigit) end = field.max;
    if (step > 1) star = false;
  } else {
    throw new CronParseError({ kind: "too-many-slashes", field: field.name, token: expr });
  }

  if (start < field.min) {
    throw new CronParseError({
      kind: "below-min", field: field.name, token: expr, min: field.min, max: field.max,
    });
  }
  if (end > field.max) {
    throw new CronParseError({
      kind: "above-max", field: field.name, token: expr, min: field.min, max: field.max,
    });
  }
  if (start > end) {
    throw new CronParseError({ kind: "reversed", field: field.name, token: expr });
  }
  if (step === 0) {
    throw new CronParseError({ kind: "zero-step", field: field.name, token: expr });
  }

  const bits = new Set<number>();
  for (let v = start; v <= end; v += step) bits.add(v);
  return { bits, star };
}

function getField(fieldExpr: string, field: FieldBounds): CronFieldSpec {
  const bits = new Set<number>();
  let star = false;
  // strings.FieldsFunc 语义：连续逗号间的空段被丢弃（"1,,2" = {1,2}，
  // 全空字段解析为空集合——永不可匹配，等价 robfig 返回 5 年零值）。
  for (const expr of fieldExpr.split(",").filter((s) => s !== "")) {
    const r = getRange(expr, field);
    for (const v of r.bits) bits.add(v);
    star = star || r.star;
  }
  return { bits, star };
}

export type CronParseResult =
  | { ok: true; schedule: CronSchedule }
  | { ok: false; error: CronError };

/** 解析五字段 cron 表达式；行为与 robfig/cron v3 标准解析器逐项一致。 */
export function parseCronExpression(expression: string): CronParseResult {
  const trimmed = expression.trim();
  if (trimmed === "") {
    return { ok: false, error: { kind: "empty" } };
  }
  if (trimmed.startsWith("TZ=") || trimmed.startsWith("CRON_TZ=")) {
    return { ok: false, error: { kind: "tz-prefix" } };
  }
  if (trimmed.startsWith("@")) {
    return { ok: false, error: { kind: "descriptor", token: trimmed } };
  }
  const fields = trimmed.split(/\s+/);
  if (fields.length !== 5) {
    return { ok: false, error: { kind: "field-count", count: fields.length } };
  }
  try {
    const parsed = FIELD_BOUNDS.map((b, i) => getField(fields[i], b));
    return {
      ok: true,
      schedule: {
        minute: parsed[0],
        hour: parsed[1],
        dom: parsed[2],
        month: parsed[3],
        dow: parsed[4],
      },
    };
  } catch (err) {
    if (err instanceof CronParseError) return { ok: false, error: err.detail };
    throw err;
  }
}

// ---------- 时区墙钟换算（Intl only） ----------

interface WallParts {
  year: number;
  month: number;
  day: number;
  hour: number;
  minute: number;
  second: number;
}

const formatterCache = new Map<string, Intl.DateTimeFormat>();

function tzFormatter(tz: string): Intl.DateTimeFormat {
  let f = formatterCache.get(tz);
  if (!f) {
    f = new Intl.DateTimeFormat("en-US", {
      timeZone: tz,
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
      hourCycle: "h23",
    });
    formatterCache.set(tz, f);
  }
  return f;
}

/** IANA 时区是否可用（Intl 不接受即视为无效，同服务端 LoadLocation 的口径）。 */
export function isValidTimezone(tz: string): boolean {
  if (tz === "" ) return true; // 留空 = UTC
  if (tz === "Local") return true; // Go 接受；浏览器按下一条规则解析本机时区
  try {
    new Intl.DateTimeFormat("en-US", { timeZone: tz });
    return true;
  } catch {
    return false;
  }
}

/**
 * 生效时区：空值按 UTC（与服务端一致）。「Local」是 Go `time.LoadLocation`
 * 接受的合法值，原样保留用于展示（语义 = 服务器机器时区）。
 */
export function effectiveTimezone(tz: string | null | undefined): string {
  const t = (tz ?? "").trim();
  return t === "" ? "UTC" : t;
}

/**
 * 计算用时区：「Local」在浏览器端解析为本机时区（尽力近似——与服务器机器
 * 时区可能不同，预览与描述仍以 Local 标注，不作静默替换）。
 */
export function computeTimezone(tz: string): string {
  return tz === "Local"
    ? Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC"
    : tz;
}

/** 可搜索时区列表；老环境不支持 supportedValuesOf 时退回常用列表。 */
export function timezoneOptions(): string[] {
  const intl = Intl as typeof Intl & {
    supportedValuesOf?: (key: string) => string[];
  };
  const names =
    typeof intl.supportedValuesOf === "function"
      ? intl.supportedValuesOf("timeZone")
      : ["UTC", "Asia/Shanghai", "Asia/Tokyo", "Europe/London", "Europe/Berlin",
         "America/New_York", "America/Los_Angeles", "America/Santiago",
         "Australia/Sydney", "Pacific/Auckland"];
  const sorted = [...names].sort((a, b) => a.localeCompare(b, "en"));
  // Local = 服务器机器时区（Go 合法值），固定跟在 UTC 后便于存量数据展示/选择。
  const pinned = ["UTC", "Local"];
  return [...pinned, ...sorted.filter((n) => !pinned.includes(n))];
}

function wallParts(ms: number, tz: string): WallParts {
  const parts = tzFormatter(tz).formatToParts(new Date(ms));
  const get = (type: string) =>
    Number(parts.find((p) => p.type === type)?.value ?? "0");
  return {
    year: get("year"),
    month: get("month"),
    day: get("day"),
    hour: get("hour"),
    minute: get("minute"),
    second: get("second"),
  };
}

/** 某时刻 tz 的 UTC 偏移（毫秒，东为正）。等价 Go 的 zone offset 查询。 */
function offsetMsAt(ms: number, tz: string): number {
  const w = wallParts(ms, tz);
  const asUtc = Date.UTC(w.year, w.month - 1, w.day, w.hour, w.minute, w.second);
  return asUtc - Math.floor(ms / 1000) * 1000;
}

/**
 * 墙钟 → UTC 时刻（Go time.Date 归一化）：先按“墙钟读作 UTC”查 offset0，
 * utc = u0 - offset0；若 utc 已不属于该 offset（不存在的墙钟），改用 utc 处
 * 的 offset1 再算 u0 - offset1。歧义墙钟落到第一次出现，不存在的墙钟回退
 * 到跳迁前的有效时刻（如 Apia 被跳过的整日回退到前一天）。
 */
function wallToInstant(
  year: number,
  month: number,
  day: number,
  hour: number,
  minute: number,
  second: number,
  tz: string,
): number {
  const u0 = Date.UTC(year, month - 1, day, hour, minute, second);
  const off0 = offsetMsAt(u0, tz);
  const utc = u0 - off0;
  const off1 = offsetMsAt(utc, tz);
  return off1 === off0 ? utc : u0 - off1;
}

/** Go AddDate：墙钟字段加减后经 wallToInstant 归一化（含日/月溢出进位）。 */
function addDate(ms: number, dYear: number, dMonth: number, dDay: number, tz: string): number {
  const w = wallParts(ms, tz);
  return wallToInstant(
    w.year + dYear, w.month + dMonth, w.day + dDay,
    w.hour, w.minute, w.second, tz,
  );
}

function civilDow(year: number, month: number, day: number): number {
  return new Date(Date.UTC(year, month - 1, day, 12)).getUTCDay();
}

function dayMatches(s: CronSchedule, w: WallParts): boolean {
  const domMatch = s.dom.bits.has(w.day);
  const dowMatch = s.dow.bits.has(civilDow(w.year, w.month, w.day));
  if (s.dom.star || s.dow.star) return domMatch && dowMatch;
  return domMatch || dowMatch;
}

const HOUR_MS = 3600_000;
const MINUTE_MS = 60_000;

/** 跳到之后第一个真实存在的墙钟午夜（整日被跳过时 wallToInstant 会回退）。 */
function nextRealMidnight(afterMs: number, tz: string): number {
  const w = wallParts(afterMs, tz);
  for (let k = 1; k <= 40; k++) {
    const t = wallToInstant(w.year, w.month, w.day + k, 0, 0, 0, tz);
    if (t > afterMs) return t;
  }
  return afterMs + 40 * 24 * HOUR_MS; // 40 天都跳不出 = 时区数据异常，直接越界
}

/** 下一次运行时刻（UTC 毫秒）；5 年内找不到返回 null。对应 robfig SpecSchedule.Next。 */
export function nextCronRun(
  schedule: CronSchedule,
  timezone: string,
  fromMs: number,
): number | null {
  const tz = computeTimezone(effectiveTimezone(timezone));
  // 空字段集永不可能命中（如 ", * * * *" 解析出空分钟集——robfig 同样接受并
  // 在 5 年扫描后返回空）。逐分钟扫 5 年会拖死渲染线程，提前判无匹配。
  const domEmpty = schedule.dom.bits.size === 0;
  const dowEmpty = schedule.dow.bits.size === 0;
  const dayImpossible =
    (domEmpty && dowEmpty) ||
    ((schedule.dom.star || schedule.dow.star) && (domEmpty || dowEmpty));
  if (
    schedule.minute.bits.size === 0 ||
    schedule.hour.bits.size === 0 ||
    schedule.month.bits.size === 0 ||
    dayImpossible
  ) {
    return null;
  }
  // 起点：下一整秒（Next 返回严格大于 from 的时刻）。
  let t = Math.floor(fromMs / 1000) * 1000 + 1000;
  let added = false;
  const yearLimit = wallParts(t, tz).year + 5;
  // 安全网：month/day 循环的 AddDate 总次数封顶（5 年天数约 1827，取余量）。
  let budget = 6000;

  outer: while (true) {
    if (wallParts(t, tz).year > yearLimit) return null;

    // 月
    while (!schedule.month.bits.has(wallParts(t, tz).month)) {
      if (!added) {
        added = true;
        const w = wallParts(t, tz);
        t = wallToInstant(w.year, w.month, 1, 0, 0, 0, tz);
      }
      const before = t;
      t = addDate(t, 0, 1, 0, tz);
      if (t <= before) t = nextRealMidnight(t, tz);
      if (--budget <= 0) return null;
      if (wallParts(t, tz).month === 1) continue outer;
    }

    // 日（dom/dow 组合语义同 robfig dayMatches）
    while (!dayMatches(schedule, wallParts(t, tz))) {
      if (!added) {
        added = true;
        const w = wallParts(t, tz);
        t = wallToInstant(w.year, w.month, w.day, 0, 0, 0, tz);
      }
      const before = t;
      t = addDate(t, 0, 0, 1, tz);
      // DST 校正：跳迁导致 hour≠0 时按绝对时间挪回午夜（同 robfig）。
      const h = wallParts(t, tz).hour;
      if (h !== 0) t += (h > 12 ? 24 - h : -h) * HOUR_MS;
      if (t <= before) t = nextRealMidnight(t, tz);
      if (--budget <= 0) return null;
      if (wallParts(t, tz).day === 1) continue outer;
    }

    // 时
    while (!schedule.hour.bits.has(wallParts(t, tz).hour)) {
      if (!added) {
        added = true;
        const w = wallParts(t, tz);
        t = wallToInstant(w.year, w.month, w.day, w.hour, 0, 0, tz);
      }
      t += HOUR_MS;
      if (wallParts(t, tz).hour === 0) continue outer;
    }

    // 分
    while (!schedule.minute.bits.has(wallParts(t, tz).minute)) {
      if (!added) {
        added = true;
        t = Math.floor(t / MINUTE_MS) * MINUTE_MS;
      }
      t += MINUTE_MS;
      if (wallParts(t, tz).minute === 0) continue outer;
    }

    // 秒：五字段下固定匹配 0；进入循环的唯一可能是 minute 命中但 second≠0
    // （起点 +1s 的非整秒），逐秒推进到下一分边界后回 WRAP 复核各字段。
    while (wallParts(t, tz).second !== 0) {
      if (!added) {
        added = true;
        t = Math.floor(t / 1000) * 1000;
      }
      t += 1000;
      if (wallParts(t, tz).second === 0) continue outer;
    }

    return t;
  }
}

/**
 * 解析表达式并返回接下来 count 次运行时刻（UTC 毫秒数组）。
 * 表达式非法返回 ok:false + 结构化错误；合法但 5 年内无匹配返回空数组。
 */
export function nextCronRuns(
  expression: string,
  timezone: string,
  count: number,
  fromMs: number = Date.now(),
): { ok: true; runs: number[] } | { ok: false; error: CronError } {
  const parsed = parseCronExpression(expression);
  if (!parsed.ok) return { ok: false, error: parsed.error };
  if (!isValidTimezone(effectiveTimezone(timezone))) {
    return { ok: false, error: { kind: "syntax", token: timezone } };
  }
  const runs: number[] = [];
  let t = fromMs;
  for (let i = 0; i < count; i++) {
    const next = nextCronRun(parsed.schedule, timezone, t);
    if (next == null) break;
    runs.push(next);
    t = next;
  }
  return { ok: true, runs };
}
