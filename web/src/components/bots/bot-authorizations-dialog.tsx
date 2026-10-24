import { KeyRound, ShieldCheck, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Skeleton } from "@/components/ui/skeleton";
import {
  useBotAuthorizations,
  useBotPolicyRules,
  useRevokeAuthorization,
  useSetPolicyRuleEnabled,
} from "@/lib/hooks/use-bots";
import { ApiRequestError } from "@/lib/api/client";
import { expiryDisplay, formatDate, formatRelativeTime } from "@/lib/utils/format";
import { cn } from "@/lib/utils";

/** 授权范围的可读标签；未知 scope_kind 原样展示，不猜语义。 */
function scopeLabel(a: {
  scope_kind: string;
  scope_id: string;
  environment_id?: string | null;
}) {
  const parts = [`${a.scope_kind}：${a.scope_id}`];
  if (a.environment_id) parts.push(`环境 ${a.environment_id}`);
  return parts.join(" · ");
}

/** constraints_json 分解展示：fields/param/exec_config 有专属行，
 * 其余未知键兜底按键名拼接，不静默吞掉。 */
function constraintView(constraints?: Record<string, unknown>) {
  const c = constraints ?? {};
  const fields = Array.isArray(c.fields) ? c.fields.map(String) : null;
  const exec = c.exec_config !== undefined ? JSON.stringify(c.exec_config) : null;
  const param = exec === null && typeof c.param === "string" ? c.param : null;
  const others = Object.keys(c).filter(
    (k) => k !== "fields" && k !== "param" && k !== "exec_config",
  );
  return { fields, exec, param, others: others.length > 0 ? others.join("、") : null };
}

/**
 * 分级放行规则开关行（INT-160）：tier1 规则的独立开关与误判状态。
 * 误判自动降级（24 小时内 2 次用户反向操作）也会写到这里；手动重开
 * 会清自动降级痕迹与误判计数。
 */
function PolicyRuleRow({
  rule,
  pending,
  onToggle,
}: {
  rule: {
    rule_id: string;
    name: string;
    description: string;
    enabled: boolean;
    false_positive_count_24h: number;
    auto_disabled_at?: string | null;
  };
  pending: boolean;
  onToggle: (ruleId: string, enabled: boolean) => void;
}) {
  return (
    <li
      data-testid="bot-policy-rule-item"
      className="space-y-1 rounded-lg border px-3 py-2.5 text-sm"
    >
      <div className="flex items-center gap-2">
        <span className="flex items-center gap-1.5 font-medium">
          <ShieldCheck className="h-3.5 w-3.5 text-muted-foreground" />
          {rule.name}
        </span>
        <button
          type="button"
          role="switch"
          aria-checked={rule.enabled}
          aria-label={`开关规则 ${rule.name}`}
          disabled={pending}
          onClick={() => onToggle(rule.rule_id, !rule.enabled)}
          className={cn(
            "relative ml-auto inline-flex h-5 w-9 shrink-0 items-center rounded-full outline-none transition-colors",
            "focus-visible:ring-3 focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50",
            rule.enabled ? "bg-primary" : "bg-input dark:bg-input/60",
          )}
        >
          <span
            className={cn(
              "block h-4 w-4 rounded-full bg-background shadow-sm transition-transform",
              rule.enabled ? "translate-x-[18px]" : "translate-x-0.5",
            )}
          />
        </button>
      </div>
      <p className="text-xs text-muted-foreground">{rule.description}</p>
      {!rule.enabled && rule.auto_disabled_at && (
        <p className="text-xs text-amber-700 dark:text-amber-400">
          已因 24 小时内累计 2 次误判自动降级（{formatRelativeTime(rule.auto_disabled_at)}）；
          重新开启会清零误判计数。
        </p>
      )}
      {rule.enabled && rule.false_positive_count_24h > 0 && (
        <p className="text-xs text-muted-foreground">
          24 小时内误判 {rule.false_positive_count_24h}/2 次；再累计 1 次将自动降级回确认。
        </p>
      )}
    </li>
  );
}

/**
 * 授权管理弹窗：上半区是每条「始终允许」的 action、范围、字段约束与
 * 有效期；下半区是分级放行（INT-160）tier1 规则的独立开关与误判状态。
 * 撤销授权写 revoked_at 保留审计关联，对已开始的操作不回滚。
 */
export function BotAuthorizationsDialog({
  botId,
  open,
  onClose,
}: {
  botId: string;
  open: boolean;
  onClose: () => void;
}) {
  const { data: authorizations, isLoading } = useBotAuthorizations(botId);
  const revoke = useRevokeAuthorization(botId);
  const { data: policyRules, isLoading: rulesLoading } =
    useBotPolicyRules(botId);
  const toggleRule = useSetPolicyRuleEnabled(botId);

  const handleRevoke = async (id: string) => {
    try {
      await revoke.mutateAsync(id);
      toast.success("授权已撤销");
    } catch (err) {
      toast.error(
        err instanceof ApiRequestError && err.message ? err.message : "撤销失败",
      );
    }
  };

  const handleToggleRule = async (ruleId: string, enabled: boolean) => {
    try {
      await toggleRule.mutateAsync({ ruleId, enabled });
      toast.success(enabled ? "规则已开启" : "规则已关闭");
    } catch (err) {
      toast.error(
        err instanceof ApiRequestError && err.message ? err.message : "开关失败",
      );
    }
  };

  const active = (authorizations ?? []).filter((a) => !a.revoked_at);

  return (
    <Dialog open={open} onOpenChange={(next) => !next && onClose()}>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>授权管理</DialogTitle>
          <DialogDescription>
            「始终允许」建立的精确授权，现在限时 30 天。撤销后未开始的调用回到待确认，
            已开始的操作不回滚。
          </DialogDescription>
        </DialogHeader>
        {isLoading ? (
          <div className="space-y-2 py-2">
            <Skeleton className="h-12 rounded-lg" />
            <Skeleton className="h-12 rounded-lg" />
          </div>
        ) : active.length === 0 ? (
          <p className="flex items-center justify-center gap-2 py-8 text-center text-sm text-muted-foreground">
            <KeyRound className="h-4 w-4" />
            暂无有效授权
          </p>
        ) : (
          <ul className="space-y-2">
            {active.map((a) => {
              const { fields, exec, param, others } = constraintView(a.constraints);
              const expiry = expiryDisplay(a.expires_at);
              return (
                <li
                  key={a.id}
                  data-testid="bot-authorization-item"
                  className="space-y-1 rounded-lg border px-3 py-2.5 text-sm"
                >
                  <div className="flex items-center gap-2">
                    <span className="font-medium">
                      {a.tool_name}.{a.action}
                    </span>
                    {/* 已过期等同已撤销，再挂「撤销」按钮与过期说明自相矛盾。 */}
                    {!expiry.expired && (
                      <Button
                        type="button"
                        size="xs"
                        variant="ghost"
                        className="ml-auto text-destructive"
                        disabled={revoke.isPending}
                        aria-label={`撤销 ${a.tool_name}.${a.action} 授权`}
                        onClick={() => void handleRevoke(a.id)}
                      >
                        <Trash2 className="h-3.5 w-3.5" />
                        撤销
                      </Button>
                    )}
                  </div>
                  <p className="text-xs text-muted-foreground">
                    {scopeLabel(a)}
                  </p>
                  {fields && (
                    <p className="text-xs text-muted-foreground">
                      允许写入字段：{fields.join("、")}
                    </p>
                  )}
                  {exec && (
                    <p className="break-all font-mono text-xs text-muted-foreground">
                      执行配置：{exec}
                    </p>
                  )}
                  {param && (
                    <p className="text-xs text-muted-foreground">
                      参数约束：{param}
                    </p>
                  )}
                  {others && (
                    <p className="text-xs text-muted-foreground">
                      约束：{others}
                    </p>
                  )}
                  <p className="text-xs text-muted-foreground">
                    创建于 {formatDate(a.created_at)}
                    {!expiry.expired && expiry.label &&
                      ` · ${expiry.label}过期`}
                  </p>
                  {expiry.expired && (
                    <p className="text-xs text-amber-700 dark:text-amber-400">
                      已过期：不再命中，等同已撤销（历史记录保留）。
                    </p>
                  )}
                </li>
              );
            })}
          </ul>
        )}
        {/* 分级放行规则（INT-160）：分档与前置条件都在服务端，这里只开关。 */}
        <div className="space-y-2 border-t pt-3">
          <h4 className="text-xs font-medium text-muted-foreground">
            分级放行规则
            <span className="font-normal">
              {" · "}开启后满足前置条件的动作自动执行，不逐次确认
            </span>
          </h4>
          {rulesLoading ? (
            <Skeleton className="h-16 rounded-lg" />
          ) : (policyRules ?? []).length === 0 ? (
            <p className="py-2 text-center text-xs text-muted-foreground">
              暂无可配置规则
            </p>
          ) : (
            <ul className="space-y-2">
              {(policyRules ?? []).map((rule) => (
                <PolicyRuleRow
                  key={rule.rule_id}
                  rule={rule}
                  pending={toggleRule.isPending}
                  onToggle={(ruleId, enabled) =>
                    void handleToggleRule(ruleId, enabled)
                  }
                />
              ))}
            </ul>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}
