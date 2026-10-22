import { describe, expect, it, vi, afterEach } from "vitest";
import {
  browserTimezone,
  formatTimeInZone,
  syncBrowserTimezone,
  timezoneOptions,
} from "@/lib/utils/timezone";
import { authApi } from "@/lib/api/auth";

vi.mock("@/lib/api/auth", () => ({
  authApi: { updateMe: vi.fn() },
}));

function fakeUser(overrides: Partial<User> = {}): User {
  return {
    id: "user-1",
    username: "godbobo",
    email: "godbobo@example.com",
    avatar_url: "",
    role: "member",
    timezone: "",
    created_at: "2026-04-06T10:00:00Z",
    updated_at: "2026-04-06T10:00:00Z",
    ...overrides,
  };
}

describe("browserTimezone", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("返回 Intl 解析出的浏览器时区", () => {
    expect(browserTimezone()).toBe(
      Intl.DateTimeFormat().resolvedOptions().timeZone,
    );
  });

  it("Intl 抛错时返回空串而不是抛错", () => {
    vi.spyOn(Intl, "DateTimeFormat").mockImplementation(() => {
      throw new Error("Intl 不可用");
    });

    expect(browserTimezone()).toBe("");
  });
});

describe("syncBrowserTimezone", () => {
  afterEach(() => {
    vi.clearAllMocks();
    vi.restoreAllMocks();
  });

  it("用户已声明时区时不发请求，原样返回", async () => {
    const user = fakeUser({ timezone: "Asia/Shanghai" });

    await expect(syncBrowserTimezone(user)).resolves.toBe(user);
    expect(authApi.updateMe).not.toHaveBeenCalled();
  });

  it("时区为空时只把浏览器时区写到 PUT auth/me", async () => {
    vi.spyOn(
      Intl.DateTimeFormat.prototype,
      "resolvedOptions",
    ).mockReturnValue({
      timeZone: "Asia/Shanghai",
    } as Intl.ResolvedDateTimeFormatOptions);
    const updated = fakeUser({ timezone: "Asia/Shanghai" });
    vi.mocked(authApi.updateMe).mockResolvedValue({
      data: updated,
    } as Awaited<ReturnType<typeof authApi.updateMe>>);

    await expect(syncBrowserTimezone(fakeUser())).resolves.toBe(updated);
    expect(authApi.updateMe).toHaveBeenCalledTimes(1);
    expect(authApi.updateMe).toHaveBeenCalledWith({ timezone: "Asia/Shanghai" });
  });

  it("写入失败时静默返回原 user", async () => {
    const user = fakeUser();
    vi.mocked(authApi.updateMe).mockRejectedValue(new Error("网络异常"));

    await expect(syncBrowserTimezone(user)).resolves.toBe(user);
  });
});

describe("timezoneOptions", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("supportedValuesOf 不含 UTC 时也把 UTC 补进候选", () => {
    // 真实环境里 supportedValuesOf("timeZone") 只列地区时区，不含 UTC；
    // 「跟随部署配置」不能代替显式 UTC。
    vi.spyOn(Intl, "supportedValuesOf").mockReturnValue([
      "Asia/Tokyo",
      "Asia/Shanghai",
    ]);

    const values = timezoneOptions("").map((o) => o.value);

    expect(values).toContain("UTC");
    expect(values.filter((v) => v === "UTC")).toHaveLength(1);
  });

  it("剔除历史别名，并让常用时区排在字母序之前", () => {
    vi.spyOn(Intl, "supportedValuesOf").mockReturnValue([
      "Africa/Abidjan",
      "Asia/Calcutta",
      "Asia/Rangoon",
      "Asia/Saigon",
      "Asia/Shanghai",
      "Asia/Tokyo",
      "UTC",
      "Zulu",
    ]);

    const values = timezoneOptions("").map((o) => o.value);

    for (const legacy of ["Asia/Calcutta", "Asia/Rangoon", "Asia/Saigon"]) {
      expect(values).not.toContain(legacy);
    }
    // 常用时区在字母序之前，字母序部分保持原顺序。
    expect(values.indexOf("Asia/Tokyo")).toBeLessThan(
      values.indexOf("Africa/Abidjan"),
    );
    expect(values.indexOf("Asia/Shanghai")).toBeLessThan(
      values.indexOf("Africa/Abidjan"),
    );
    expect(values.filter((v) => v === "UTC")).toHaveLength(1);
  });

  it("当前值是历史别名时仍然可选（插到首位）", () => {
    vi.spyOn(Intl, "supportedValuesOf").mockReturnValue([
      "Asia/Shanghai",
      "Asia/Saigon",
    ]);

    const values = timezoneOptions("Asia/Saigon").map((o) => o.value);

    // 首位是「常用时区」分组标题，当前值紧随其后。
    expect(values[1]).toBe("Asia/Saigon");
  });

  it("当前值不在候选里时插到首位，并带上 UTC 偏移", () => {
    vi.spyOn(Intl, "supportedValuesOf").mockReturnValue(["UTC", "Asia/Tokyo"]);

    const options = timezoneOptions("Asia/Shanghai");

    expect(options[1]).toEqual({
      value: "Asia/Shanghai",
      label: "Asia/Shanghai (UTC+8)",
    });
    expect(options.map((o) => o.value)).toContain("Asia/Tokyo");
  });

  it("supportedValuesOf 不可用时退回内置常用时区", () => {
    Object.defineProperty(Intl, "supportedValuesOf", {
      value: undefined,
      configurable: true,
      writable: true,
    });

    const values = timezoneOptions("").map((o) => o.value);

    for (const zone of [
      "Asia/Shanghai",
      "Asia/Tokyo",
      "Asia/Singapore",
      "Europe/London",
      "Europe/Berlin",
      "America/New_York",
      "America/Los_Angeles",
      "UTC",
    ]) {
      expect(values).toContain(zone);
    }
  });
});

describe("formatTimeInZone", () => {
  it("按目标时区格式化时间并带偏移", () => {
    expect(
      formatTimeInZone(new Date("2026-10-22T05:05:00Z"), "Asia/Shanghai"),
    ).toBe("2026-10-22 13:05 (UTC+8)");
  });
});
