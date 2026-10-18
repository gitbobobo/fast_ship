import { describe, expect, it } from "vitest";
import {
  canProbe,
  collectCredentialValues,
  formatQuotaAmount,
  formatResetCountdown,
  hasAnyCredentialValue,
  missingRequiredCredentialFields,
  quotaUsageTone,
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
