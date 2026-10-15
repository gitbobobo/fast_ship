import { describe, expect, it } from "vitest";

import {
  cronErrorMessage,
  cronFromFrequency,
  defaultFrequency,
  describeCronExpression,
  describeFrequency,
  frequencyFromCron,
  HOUR_STEP_OPTIONS,
  MINUTE_STEP_OPTIONS,
} from "@/lib/cron-frequency";
import { parseCronExpression } from "@/lib/cron-schedule";
import type { Frequency } from "@/lib/cron-frequency";

const base = (patch: Partial<Frequency>): Frequency => ({
  ...defaultFrequency(),
  ...patch,
});

describe("频率 → cron 生成", () => {
  it.each([
    [base({ kind: "minutes", everyMinutes: 1 }), "* * * * *"],
    [base({ kind: "minutes", everyMinutes: 10 }), "*/10 * * * *"],
    [base({ kind: "minutes", everyMinutes: 30 }), "*/30 * * * *"],
    [base({ kind: "hours", everyHours: 1 }), "0 * * * *"],
    [base({ kind: "hours", everyHours: 6 }), "0 */6 * * *"],
    [base({ kind: "daily", time: "09:30" }), "30 9 * * *"],
    [base({ kind: "weekly", time: "09:30", weekdays: [1, 3] }), "30 9 * * 1,3"],
    [base({ kind: "weekly", time: "08:00", weekdays: [0] }), "0 8 * * 0"],
    [base({ kind: "weekly", time: "08:00", weekdays: [0, 6] }), "0 8 * * 0,6"],
    [base({ kind: "monthly", time: "09:00", dayOfMonth: 15 }), "0 9 15 * *"],
    [base({ kind: "monthly", time: "09:00", dayOfMonth: 31 }), "0 9 31 * *"],
    [base({ kind: "weekdays", time: "18:30" }), "30 18 * * 1-5"],
  ])("%o → %s", (freq, expr) => {
    expect(cronFromFrequency(freq)).toBe(expr);
  });

  it("每周未选任何一天时无法生成", () => {
    expect(cronFromFrequency(base({ kind: "weekly", weekdays: [] }))).toBeNull();
  });

  it("需要时间的频率在时间非法/缺失时不生成（不清空也不回退默认时间）", () => {
    for (const kind of ["daily", "weekly", "monthly", "weekdays"] as const) {
      expect(cronFromFrequency(base({ kind, time: "" }))).toBeNull();
      expect(cronFromFrequency(base({ kind, time: "25:00" }))).toBeNull();
      expect(cronFromFrequency(base({ kind, time: "9:99" }))).toBeNull();
    }
  });

  it("custom 不由本层生成", () => {
    expect(cronFromFrequency(base({ kind: "custom" }))).toBeNull();
  });

  it("步进选项只含整除值", () => {
    for (const m of MINUTE_STEP_OPTIONS) expect(60 % m).toBe(0);
    for (const h of HOUR_STEP_OPTIONS) expect(24 % h).toBe(0);
  });
});

describe("cron → 频率还原", () => {
  it.each([
    ["* * * * *", "minutes", 1],
    ["*/10 * * * *", "minutes", 10],
    ["*/2 * * * *", "minutes", 2],
    ["0 * * * *", "hours", 1],
    ["0 */3 * * *", "hours", 3],
  ] as const)("%s → %s/%s", (expr, kind, n) => {
    const f = frequencyFromCron(expr);
    expect(f?.kind).toBe(kind);
    expect(kind === "minutes" ? f?.everyMinutes : f?.everyHours).toBe(n);
  });

  it("每天", () => {
    const f = frequencyFromCron("30 9 * * *");
    expect(f).toMatchObject({ kind: "daily", time: "09:30" });
  });

  it("每周：列表、含周日、乱序", () => {
    expect(frequencyFromCron("30 9 * * 1,3")).toMatchObject({
      kind: "weekly",
      time: "09:30",
      weekdays: [1, 3],
    });
    expect(frequencyFromCron("0 8 * * 0,6")).toMatchObject({
      kind: "weekly",
      weekdays: [0, 6],
    });
  });

  it("工作日", () => {
    expect(frequencyFromCron("30 18 * * 1-5")).toMatchObject({
      kind: "weekdays",
      time: "18:30",
    });
  });

  it("每月第 N 天", () => {
    expect(frequencyFromCron("0 9 15 * *")).toMatchObject({
      kind: "monthly",
      dayOfMonth: 15,
    });
    // 31 日还原为 monthly 第 31 天（描述会注明短月不运行）
    expect(frequencyFromCron("0 9 31 * *")).toMatchObject({
      kind: "monthly",
      dayOfMonth: 31,
    });
  });

  it.each([
    "*/7 * * * *", // 7 不整除 60 → 留自定义
    "0 */5 * * *", // 5 不整除 24
    "0 9 * * 1#2",
    "0 9 L * *",
    "0 9 * JAN *",
    "0 9-17 * * *", // 小时是区间
    "15,45 9 * * *", // 分钟是列表
    "0 9 1,15 * *", // 日是列表
    "0 9 * * 1-3", // 非 1-5 的区间（可视化只有周一~周日多选）
    "0 9 * * * *", // 6 字段
    "@daily",
    "",
  ])("不可还原 → null: %s", (expr) => {
    expect(frequencyFromCron(expr)).toBeNull();
  });

  it("? 与 * 互换、等价步进写法可还原", () => {
    expect(frequencyFromCron("0 9 * ? *")).toMatchObject({
      kind: "daily",
      time: "09:00",
    });
    expect(frequencyFromCron("30 9 ? * 5")).toMatchObject({
      kind: "weekly",
      weekdays: [5],
    });
    for (const expr of ["0/10 * * * *", "0-59/10 * * * *", "*/10 * * * *"]) {
      expect(frequencyFromCron(expr)).toMatchObject({
        kind: "minutes",
        everyMinutes: 10,
      });
    }
    for (const expr of ["0 0/6 * * *", "0 0-23/6 * * *", "0 */6 * * *"]) {
      expect(frequencyFromCron(expr)).toMatchObject({
        kind: "hours",
        everyHours: 6,
      });
    }
    // 非零起点的步进不等价 */N → 不可还原；不在选项内的步进同样不可还原
    expect(frequencyFromCron("1/10 * * * *")).toBeNull();
    expect(frequencyFromCron("0 1/6 * * *")).toBeNull();
    expect(frequencyFromCron("*/7 * * * *")).toBeNull();
  });

  it("生成→还原 往返", () => {
    const cases: Frequency[] = [
      base({ kind: "minutes", everyMinutes: 5 }),
      base({ kind: "hours", everyHours: 12 }),
      base({ kind: "daily", time: "23:59" }),
      base({ kind: "weekly", time: "09:30", weekdays: [2, 4] }),
      base({ kind: "monthly", time: "00:00", dayOfMonth: 28 }),
      base({ kind: "weekdays", time: "07:45" }),
    ];
    for (const f of cases) {
      const expr = cronFromFrequency(f)!;
      const restored = frequencyFromCron(expr)!;
      expect(restored.kind).toBe(f.kind);
      expect(cronFromFrequency(restored)).toBe(expr);
    }
  });
});

describe("描述文案", () => {
  it("中文", () => {
    expect(describeFrequency(base({ kind: "minutes", everyMinutes: 10 }), "zh")).toBe(
      "每隔 10 分钟",
    );
    expect(
      describeFrequency(
        base({ kind: "weekly", time: "09:30", weekdays: [1, 3] }),
        "zh",
      ),
    ).toBe("每周一、周三 09:30");
    expect(describeFrequency(base({ kind: "daily", time: "09:00" }), "zh")).toBe(
      "每天 09:00",
    );
    expect(
      describeFrequency(base({ kind: "weekdays", time: "18:00" }), "zh"),
    ).toBe("工作日（周一至周五）18:00");
    // 31 日明确提示短月不运行
    expect(
      describeFrequency(base({ kind: "monthly", dayOfMonth: 31 }), "zh"),
    ).toContain("没有该日的月份不运行");
    expect(
      describeFrequency(base({ kind: "monthly", dayOfMonth: 30 }), "zh"),
    ).toContain("没有该日的月份不运行");
  });

  it("英文", () => {
    expect(describeFrequency(base({ kind: "minutes", everyMinutes: 10 }), "en")).toBe(
      "Every 10 minutes",
    );
    expect(
      describeFrequency(
        base({ kind: "weekly", time: "09:30", weekdays: [1, 3] }),
        "en",
      ),
    ).toBe("Every Mon, Wed at 09:30");
    expect(describeFrequency(base({ kind: "daily", time: "09:00" }), "en")).toBe(
      "Daily at 09:00",
    );
  });

  it("describeCronExpression：简单表达式走结构化描述并带时区", () => {
    expect(describeCronExpression("30 9 * * 1,3", "Asia/Shanghai", "zh")).toBe(
      "每周一、周三 09:30（Asia/Shanghai）",
    );
    expect(describeCronExpression("30 9 * * 1,3", "Asia/Shanghai", "en")).toBe(
      "Every Mon, Wed at 09:30 (Asia/Shanghai)",
    );
    // 空时区 → UTC
    expect(describeCronExpression("0 9 * * *", "", "zh")).toContain("（UTC）");
  });

  it("describeCronExpression：复杂表达式用 cronstrue，非法表达式返回 null", () => {
    expect(describeCronExpression("0 9-17 * * 1-5", "UTC", "zh")).toContain(
      "（UTC）",
    );
    expect(describeCronExpression("0 9-17 * * 1-5", "UTC", "en")).toContain(
      "(UTC)",
    );
    expect(describeCronExpression("bad expr", "UTC", "zh")).toBeNull();
    expect(describeCronExpression("", "UTC", "en")).toBeNull();
  });

  it("? 归一化后仍可描述；英文 dom/dow 为「或」语义时补消歧注", () => {
    expect(describeCronExpression("0 9 * ? 5", "UTC", "en")).toBeTruthy();
    const en = describeCronExpression("0 9 15 * 1", "UTC", "en");
    expect(en).toContain("OR day-of-week");
    const zh = describeCronExpression("0 9 15 * 1", "UTC", "zh");
    expect(zh).toContain("或者");
    expect(zh).not.toContain("OR day-of-week");
  });
});

describe("错误文案", () => {
  const msg = (expr: string, locale: "zh" | "en") => {
    const r = parseCronExpression(expr);
    expect(r.ok).toBe(false);
    return r.ok ? "" : cronErrorMessage(r.error, locale);
  };

  it("中文：字段数 / 越界 / 描述符 / 时区前缀", () => {
    expect(msg("* * * *", "zh")).toContain("5 个字段");
    expect(msg("61 * * * *", "zh")).toContain("分钟");
    expect(msg("61 * * * *", "zh")).toContain("59");
    expect(msg("@daily", "zh")).toContain("@daily");
    expect(msg("TZ=UTC 0 9 * * *", "zh")).toContain("时区");
    expect(msg("0 0 * * 7", "zh")).toContain("周");
  });

  it("英文：字段数 / 越界 / 描述符", () => {
    expect(msg("* * * *", "en")).toContain("5 fields");
    expect(msg("61 * * * *", "en")).toContain("minute");
    expect(msg("@every 1h", "en")).toContain("@every");
  });
});
