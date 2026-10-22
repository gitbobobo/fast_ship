import { describe, expect, it } from "vitest";
import {
  accountRecoveryAt,
  accountRemaining,
  canProbe,
  collectCredentialValues,
  defaultQuotaPool,
  formatQuotaAmount,
  formatRecoveryCountdown,
  formatResetCountdown,
  hasAnyCredentialValue,
  missingRequiredCredentialFields,
  quotaAccountGroup,
  quotaPools,
  quotaUsageTone,
  quotaWindowUsageText,
  sortExhaustedAccounts,
  sortUsableAccounts,
} from "./quota";

const FIELDS: QuotaCredentialField[] = [
  {
    key: "access_token",
    label: "Access Token",
    secret: true,
    required: true,
    placeholder: "",
    help: "",
  },
  {
    key: "account_id",
    label: "Account ID",
    secret: false,
    required: false,
    placeholder: "",
    help: "",
  },
];

describe("quotaUsageTone", () => {
  it("maps used_percent to normal / warning / danger bands", () => {
    expect(quotaUsageTone(null)).toBe("normal");
    expect(quotaUsageTone(0)).toBe("normal");
    expect(quotaUsageTone(79.9)).toBe("normal");
    expect(quotaUsageTone(80)).toBe("warning");
    expect(quotaUsageTone(94.9)).toBe("warning");
    expect(quotaUsageTone(95)).toBe("danger");
    expect(quotaUsageTone(100)).toBe("danger");
  });
});

describe("formatQuotaAmount", () => {
  it("formats per unit with one decimal for fractions", () => {
    expect(formatQuotaAmount(12.5, "count")).toBe("12.5");
    expect(formatQuotaAmount(10, "count")).toBe("10");
    expect(formatQuotaAmount(12.5, "usd")).toBe("$12.5");
    expect(formatQuotaAmount(38, "percent")).toBe("38%");
  });
});

describe("formatResetCountdown", () => {
  const now = new Date("2026-10-17T04:00:00Z").getTime();

  it("renders relative countdowns", () => {
    expect(formatResetCountdown("2026-10-17T04:45:00Z", now)).toBe(
      "45 分钟后重置",
    );
    expect(formatResetCountdown("2026-10-17T07:12:00Z", now)).toBe(
      "3 小时 12 分后重置",
    );
    expect(formatResetCountdown("2026-10-17T09:00:00Z", now)).toBe(
      "5 小时后重置",
    );
    expect(formatResetCountdown("2026-10-19T09:00:00Z", now)).toBe(
      "2 天 5 小时后重置",
    );
    expect(formatResetCountdown("2026-10-20T04:00:00Z", now)).toBe(
      "3 天后重置",
    );
  });

  it("renders imminent reset for past or sub-minute boundaries", () => {
    expect(formatResetCountdown("2026-10-17T03:59:59Z", now)).toBe("即将重置");
    expect(formatResetCountdown("2026-10-17T04:00:00Z", now)).toBe("即将重置");
    expect(formatResetCountdown("2026-10-17T04:00:30Z", now)).toBe("即将重置");
    expect(formatResetCountdown("not-a-date", now)).toBe("即将重置");
  });
});

describe("credential values", () => {
  it("collects non-empty trimmed values and reports missing required fields", () => {
    const values = { access_token: "  tok-1  ", account_id: " " };
    expect(collectCredentialValues(FIELDS, values)).toEqual({
      access_token: "tok-1",
    });
    expect(missingRequiredCredentialFields(FIELDS, values)).toHaveLength(0);
    expect(missingRequiredCredentialFields(FIELDS, {})).toEqual([FIELDS[0]]);
    expect(hasAnyCredentialValue(FIELDS, values)).toBe(true);
    expect(hasAnyCredentialValue(FIELDS, {})).toBe(false);
    expect(hasAnyCredentialValue(FIELDS, { access_token: "  " })).toBe(false);
  });
});

describe("canProbe", () => {
  const PROVIDER: QuotaProvider = {
    id: "factory",
    name: "Factory Droid",
    plan_hint: "",
    credential_fields: FIELDS,
    instructions: [],
    warning: "",
    device_authorization: false,
  };

  it("requires a selected provider", () => {
    expect(canProbe(undefined, { access_token: "tok-1" })).toBe(false);
  });

  it("requires all required credential fields, ignoring optional ones", () => {
    expect(canProbe(PROVIDER, {})).toBe(false);
    expect(canProbe(PROVIDER, { access_token: "   " })).toBe(false);
    expect(canProbe(PROVIDER, { account_id: "acc-1" })).toBe(false);
    expect(canProbe(PROVIDER, { access_token: "tok-1" })).toBe(true);
  });
});

/* ------------------------- INT-130 ------------------------- */

function makeWindow(overrides: Partial<QuotaWindow> = {}): QuotaWindow {
  return {
    kind: "monthly",
    label: "每月",
    used_percent: null,
    used: null,
    limit: null,
    unit: "percent",
    reset_at: null,
    ...overrides,
  };
}

function makeAccount(overrides: Partial<QuotaAccount> = {}): QuotaAccount {
  return {
    id: "acc-1",
    provider: "factory",
    provider_name: "Factory Droid",
    label: "我的 Pro",
    credential_hint: "…ab12",
    plan_name: "Pro",
    status: "ok",
    last_error: "",
    fetched_at: "2026-10-17T04:00:00Z",
    last_success_at: "2026-10-17T04:00:00Z",
    windows: [],
    balance: null,
    note: "",
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-01T00:00:00Z",
    ...overrides,
  };
}

describe("quotaPools", () => {
  it("归并无 pool 字段的窗口为唯一的匿名池", () => {
    const pools = quotaPools([
      makeWindow({ used_percent: 38 }),
      makeWindow({ kind: "weekly", label: "每周", used_percent: 82 }),
    ]);

    expect(pools).toHaveLength(1);
    expect(pools[0].name).toBeNull();
    // 受限窗口 = used_percent 最大的窗口；有效剩余 = 100 - 82
    expect(pools[0].limiting?.label).toBe("每周");
    expect(pools[0].remaining).toBe(18);
    expect(pools[0].exhausted).toBe(false);
  });

  it("按首次出现顺序保留多个池，同池窗口合并", () => {
    const pools = quotaPools([
      makeWindow({ label: "每月", pool: "标准", used_percent: 100 }),
      makeWindow({ label: "5 小时", pool: "Core", used_percent: 87 }),
      makeWindow({ label: "每周", pool: "Core", used_percent: 20 }),
      makeWindow({ label: "每天", pool: "标准", used_percent: 50 }),
    ]);

    expect(pools.map((p) => p.name)).toEqual(["标准", "Core"]);
    expect(pools[0].windows).toHaveLength(2);
    expect(pools[0].limiting?.label).toBe("每月");
    expect(pools[0].remaining).toBe(0);
    expect(pools[0].exhausted).toBe(true);
    expect(pools[1].limiting?.label).toBe("5 小时");
    expect(pools[1].remaining).toBe(13);
    expect(pools[1].exhausted).toBe(false);
  });

  it("used_percent 全为 null 时 remaining 为 null、不算用尽", () => {
    const pools = quotaPools([makeWindow({ used_percent: null })]);
    expect(pools[0].remaining).toBeNull();
    expect(pools[0].limiting).toBeNull();
    expect(pools[0].exhausted).toBe(false);
  });

  it("used_percent 超过 100 时剩余压到 0", () => {
    const pools = quotaPools([makeWindow({ used_percent: 120 })]);
    expect(pools[0].remaining).toBe(0);
    expect(pools[0].exhausted).toBe(true);
  });
});

describe("defaultQuotaPool", () => {
  it("选第一个未用尽的池（正在消耗的池）", () => {
    const pools = quotaPools([
      makeWindow({ label: "每月", pool: "标准", used_percent: 100 }),
      makeWindow({ label: "5 小时", pool: "Core", used_percent: 87 }),
    ]);
    expect(defaultQuotaPool(pools)?.name).toBe("Core");
  });

  it("全部用尽时回退第一个池；空池列表为 null", () => {
    const pools = quotaPools([
      makeWindow({ label: "每月", pool: "标准", used_percent: 100 }),
      makeWindow({ label: "5 小时", pool: "Core", used_percent: 100 }),
    ]);
    expect(defaultQuotaPool(pools)?.name).toBe("标准");
    expect(defaultQuotaPool([])).toBeNull();
  });
});

describe("accountRemaining", () => {
  it("取默认池的有效剩余（标准用尽时取 Core）", () => {
    const account = makeAccount({
      windows: [
        makeWindow({ pool: "标准", used_percent: 100 }),
        makeWindow({ pool: "Core", used_percent: 87 }),
      ],
    });
    expect(accountRemaining(account)).toBe(13);
  });

  it("无窗口或无百分比时为 null", () => {
    expect(accountRemaining(makeAccount())).toBeNull();
    expect(
      accountRemaining(makeAccount({ windows: [makeWindow()] })),
    ).toBeNull();
  });
});

describe("quotaAccountGroup", () => {
  it("credential_invalid / provider_error 归需处理", () => {
    expect(
      quotaAccountGroup(makeAccount({ status: "credential_invalid" })),
    ).toBe("attention");
    expect(quotaAccountGroup(makeAccount({ status: "provider_error" }))).toBe(
      "attention",
    );
  });

  it("全部池用尽归已用尽，任一池有剩余归可用", () => {
    const exhausted = makeAccount({
      windows: [
        makeWindow({ pool: "标准", used_percent: 100 }),
        makeWindow({ pool: "Core", used_percent: 100 }),
      ],
    });
    expect(quotaAccountGroup(exhausted)).toBe("exhausted");

    const usable = makeAccount({
      windows: [
        makeWindow({ pool: "标准", used_percent: 100 }),
        makeWindow({ pool: "Core", used_percent: 10 }),
      ],
    });
    expect(quotaAccountGroup(usable)).toBe("usable");
  });

  it("未抓取的 pending 账号归可用（不算已用尽）", () => {
    expect(quotaAccountGroup(makeAccount({ status: "pending" }))).toBe("usable");
  });
});

describe("accountRecoveryAt", () => {
  it("取各池恢复时间里最早的一个", () => {
    const account = makeAccount({
      status: "ok",
      windows: [
        makeWindow({
          pool: "标准",
          used_percent: 100,
          reset_at: "2026-11-01T00:00:00Z",
        }),
        makeWindow({
          pool: "Core",
          used_percent: 100,
          reset_at: "2026-10-20T00:00:00Z",
        }),
      ],
    });
    expect(accountRecoveryAt(account)).toBe("2026-10-20T00:00:00Z");
  });

  it("同池多个用尽窗口取最晚的 reset_at（早重置的窗口先恢复池仍是用尽）", () => {
    const account = makeAccount({
      windows: [
        makeWindow({
          pool: "标准",
          used_percent: 100,
          reset_at: "2026-10-20T00:00:00Z",
        }),
        makeWindow({
          pool: "标准",
          used_percent: 100,
          reset_at: "2026-10-25T00:00:00Z",
        }),
        // 非用尽窗口不参与恢复时间
        makeWindow({
          pool: "标准",
          used_percent: 50,
          reset_at: "2026-11-01T00:00:00Z",
        }),
      ],
    });
    expect(accountRecoveryAt(account)).toBe("2026-10-25T00:00:00Z");
  });

  it("同池任一用尽窗口缺 reset_at 或无法解析时该池不算出恢复时间", () => {
    expect(
      accountRecoveryAt(
        makeAccount({
          windows: [
            makeWindow({
              pool: "标准",
              used_percent: 100,
              reset_at: "2026-10-20T00:00:00Z",
            }),
            makeWindow({ pool: "标准", used_percent: 100 }),
          ],
        }),
      ),
    ).toBeNull();
  });

  it("一池算不出、一池算得出时取算得出的", () => {
    const account = makeAccount({
      windows: [
        makeWindow({ pool: "标准", used_percent: 100 }),
        makeWindow({
          pool: "Core",
          used_percent: 100,
          reset_at: "2026-10-20T00:00:00Z",
        }),
      ],
    });
    expect(accountRecoveryAt(account)).toBe("2026-10-20T00:00:00Z");
  });

  it("所有池都算不出时为 null", () => {
    expect(
      accountRecoveryAt(
        makeAccount({ windows: [makeWindow({ used_percent: 100 })] }),
      ),
    ).toBeNull();
    expect(
      accountRecoveryAt(
        makeAccount({
          windows: [
            makeWindow({ used_percent: 100, reset_at: "bad" }),
            makeWindow({
              pool: "Core",
              used_percent: 100,
              reset_at: "also-bad",
            }),
          ],
        }),
      ),
    ).toBeNull();
  });
});

describe("account sorting", () => {
  it("可用段按有效剩余降序，算不出剩余的排最后", () => {
    const low = makeAccount({
      id: "low",
      windows: [makeWindow({ used_percent: 90 })],
    });
    const high = makeAccount({
      id: "high",
      windows: [makeWindow({ used_percent: 10 })],
    });
    const unknown = makeAccount({ id: "unknown", windows: [] });

    expect(
      sortUsableAccounts([low, unknown, high]).map((a) => a.id),
    ).toEqual(["high", "low", "unknown"]);
  });

  it("已用尽段按恢复时间升序，未知的排最后", () => {
    const later = makeAccount({
      id: "later",
      windows: [
        makeWindow({ used_percent: 100, reset_at: "2026-11-01T00:00:00Z" }),
      ],
    });
    const sooner = makeAccount({
      id: "sooner",
      windows: [
        makeWindow({ used_percent: 100, reset_at: "2026-10-20T00:00:00Z" }),
      ],
    });
    const unknown = makeAccount({
      id: "unknown",
      windows: [makeWindow({ used_percent: 100 })],
    });

    expect(
      sortExhaustedAccounts([later, unknown, sooner]).map((a) => a.id),
    ).toEqual(["sooner", "later", "unknown"]);
  });

  it("已用尽段按时间戳排序：RFC3339Nano 小数秒不走字符串序", () => {
    // ".5Z" 字符串序在 "Z" 之前，但时间上晚 0.5 秒
    const frac = makeAccount({
      id: "frac",
      windows: [
        makeWindow({
          used_percent: 100,
          reset_at: "2026-10-20T00:00:00.5Z",
        }),
      ],
    });
    const plain = makeAccount({
      id: "plain",
      windows: [
        makeWindow({
          used_percent: 100,
          reset_at: "2026-10-20T00:00:00Z",
        }),
      ],
    });

    expect(sortExhaustedAccounts([frac, plain]).map((a) => a.id)).toEqual([
      "plain",
      "frac",
    ]);
  });
});

describe("formatRecoveryCountdown", () => {
  const now = new Date("2026-10-17T04:00:00Z").getTime();

  it("与重置倒计时同一粒度，动词为恢复", () => {
    expect(formatRecoveryCountdown("2026-10-17T04:45:00Z", now)).toBe(
      "45 分钟后恢复",
    );
    expect(formatRecoveryCountdown("2026-10-19T09:00:00Z", now)).toBe(
      "2 天 5 小时后恢复",
    );
    expect(formatRecoveryCountdown("2026-10-20T04:00:00Z", now)).toBe(
      "3 天后恢复",
    );
  });

  it("已过期或无法解析显示即将恢复", () => {
    expect(formatRecoveryCountdown("2026-10-17T03:00:00Z", now)).toBe(
      "即将恢复",
    );
    expect(formatRecoveryCountdown("not-a-date", now)).toBe("即将恢复");
  });
});

describe("quotaWindowUsageText", () => {
  it("used/limit 齐全时输出「9.7 / 10」，按 unit 带单位", () => {
    expect(
      quotaWindowUsageText(
        makeWindow({ used: 9.7, limit: 10, unit: "count" }),
      ),
    ).toBe("9.7 / 10");
    expect(
      quotaWindowUsageText(
        makeWindow({ used: 38, limit: 100, unit: "usd" }),
      ),
    ).toBe("$38 / $100");
  });

  it("只有 limit 时输出「限额 N」，两者都缺或只有 used 时为 null", () => {
    expect(
      quotaWindowUsageText(makeWindow({ limit: 400, unit: "count" })),
    ).toBe("限额 400");
    expect(quotaWindowUsageText(makeWindow({ used: 9.7 }))).toBeNull();
    expect(quotaWindowUsageText(makeWindow())).toBeNull();
  });
});
