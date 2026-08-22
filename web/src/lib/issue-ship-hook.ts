import {
  ISSUE_WORKFLOW_STATUS_LABELS,
  type IssueWorkflowStatus,
} from "@/lib/issue-workflow-status";

export const DEFAULT_SHIP_HOOK_COMMENT_TEMPLATE = "已随 {version} 发出。";

export const SHIP_HOOK_COMMENT_MAX_LENGTH = 4000;

export function shipHookBadge(
  hook: IssueShipHook | null | undefined,
): "pending" | "failed" | null {
  if (!hook) {
    return null;
  }

  if (hook.status === "pending") {
    return "pending";
  }

  if (hook.status === "fired" && hook.results) {
    const { comment, close, workflow_status } = hook.results;
    if (
      comment?.ok === false ||
      close?.ok === false ||
      workflow_status?.ok === false
    ) {
      return "failed";
    }
  }

  return null;
}

export function formatShipHookActionSummary(actions: {
  comment?: boolean;
  close?: boolean;
  workflow_enabled?: boolean;
  workflow_status?: string;
}): string {
  const parts: string[] = [];

  if (actions.comment) {
    parts.push("发评论");
  }
  if (actions.close) {
    parts.push("关闭");
  }
  if (actions.workflow_enabled) {
    const label =
      ISSUE_WORKFLOW_STATUS_LABELS[
        actions.workflow_status as IssueWorkflowStatus
      ] ?? actions.workflow_status;
    parts.push(`内部状态=${label}`);
  }

  return parts.join("、");
}

export function shipHookActionLabel(
  action: "comment" | "close" | "workflow_status",
): string {
  switch (action) {
    case "comment":
      return "发评论";
    case "close":
      return "关闭";
    case "workflow_status":
      return "内部状态";
  }
}

export function formatShipHookActionResultStatus(
  result: IssueShipHookActionResult,
): string {
  if (!result.ok) {
    return "失败";
  }
  return result.skipped ? "跳过" : "成功";
}

export function defaultShipHookFormState() {
  return {
    commentEnabled: true,
    closeEnabled: true,
    workflowEnabled: true,
    workflowStatus: "done" as IssueWorkflowStatus,
    commentBody: DEFAULT_SHIP_HOOK_COMMENT_TEMPLATE,
  };
}

export function shipHookToFormState(hook: IssueShipHook) {
  return {
    commentEnabled: hook.comment_enabled,
    closeEnabled: hook.close_enabled,
    workflowEnabled: hook.workflow_enabled,
    workflowStatus: hook.workflow_status as IssueWorkflowStatus,
    commentBody: hook.comment_body ?? DEFAULT_SHIP_HOOK_COMMENT_TEMPLATE,
  };
}

export function buildUpsertShipHookPayload(form: {
  commentEnabled: boolean;
  closeEnabled: boolean;
  workflowEnabled: boolean;
  workflowStatus: IssueWorkflowStatus;
  commentBody: string;
}): {
  comment_body?: string;
  close?: boolean;
  workflow_status?: IssueWorkflowStatus;
} | null {
  const payload: {
    comment_body?: string;
    close?: boolean;
    workflow_status?: IssueWorkflowStatus;
  } = {};

  if (form.commentEnabled) {
    payload.comment_body = form.commentBody.trim();
  }
  if (form.closeEnabled) {
    payload.close = true;
  }
  if (form.workflowEnabled) {
    payload.workflow_status = form.workflowStatus;
  }

  if (
    payload.comment_body === undefined &&
    payload.close === undefined &&
    payload.workflow_status === undefined
  ) {
    return null;
  }

  return payload;
}

export function validateShipHookForm(form: {
  commentEnabled: boolean;
  commentBody: string;
  closeEnabled: boolean;
  workflowEnabled: boolean;
}): string | null {
  const hasAction =
    form.commentEnabled || form.closeEnabled || form.workflowEnabled;

  if (!hasAction) {
    return "请至少选择一项动作";
  }

  if (form.commentEnabled) {
    const trimmed = form.commentBody.trim();
    if (!trimmed) {
      return "评论内容不能为空";
    }
    if ([...trimmed].length > SHIP_HOOK_COMMENT_MAX_LENGTH) {
      return `评论内容不能超过 ${SHIP_HOOK_COMMENT_MAX_LENGTH} 个字符`;
    }
  }

  return null;
}

export function formatShipSuccessToast(result: ShipResult): string {
  const hookTotal = result.hook_total;
  const hookFailed = result.hook_failed;

  if (hookTotal === 0) {
    return "发货成功！";
  }

  if (hookFailed === 0) {
    return `已发货，并执行了 ${hookTotal} 个问题钩子`;
  }

  return `已发货，并执行了 ${hookTotal} 个问题钩子，其中 ${hookFailed} 个有失败，请打开问题详情`;
}
