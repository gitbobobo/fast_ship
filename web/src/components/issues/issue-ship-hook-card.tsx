import { useState } from "react";
import { ChevronDown, ChevronUp, Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import {
  useDeleteIssueShipHook,
  useUpsertIssueShipHook,
} from "@/lib/hooks/use-issues";
import {
  buildUpsertShipHookPayload,
  defaultShipHookFormState,
  formatShipHookActionResultStatus,
  formatShipHookActionSummary,
  shipHookActionLabel,
  shipHookToFormState,
  validateShipHookForm,
} from "@/lib/issue-ship-hook";
import {
  ISSUE_WORKFLOW_STATUS_LABELS,
  ISSUE_WORKFLOW_STATUS_SELECT_OPTIONS,
  type IssueWorkflowStatus,
} from "@/lib/issue-workflow-status";
import { formatDate } from "@/lib/utils/format";
import { cn } from "@/lib/utils";
import { toast } from "sonner";

interface IssueShipHookCardProps {
  issueId: string;
  projectId: string;
  shipHook?: IssueShipHook | null;
}

type ShipHookFormState = ReturnType<typeof defaultShipHookFormState>;

function ShipHookCheckbox({
  checked,
  disabled,
  label,
  onChange,
}: {
  checked: boolean;
  disabled?: boolean;
  label: string;
  onChange: (checked: boolean) => void;
}) {
  return (
    <Label className="items-start gap-2 font-normal text-sm text-foreground">
      <input
        type="checkbox"
        checked={checked}
        disabled={disabled}
        onChange={(event) => onChange(event.target.checked)}
        className="mt-0.5 h-4 w-4 shrink-0 rounded border border-input accent-primary"
      />
      <span>{label}</span>
    </Label>
  );
}

function ShipHookCommentPreview({ body }: { body: string }) {
  const [expanded, setExpanded] = useState(false);
  const shouldCollapse = body.length > 120;

  return (
    <div className="space-y-1">
      <p className="text-xs font-medium text-muted-foreground">评论预览</p>
      <p
        className={cn(
          "text-sm text-muted-foreground",
          !expanded && shouldCollapse && "line-clamp-3",
        )}
      >
        {body}
      </p>
      {shouldCollapse && (
        <Button
          type="button"
          variant="ghost"
          size="sm"
          className="h-7 px-2 text-xs text-muted-foreground"
          onClick={() => setExpanded((value) => !value)}
        >
          {expanded ? (
            <>
              <ChevronUp className="mr-1 h-3 w-3" />
              收起
            </>
          ) : (
            <>
              <ChevronDown className="mr-1 h-3 w-3" />
              展开
            </>
          )}
        </Button>
      )}
    </div>
  );
}

function ShipHookEditForm({
  initialState,
  isSaving,
  onCancel,
  onSave,
}: {
  initialState: ShipHookFormState;
  isSaving: boolean;
  onCancel: () => void;
  onSave: (state: ShipHookFormState) => void;
}) {
  const [commentEnabled, setCommentEnabled] = useState(initialState.commentEnabled);
  const [closeEnabled, setCloseEnabled] = useState(initialState.closeEnabled);
  const [workflowEnabled, setWorkflowEnabled] = useState(
    initialState.workflowEnabled,
  );
  const [workflowStatus, setWorkflowStatus] = useState<IssueWorkflowStatus>(
    initialState.workflowStatus,
  );
  const [commentBody, setCommentBody] = useState(initialState.commentBody);

  const handleSave = () => {
    const formState = {
      commentEnabled,
      closeEnabled,
      workflowEnabled,
      workflowStatus,
      commentBody,
    };
    const validationError = validateShipHookForm(formState);
    if (validationError) {
      toast.error(validationError);
      return;
    }

    onSave(formState);
  };

  return (
    <div className="space-y-3">
      <div className="space-y-2">
        <ShipHookCheckbox
          checked={commentEnabled}
          disabled={isSaving}
          label="发评论"
          onChange={setCommentEnabled}
        />
        <ShipHookCheckbox
          checked={closeEnabled}
          disabled={isSaving}
          label="关闭问题"
          onChange={setCloseEnabled}
        />
        <ShipHookCheckbox
          checked={workflowEnabled}
          disabled={isSaving}
          label="调整内部状态"
          onChange={setWorkflowEnabled}
        />
      </div>

      {commentEnabled && (
        <div className="space-y-1.5">
          <Label className="text-xs text-muted-foreground">评论内容</Label>
          <Textarea
            value={commentBody}
            disabled={isSaving}
            rows={3}
            onChange={(event) => setCommentBody(event.target.value)}
            className="min-h-20 text-sm"
          />
        </div>
      )}

      {workflowEnabled && (
        <div className="space-y-1.5">
          <Label className="text-xs text-muted-foreground">目标内部状态</Label>
          <Select
            value={workflowStatus === "" ? "unset" : workflowStatus}
            disabled={isSaving}
            onValueChange={(value) =>
              setWorkflowStatus(
                !value || value === "unset"
                  ? ""
                  : (value as IssueWorkflowStatus),
              )
            }
          >
            <SelectTrigger className="h-8 text-sm">
              <SelectValue>
                {ISSUE_WORKFLOW_STATUS_LABELS[workflowStatus]}
              </SelectValue>
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="unset">未设置</SelectItem>
              {ISSUE_WORKFLOW_STATUS_SELECT_OPTIONS.map((option) => (
                <SelectItem key={option.value} value={option.value}>
                  {option.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      )}

      <div className="flex items-center gap-2">
        <Button size="sm" disabled={isSaving} onClick={handleSave}>
          {isSaving ? (
            <>
              <Loader2 className="mr-1 h-3.5 w-3.5 animate-spin" />
              保存中
            </>
          ) : (
            "保存"
          )}
        </Button>
        <Button
          size="sm"
          variant="ghost"
          disabled={isSaving}
          onClick={onCancel}
        >
          取消
        </Button>
      </div>
    </div>
  );
}

function ShipHookFiredView({ hook }: { hook: IssueShipHook }) {
  const actions: Array<{
    key: "comment" | "close" | "workflow_status";
    result: IssueShipHookActionResult;
  }> = [];

  if (hook.results?.comment) {
    actions.push({ key: "comment", result: hook.results.comment });
  }
  if (hook.results?.close) {
    actions.push({ key: "close", result: hook.results.close });
  }
  if (hook.results?.workflow_status) {
    actions.push({
      key: "workflow_status",
      result: hook.results.workflow_status,
    });
  }

  return (
    <div className="space-y-3">
      <div className="space-y-1 text-sm text-muted-foreground">
        {hook.version_number && (
          <p>
            版本：
            {hook.release_url ? (
              <a
                href={hook.release_url}
                target="_blank"
                rel="noreferrer"
                className="text-primary hover:underline"
              >
                {hook.version_number}
              </a>
            ) : (
              <span>{hook.version_number}</span>
            )}
          </p>
        )}
        {hook.fired_at && <p>执行时间：{formatDate(hook.fired_at)}</p>}
      </div>

      {actions.length > 0 && (
        <div className="space-y-2">
          {actions.map(({ key, result }) => (
            <div
              key={key}
              className="rounded-md border px-3 py-2 text-sm"
            >
              <div className="flex items-center justify-between gap-2">
                <span>{shipHookActionLabel(key)}</span>
                <span
                  className={cn(
                    "text-xs font-medium",
                    result.ok
                      ? "text-emerald-600 dark:text-emerald-400"
                      : "text-destructive",
                  )}
                >
                  {formatShipHookActionResultStatus(result)}
                </span>
              </div>
              {!result.ok && result.error && (
                <p className="mt-1 text-xs text-destructive">{result.error}</p>
              )}
            </div>
          ))}
        </div>
      )}

      {hook.comment_body && (
        <div className="space-y-1">
          <p className="text-xs font-medium text-muted-foreground">已发评论</p>
          <p className="text-sm text-muted-foreground">{hook.comment_body}</p>
        </div>
      )}
    </div>
  );
}

export function IssueShipHookCard({
  issueId,
  projectId,
  shipHook,
}: IssueShipHookCardProps) {
  const upsertShipHook = useUpsertIssueShipHook(issueId, projectId);
  const deleteShipHook = useDeleteIssueShipHook(issueId, projectId);
  const [editState, setEditState] = useState<ShipHookFormState | null>(null);
  const editing = editState !== null;
  const isSaving = upsertShipHook.isPending || deleteShipHook.isPending;

  const startEditing = (state = defaultShipHookFormState()) => {
    setEditState(state);
  };

  const handleSave = async (formState: ShipHookFormState) => {
    const payload = buildUpsertShipHookPayload(formState);
    if (!payload) {
      return;
    }

    try {
      await upsertShipHook.mutateAsync(payload);
      setEditState(null);
      toast.success("已保存下次发货后动作");
    } catch {
      toast.error("保存失败");
    }
  };

  const handleCancelHook = async () => {
    try {
      await deleteShipHook.mutateAsync();
      setEditState(null);
      toast.success("已取消");
    } catch {
      toast.error("取消失败");
    }
  };

  const pendingSummary =
    shipHook?.status === "pending"
      ? formatShipHookActionSummary({
          comment: shipHook.comment_enabled,
          close: shipHook.close_enabled,
          workflow_enabled: shipHook.workflow_enabled,
          workflow_status: shipHook.workflow_status,
        })
      : "";
  const isRunning = shipHook?.status === "running";

  return (
    <Card>
      <CardHeader className="pb-0 pt-4">
        <div className="flex items-start justify-between gap-2">
          <CardTitle className="text-sm">下次发货后</CardTitle>

          {!editing && shipHook?.status === "pending" && (
            <div className="flex items-center gap-1">
              <Button
                size="sm"
                variant="ghost"
                className="h-7 px-2 text-xs"
                disabled={isSaving}
                onClick={() => startEditing(shipHookToFormState(shipHook))}
              >
                修改
              </Button>
              <Button
                size="sm"
                variant="ghost"
                className="h-7 px-2 text-xs text-destructive hover:text-destructive"
                disabled={isSaving}
                onClick={() => void handleCancelHook()}
              >
                取消
              </Button>
            </div>
          )}

          {!editing && !shipHook && (
            <Button
              size="sm"
              variant="ghost"
              className="h-7 px-2 text-xs"
              onClick={() => startEditing()}
            >
              设置
            </Button>
          )}

          {!editing && shipHook?.status === "fired" && (
            <Button
              size="sm"
              variant="ghost"
              className="h-7 px-2 text-xs"
              disabled={isSaving}
              onClick={() => startEditing()}
            >
              再次设置
            </Button>
          )}
        </div>
      </CardHeader>
      <CardContent className="space-y-3 p-4 pt-3">
        {editing && editState ? (
          <ShipHookEditForm
            initialState={editState}
            isSaving={isSaving}
            onCancel={() => setEditState(null)}
            onSave={(state) => void handleSave(state)}
          />
        ) : shipHook?.status === "pending" || isRunning ? (
          <div className="space-y-2">
            <p className="text-sm text-muted-foreground">
              {isRunning ? "正在执行发货后动作，请稍候" : pendingSummary}
            </p>
            {shipHook.comment_body && (
              <ShipHookCommentPreview body={shipHook.comment_body} />
            )}
          </div>
        ) : shipHook?.status === "fired" ? (
          <ShipHookFiredView hook={shipHook} />
        ) : (
          <p className="text-sm text-muted-foreground">
            该项目下一次成功发货时执行
          </p>
        )}
      </CardContent>
    </Card>
  );
}
