import { describe, expect, it } from "vitest";
import { providerIconKey } from "./provider-icons";

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
