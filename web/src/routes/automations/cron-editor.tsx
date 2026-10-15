import { useEffect, useMemo, useState } from "react";
import { Combobox } from "@base-ui/react/combobox";
import { CheckIcon, ChevronDownIcon } from "lucide-react";

import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  OptionSelect,
  type OptionSelectOption,
} from "@/components/ui/option-select";
import { cn } from "@/lib/utils";
import {
  nextCronRuns,
  timezoneOptions,
  effectiveTimezone,
  computeTimezone,
  isValidTimezone,
} from "@/lib/cron-schedule";
import {
  cronErrorMessage,
  cronFromFrequency,
  defaultFrequency,
  describeCronExpression,
  frequencyFromCron,
  uiLocale,
  DAY_OF_MONTH_OPTIONS,
  HOUR_STEP_OPTIONS,
  MINUTE_STEP_OPTIONS,
  WEEKDAY_ORDER,
  type Frequency,
  type FrequencyKind,
  type UiLocale,
} from "@/lib/cron-frequency";

// cron 触发的可视化编辑器（INT-100）：频率层 + 自定义表达式高级层 +
// 描述/下次运行预览。编辑与新建共用；只做前端生成与解析，服务端存储不变。

const TEXT: Record<string, Record<UiLocale, string>> = {
  frequency: { zh: "频率", en: "Frequency" },
  timezone: { zh: "时区", en: "Timezone" },
  minutes: { zh: "每隔 N 分钟", en: "Every N minutes" },
  hours: { zh: "每隔 N 小时", en: "Every N hours" },
  daily: { zh: "每天", en: "Daily" },
  weekly: { zh: "每周", en: "Weekly" },
  monthly: { zh: "每月", en: "Monthly" },
  weekdays: { zh: "工作日", en: "Weekdays" },
  custom: { zh: "自定义（cron 表达式）", en: "Custom (cron expression)" },
  minuteUnit: { zh: "分钟", en: "min" },
  hourUnit: { zh: "小时", en: "h" },
  atTime: { zh: "时间", en: "At" },
  dayOfMonth: { zh: "日期", en: "Day" },
  daySuffix: { zh: "日", en: "" },
  everyPrefix: { zh: "每隔", en: "Every" },
  cronExpr: { zh: "cron 表达式", en: "Cron expression" },
  cronHelp: {
    zh: "标准五字段：分 时 日 月 周；支持 * , - / 与 JAN–DEC、SUN–SAT 名称；不支持秒与 @every。夏令时跳过的本地时间不触发；时钟回拨产生的重复本地时间会各触发一次。",
    en: "Standard 5 fields: minute hour day month weekday; supports * , - / and JAN–DEC, SUN–SAT names; no seconds or @every. Local times skipped by DST do not fire; repeated local times from a fallback transition each fire once.",
  },
  examples: { zh: "示例", en: "Examples" },
  nextRuns: { zh: "接下来 3 次运行时间", en: "Next 3 runs" },
  noRuns: {
    zh: "未来 5 年没有可运行时间（表达式合法但日期条件无法满足）",
    en: "No runs in the next 5 years (valid expression, unsatisfiable dates)",
  },
  invalidTz: {
    zh: "时区无效（须为 IANA 名称，如 Asia/Shanghai）",
    en: "Invalid timezone (IANA name, e.g. Asia/Shanghai)",
  },
  pickOneDay: {
    zh: "请选择至少一个星期",
    en: "Pick at least one weekday",
  },
  pickTime: {
    zh: "请填写有效的触发时间",
    en: "Enter a valid trigger time",
  },
  intervalHint: {
    zh: "需要其他间隔请选「自定义」。",
    en: "Need a different interval? Choose “Custom”.",
  },
  searchTz: { zh: "搜索时区…", en: "Search timezones…" },
  noTz: { zh: "无匹配时区", en: "No matching timezone" },
};

const t = (key: string, locale: UiLocale) => TEXT[key][locale];

const CRON_EXAMPLES = [
  { expr: "0 9 * * *", zh: "每天 09:00", en: "Daily at 09:00" },
  { expr: "30 18 * * 1-5", zh: "工作日 18:30", en: "Weekdays at 18:30" },
  { expr: "0 * * * *", zh: "每小时整点", en: "Hourly on the hour" },
  { expr: "0 0 1 * *", zh: "每月 1 日 00:00", en: "1st of month, 00:00" },
  { expr: "0 10 * * 1,3,5", zh: "周一三五 10:00", en: "Mon/Wed/Fri 10:00" },
  { expr: "*/15 8-20 * * *", zh: "8–20 点每 15 分钟", en: "Every 15 min, 8–20h" },
];

export function CronScheduleFields({
  expression,
  timezone,
  onChange,
}: {
  expression: string;
  timezone: string;
  onChange: (expr: string, tz: string) => void;
}) {
  const locale = uiLocale();
  // freq 保存可视化参数；kind=custom 时其余字段保留最后一次可视化状态。
  const [freq, setFreq] = useState<Frequency>(() => ({
    ...defaultFrequency(),
    ...(frequencyFromCron(expression) ?? { kind: "custom" as const }),
  }));

  const generated = cronFromFrequency(freq);
  const effectiveExpr = freq.kind === "custom" ? expression : (generated ?? "");
  // 提交给表单的表达式：可视化层无法生成时提交空串，由表单必填校验拦截保存。
  const submitExpr = freq.kind === "custom" ? expression : (generated ?? "");
  const tz = effectiveTimezone(timezone);

  const applyFreq = (next: Frequency) => {
    setFreq(next);
    onChange(cronFromFrequency(next) ?? "", timezone);
  };

  const pickKind = (kind: FrequencyKind) => {
    if (kind === freq.kind) return;
    if (kind === "custom") {
      // 可视化 → 自定义：带入当前表达式作为起点。
      setFreq({ ...freq, kind });
      return;
    }
    // 自定义/其他 → 可视化：表达式能还原则沿用其中参数。
    const restored = frequencyFromCron(expression);
    const next: Frequency = {
      ...freq,
      ...(restored?.kind === kind ? restored : {}),
      kind,
    };
    applyFreq(next);
  };

  return (
    <div className="space-y-3">
      <div className="grid grid-cols-2 gap-3">
        <div className="space-y-2">
          <Label>{t("frequency", locale)}</Label>
          <OptionSelect
            value={freq.kind}
            onValueChange={(next) =>
              next && pickKind(next as FrequencyKind)
            }
            options={(
              [
                "minutes",
                "hours",
                "daily",
                "weekly",
                "monthly",
                "weekdays",
                "custom",
              ] as FrequencyKind[]
            ).map((k) => ({ value: k, label: t(k, locale) }))}
            className="w-full"
          />
        </div>
        <div className="space-y-2">
          <Label>{t("timezone", locale)}</Label>
          <TimezoneSelect
            value={tz}
            onChange={(next) => onChange(submitExpr, next)}
            locale={locale}
          />
        </div>
      </div>

      {freq.kind === "minutes" && (
        <div className="flex items-end gap-2">
          <div className="w-40 space-y-2">
            <Label>{t("everyPrefix", locale)}</Label>
            <OptionSelect
              value={String(freq.everyMinutes)}
              onValueChange={(next) =>
                applyFreq({ ...freq, everyMinutes: Number(next) })
              }
              options={MINUTE_STEP_OPTIONS.map((n) => ({
                value: String(n),
                label: `${n} ${t("minuteUnit", locale)}`,
              }))}
              className="w-full"
            />
          </div>
          <p className="pb-2 text-xs text-muted-foreground">
            {t("intervalHint", locale)}
          </p>
        </div>
      )}

      {freq.kind === "hours" && (
        <div className="flex items-end gap-2">
          <div className="w-40 space-y-2">
            <Label>{t("everyPrefix", locale)}</Label>
            <OptionSelect
              value={String(freq.everyHours)}
              onValueChange={(next) =>
                applyFreq({ ...freq, everyHours: Number(next) })
              }
              options={HOUR_STEP_OPTIONS.map((n) => ({
                value: String(n),
                label: `${n} ${t("hourUnit", locale)}`,
              }))}
              className="w-full"
            />
          </div>
          <p className="pb-2 text-xs text-muted-foreground">
            {t("intervalHint", locale)}
          </p>
        </div>
      )}

      {(freq.kind === "daily" ||
        freq.kind === "weekly" ||
        freq.kind === "monthly" ||
        freq.kind === "weekdays") && (
        <div className="flex flex-wrap items-end gap-3">
          {(freq.kind === "monthly") && (
            <div className="w-40 space-y-2">
              <Label>{t("dayOfMonth", locale)}</Label>
              <OptionSelect
                value={String(freq.dayOfMonth)}
                onValueChange={(next) =>
                  applyFreq({ ...freq, dayOfMonth: Number(next) })
                }
                options={DAY_OF_MONTH_OPTIONS.map((d): OptionSelectOption => ({
                  value: String(d),
                  label:
                    locale === "zh"
                      ? `${d} ${t("daySuffix", locale)}`
                      : `Day ${d}`,
                }))}
                className="w-full"
              />
            </div>
          )}
          <div className="w-32 space-y-2">
            <Label>{t("atTime", locale)}</Label>
            <Input
              type="time"
              value={freq.time}
              onChange={(e) => applyFreq({ ...freq, time: e.target.value })}
            />
          </div>
          {freq.kind === "weekly" && (
            <div className="space-y-2">
              <Label>{t("weekly", locale)}</Label>
              <div className="flex flex-wrap gap-1">
                {WEEKDAY_ORDER.map((d) => {
                  const active = freq.weekdays.includes(d);
                  return (
                    <button
                      key={d}
                      type="button"
                      aria-pressed={active}
                      onClick={() => {
                        const days = active
                          ? freq.weekdays.filter((x) => x !== d)
                          : [...freq.weekdays, d];
                        applyFreq({ ...freq, weekdays: days });
                      }}
                      className={cn(
                        "h-8 min-w-8 rounded-md border px-2 text-xs",
                        active
                          ? "border-primary bg-primary/10 text-foreground"
                          : "border-input text-muted-foreground hover:bg-muted/60",
                      )}
                    >
                      {(locale === "zh" ? ["日", "一", "二", "三", "四", "五", "六"] : ["Su", "Mo", "Tu", "We", "Th", "Fr", "Sa"])[d]}
                    </button>
                  );
                })}
              </div>
            </div>
          )}
        </div>
      )}

      {freq.kind === "custom" && (
        <div className="space-y-2">
          <Label htmlFor="automation-cron">{t("cronExpr", locale)}</Label>
          <Input
            id="automation-cron"
            value={expression}
            placeholder="*/10 * * * *"
            onChange={(e) => onChange(e.target.value, timezone)}
          />
          <p className="text-xs text-muted-foreground">{t("cronHelp", locale)}</p>
          <div className="flex flex-wrap items-center gap-1.5">
            <span className="text-xs text-muted-foreground">
              {t("examples", locale)}:
            </span>
            {CRON_EXAMPLES.map((ex) => (
              <button
                key={ex.expr}
                type="button"
                className="rounded border border-input px-1.5 py-0.5 text-xs text-muted-foreground hover:bg-muted/60"
                onClick={() => onChange(ex.expr, timezone)}
                title={ex.expr}
              >
                {locale === "zh" ? ex.zh : ex.en}
              </button>
            ))}
          </div>
        </div>
      )}

      <SchedulePreview
        expression={effectiveExpr}
        timezone={tz}
        needsDayPick={freq.kind === "weekly" && freq.weekdays.length === 0}
        needsTime={
          generated === null &&
          freq.kind !== "custom" &&
          freq.kind !== "minutes" &&
          freq.kind !== "hours" &&
          !(freq.kind === "weekly" && freq.weekdays.length === 0)
        }
        locale={locale}
      />
    </div>
  );
}

// 描述 + 接下来 3 次运行时间：与服务端 robfig 解析对齐（见 cron-schedule.ts）。
function SchedulePreview({
  expression,
  timezone,
  needsDayPick,
  needsTime,
  locale,
}: {
  expression: string;
  timezone: string;
  needsDayPick: boolean;
  needsTime: boolean;
  locale: UiLocale;
}) {
  // 预览以 now 为基准；弹窗停留期间最早的运行会过期，定时把 now 往前推。
  const [now, setNow] = useState(() => Date.now());
  const preview = useMemo(() => {
    if (!isValidTimezone(timezone)) {
      return { error: t("invalidTz", locale) };
    }
    const result = nextCronRuns(expression, timezone, 3, now);
    if (!result.ok) {
      return { error: cronErrorMessage(result.error, locale) };
    }
    const desc = describeCronExpression(expression, timezone, locale);
    const dateLocale = locale === "zh" ? "zh-CN" : "en-US";
    const fmtOpts: Intl.DateTimeFormatOptions = {
      // 「Local」按浏览器本机时区尽力预览，标注仍是 Local（= 服务器机器时区）。
      timeZone: computeTimezone(timezone),
      month: "short",
      day: "numeric",
      weekday: "short",
      hour: "2-digit",
      minute: "2-digit",
      hourCycle: "h23",
    };
    const fmt = new Intl.DateTimeFormat(dateLocale, fmtOpts);
    const formatted = result.runs.map((ms) => fmt.format(new Date(ms)));
    // 回拨时段两个 UTC 时刻映射到同一墙钟（如 01:30 出现两次），补时区缩写消歧。
    let runs = formatted;
    if (new Set(formatted).size !== formatted.length) {
      const withZone = new Intl.DateTimeFormat(dateLocale, {
        ...fmtOpts,
        timeZoneName: "short",
      });
      runs = result.runs.map((ms) => withZone.format(new Date(ms)));
    }
    return {
      desc,
      runs,
      firstRunMs: result.runs[0],
      empty: result.runs.length === 0,
    };
  }, [expression, timezone, locale, now]);

  useEffect(() => {
    const firstRunMs =
      "firstRunMs" in preview ? preview.firstRunMs : undefined;
    const delay = firstRunMs
      ? Math.min(Math.max(firstRunMs - Date.now() + 1000, 2000), 60_000)
      : 60_000;
    const id = setTimeout(() => setNow(Date.now()), delay);
    return () => clearTimeout(id);
  }, [preview]);

  if (needsDayPick) {
    return (
      <p className="text-xs text-destructive">{t("pickOneDay", locale)}</p>
    );
  }

  if (needsTime) {
    return <p className="text-xs text-destructive">{t("pickTime", locale)}</p>;
  }

  return (
    <div className="space-y-1 rounded-md border border-dashed px-3 py-2 text-xs">
      {preview.error ? (
        <p className="text-destructive">{preview.error}</p>
      ) : (
        <>
          {preview.desc && <p className="text-foreground">{preview.desc}</p>}
          {preview.empty ? (
            <p className="text-muted-foreground">{t("noRuns", locale)}</p>
          ) : (
            <p className="text-muted-foreground">
              {t("nextRuns", locale)}：{preview.runs?.join(" · ")}（{timezone}）
            </p>
          )}
        </>
      )}
    </div>
  );
}

// 带搜索的 IANA 时区选择（Base UI Combobox）。
function TimezoneSelect({
  value,
  onChange,
  locale,
}: {
  value: string;
  onChange: (tz: string) => void;
  locale: UiLocale;
}) {
  const zones = useMemo(() => timezoneOptions(), []);
  return (
    <Combobox.Root
      items={zones}
      value={value}
      onValueChange={(next) => next && onChange(next)}
      autoHighlight
    >
      <div className="relative">
        <Combobox.Input
          placeholder={t("searchTz", locale)}
          aria-label={t("timezone", locale)}
          className="flex h-9 w-full rounded-md border border-input bg-transparent px-3 pr-8 text-sm outline-hidden placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50"
        />
        <Combobox.Trigger
          aria-label={t("timezone", locale)}
          className="absolute inset-y-0 right-0 flex w-8 items-center justify-center text-muted-foreground"
        >
          <ChevronDownIcon className="size-4" />
        </Combobox.Trigger>
      </div>
      <Combobox.Portal>
        <Combobox.Positioner
          sideOffset={4}
          className="isolate z-50"
        >
          <Combobox.Popup className="max-h-64 w-(--anchor-width) overflow-y-auto rounded-lg bg-popover text-popover-foreground shadow-md ring-1 ring-foreground/10">
            <Combobox.Empty className="px-3 py-2 text-xs text-muted-foreground">
              {t("noTz", locale)}
            </Combobox.Empty>
            <Combobox.List>
              {(zone: string) => (
                <Combobox.Item
                  key={zone}
                  value={zone}
                  className="flex cursor-default items-center justify-between gap-2 px-3 py-1.5 text-sm outline-hidden data-highlighted:bg-accent data-highlighted:text-accent-foreground"
                >
                  <span className="truncate">{zone}</span>
                  <Combobox.ItemIndicator>
                    <CheckIcon className="size-3.5" />
                  </Combobox.ItemIndicator>
                </Combobox.Item>
              )}
            </Combobox.List>
          </Combobox.Popup>
        </Combobox.Positioner>
      </Combobox.Portal>
    </Combobox.Root>
  );
}
