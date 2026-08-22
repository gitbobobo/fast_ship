import { describe, expect, it } from "vitest";
import {
  buildUpsertShipHookPayload,
  DEFAULT_SHIP_HOOK_COMMENT_TEMPLATE,
  formatShipHookActionSummary,
  formatShipHookActionResultStatus,
  formatShipSuccessToast,
  shipHookActionLabel,
  shipHookBadge,
  shipHookToFormState,
  validateShipHookForm,
} from "./issue-ship-hook";

describe("shipHookBadge", () => {
  it("returns pending for pending hooks", () => {
    expect(
      shipHookBadge({
        status: "pending",
        comment_enabled: true,
        comment_body: "已随 {version} 发出。",
        close_enabled: true,
        workflow_enabled: false,
        workflow_status: "",
      }),
    ).toBe("pending");
  });

  it("returns failed when any fired action failed", () => {
    expect(
      shipHookBadge({
        status: "fired",
        comment_enabled: true,
        close_enabled: true,
        workflow_enabled: false,
        workflow_status: "",
        version_number: "1.2.0",
        results: {
          comment: { ok: true },
          close: { ok: false, error: "already closed" },
        },
      }),
    ).toBe("failed");
  });

  it("returns null for successful fired hooks", () => {
    expect(
      shipHookBadge({
        status: "fired",
        comment_enabled: true,
        close_enabled: true,
        workflow_enabled: false,
        workflow_status: "",
        version_number: "1.2.0",
        results: {
          comment: { ok: true },
          close: { ok: true, skipped: true },
        },
      }),
    ).toBeNull();
  });
});

describe("formatShipHookActionSummary", () => {
  it("joins selected actions with Chinese labels", () => {
    expect(
      formatShipHookActionSummary({
        comment: true,
        close: true,
        workflow_enabled: true,
        workflow_status: "done",
      }),
    ).toBe("发评论、关闭、内部状态=已完成");
  });

  it("omits workflow part when workflow is disabled", () => {
    expect(
      formatShipHookActionSummary({
        comment: false,
        close: true,
        workflow_enabled: false,
        workflow_status: "done",
      }),
    ).toBe("关闭");
  });

  it("labels empty workflow status as unset", () => {
    expect(
      formatShipHookActionSummary({
        workflow_enabled: true,
        workflow_status: "",
      }),
    ).toBe("内部状态=未设置");
  });
});

describe("shipHookToFormState", () => {
  it("maps explicit action booleans into form state", () => {
    expect(
      shipHookToFormState({
        status: "pending",
        comment_enabled: true,
        comment_body: "已随 {version} 发出。",
        close_enabled: false,
        workflow_enabled: true,
        workflow_status: "done",
      }),
    ).toEqual({
      commentEnabled: true,
      closeEnabled: false,
      workflowEnabled: true,
      workflowStatus: "done",
      commentBody: "已随 {version} 发出。",
    });
  });

  it("falls back to default comment template when comment body is absent", () => {
    expect(
      shipHookToFormState({
        status: "pending",
        comment_enabled: false,
        close_enabled: true,
        workflow_enabled: false,
        workflow_status: "",
      }),
    ).toEqual({
      commentEnabled: false,
      closeEnabled: true,
      workflowEnabled: false,
      workflowStatus: "",
      commentBody: DEFAULT_SHIP_HOOK_COMMENT_TEMPLATE,
    });
  });
});

describe("shipHookActionLabel", () => {
  it("maps action keys to labels", () => {
    expect(shipHookActionLabel("comment")).toBe("发评论");
    expect(shipHookActionLabel("close")).toBe("关闭");
    expect(shipHookActionLabel("workflow_status")).toBe("内部状态");
  });
});

describe("formatShipHookActionResultStatus", () => {
  it("describes success, skipped and failed states", () => {
    expect(formatShipHookActionResultStatus({ ok: true })).toBe("成功");
    expect(formatShipHookActionResultStatus({ ok: true, skipped: true })).toBe(
      "跳过",
    );
    expect(
      formatShipHookActionResultStatus({ ok: false, error: "boom" }),
    ).toBe("失败");
  });
});

describe("buildUpsertShipHookPayload", () => {
  it("omits unchecked actions", () => {
    expect(
      buildUpsertShipHookPayload({
        commentEnabled: false,
        closeEnabled: true,
        workflowEnabled: false,
        workflowStatus: "done",
        commentBody: DEFAULT_SHIP_HOOK_COMMENT_TEMPLATE,
      }),
    ).toEqual({ close: true });
  });

  it("returns null when no actions selected", () => {
    expect(
      buildUpsertShipHookPayload({
        commentEnabled: false,
        closeEnabled: false,
        workflowEnabled: false,
        workflowStatus: "done",
        commentBody: DEFAULT_SHIP_HOOK_COMMENT_TEMPLATE,
      }),
    ).toBeNull();
  });
});

describe("validateShipHookForm", () => {
  it("rejects empty comment when comment is enabled", () => {
    expect(
      validateShipHookForm({
        commentEnabled: true,
        commentBody: "   ",
        closeEnabled: false,
        workflowEnabled: false,
      }),
    ).toBe("评论内容不能为空");
  });
});

describe("formatShipSuccessToast", () => {
  it("formats hook counts into success messages", () => {
    expect(formatShipSuccessToast({ hook_total: 0, hook_failed: 0 })).toBe(
      "发货成功！",
    );
    expect(formatShipSuccessToast({ hook_total: 2, hook_failed: 0 })).toBe(
      "已发货，并执行了 2 个问题钩子",
    );
    expect(formatShipSuccessToast({ hook_total: 2, hook_failed: 1 })).toBe(
      "已发货，并执行了 2 个问题钩子，其中 1 个有失败，请打开问题详情",
    );
  });
});
