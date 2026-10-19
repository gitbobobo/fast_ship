import { describe, expect, it } from "vitest";
import { safeNextPath } from "@/lib/utils/safe-next";

describe("safeNextPath", () => {
  it("accepts in-app absolute paths with query", () => {
    expect(
      safeNextPath("/settings/integrations/github/callback?code=a&state=b"),
    ).toBe("/settings/integrations/github/callback?code=a&state=b");
    expect(safeNextPath("/projects")).toBe("/projects");
  });

  it("rejects off-site and protocol-relative targets", () => {
    expect(safeNextPath(null)).toBeNull();
    expect(safeNextPath("")).toBeNull();
    expect(safeNextPath("//evil.example.com/x")).toBeNull();
    expect(safeNextPath("https://evil.example.com/x")).toBeNull();
    expect(safeNextPath("dashboard")).toBeNull();
  });
});
