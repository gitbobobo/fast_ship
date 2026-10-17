import { describe, expect, it } from "vitest";
import { PROVIDER_ICONS, providerIconKey, resolveProviderIcon } from "./provider-icons";

describe("providerIconKey", () => {
  it.each([
    ["cursor", "cursor"],
    ["codex", "codex"],
    ["codex_work", "codex"],
    ["claudeAgent", "claude"],
    ["claudeAgent_2", "claude"],
    ["acpRegistry_devin", "acp"],
    ["opencode", "opencode"],
    ["grok", "grok"],
    ["muse", "muse"],
    ["pi", "pi"],
    ["pi_2", "pi"],
  ])("maps %s to %s", (id, key) => {
    expect(providerIconKey(id)).toBe(key);
  });

  it("returns undefined for unknown or empty instances", () => {
    expect(providerIconKey("mystery")).toBeUndefined();
    expect(providerIconKey("pilot")).toBeUndefined();
    expect(providerIconKey("")).toBeUndefined();
  });
});

describe("resolveProviderIcon", () => {
  it("ACP 接入的 harness 用注册表官方图标", () => {
    expect(resolveProviderIcon("acpRegistry_devin")).toEqual({
      key: "acp:devin",
      icon: expect.any(Object),
    });
    // 实例后缀是 agent id 把 - 换成 _，解析时还原。
    expect(resolveProviderIcon("acpRegistry_factory_droid")?.key).toBe(
      "acp:factory-droid",
    );
    expect(resolveProviderIcon("acpRegistry_kilo_code")?.key).toBe(
      "acp:kilo-code",
    );
  });

  it("每个注册表 agent 都有图标，未登记时才回落通用 acp", () => {
    expect(resolveProviderIcon("acpRegistry_gemini")?.icon).not.toBe(
      PROVIDER_ICONS.acp,
    );
    expect(resolveProviderIcon("acpRegistry_not-a-real-agent")?.icon).toBe(
      PROVIDER_ICONS.acp,
    );
  });

  it("非 ACP 实例走内置映射，未知实例返回 null", () => {
    expect(resolveProviderIcon("codex_work")?.key).toBe("codex");
    expect(resolveProviderIcon("claudeAgent")?.icon).toBe(
      PROVIDER_ICONS.claude,
    );
    expect(resolveProviderIcon("mystery")).toBeNull();
    expect(resolveProviderIcon("")).toBeNull();
  });
});
