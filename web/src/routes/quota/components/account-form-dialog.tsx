import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { OptionSelect } from "@/components/ui/option-select";
import { QuotaProviderIcon } from "@/components/quota/provider-icons";
import { QuotaCredentialFields } from "./credential-fields";
import { ProbeResultPanel } from "./probe-result-panel";
import {
  useCreateQuotaAccount,
  useQuotaProbe,
  useQuotaProviders,
  useUpdateQuotaAccount,
} from "@/lib/hooks/use-quota";
import {
  canProbe,
  collectCredentialValues,
  errorText,
  hasAnyCredentialValue,
  missingRequiredCredentialFields,
} from "@/lib/quota";

/**
 * 添加 / 编辑额度账号对话框：表单结构完全由供应商元数据驱动。
 * 编辑模式下凭据留空表示不修改（对话框回显 credential_hint，不回显明文）；
 * 「测试」调 /api/quota/test 就地展示结果，不落库。
 */
export function QuotaAccountFormDialog({
  open,
  onOpenChange,
  account,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** null = 添加模式；传入账号 = 编辑模式 */
  account: QuotaAccount | null;
}) {
  const { data: providers } = useQuotaProviders();
  const createAccount = useCreateQuotaAccount();
  const updateAccount = useUpdateQuotaAccount();
  const probe = useQuotaProbe();

  const editing = account !== null;
  const [providerId, setProviderId] = useState<string | null>(null);
  const [label, setLabel] = useState("");
  const [values, setValues] = useState<Record<string, string>>({});
  const [submitting, setSubmitting] = useState(false);
  const resetProbe = probe.reset;

  // 打开时从账号/空态重建表单；关闭即丢掉凭据明文与探测结果，
  // 不让用户输入留在 state 里等下一次打开。
  useEffect(() => {
    if (!open) {
      setValues({});
      resetProbe();
      return;
    }
    setProviderId(account?.provider ?? null);
    setLabel(account?.label ?? "");
    setValues({});
    setSubmitting(false);
    resetProbe();
  }, [open, account, resetProbe]);

  const provider: QuotaProvider | undefined = useMemo(() => {
    const id = editing ? account.provider : providerId;
    return providers?.find((item) => item.id === id);
  }, [providers, providerId, editing, account]);

  const fields = provider?.credential_fields ?? [];
  const missingRequired = missingRequiredCredentialFields(fields, values);
  // 测试需要完整必填凭据：编辑模式下留空表示沿用已存值，而探测接口只接受
  // 请求体里的凭据，所以测试前必须先粘贴完整必填字段。
  const probeReady = canProbe(provider, values);

  const setFieldValue = (key: string, value: string) => {
    setValues((prev) => ({ ...prev, [key]: value }));
    probe.reset();
  };

  const handleClose = () => onOpenChange(false);

  const handleProbe = async () => {
    if (!provider || !probeReady) return;
    try {
      await probe.mutateAsync({
        provider: provider.id,
        credentials: collectCredentialValues(fields, values),
      });
    } catch {
      // mutation.error 已在下方就地展示
    }
  };

  const onSubmit = async (event: React.FormEvent) => {
    event.preventDefault();
    const trimmedLabel = label.trim();
    if (!trimmedLabel) {
      toast.error("请填写账号标签");
      return;
    }

    if (!editing) {
      if (!provider) {
        toast.error("请选择供应商");
        return;
      }
      if (missingRequired.length > 0) {
        toast.error(
          `请先填写必填字段：${missingRequired.map((f) => f.label).join("、")}`,
        );
        return;
      }
      setSubmitting(true);
      try {
        await createAccount.mutateAsync({
          provider: provider.id,
          label: trimmedLabel,
          credentials: collectCredentialValues(fields, values),
        });
        toast.success("账号已添加，稍后自动拉取额度");
        handleClose();
      } catch (err) {
        toast.error(errorText(err, "添加失败，请检查输入"));
      } finally {
        setSubmitting(false);
      }
      return;
    }

    // 编辑：任一凭据字段非空 → 整组替换（此时必填项必须齐全）；全空 → 仅改标签
    const credentialsChanged = hasAnyCredentialValue(fields, values);
    if (credentialsChanged && missingRequired.length > 0) {
      toast.error(
        `修改凭据需填齐必填字段：${missingRequired.map((f) => f.label).join("、")}`,
      );
      return;
    }
    setSubmitting(true);
    try {
      await updateAccount.mutateAsync({
        id: account.id,
        label: trimmedLabel,
        ...(credentialsChanged
          ? { credentials: collectCredentialValues(fields, values) }
          : {}),
      });
      toast.success("已保存");
      handleClose();
    } catch (err) {
      toast.error(errorText(err, "保存失败，请稍后重试"));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={(next) => (next ? undefined : handleClose())}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{editing ? "编辑额度账号" : "添加额度账号"}</DialogTitle>
          <DialogDescription>
            {editing
              ? `凭据加密保存，不回显明文（当前 ${account.credential_hint || "未保存"}）；留空表示不修改`
              : "凭据加密保存，不回显明文；保存后服务端定时拉取额度"}
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={(event) => void onSubmit(event)}>
          <div className="max-h-[60vh] space-y-4 overflow-y-auto py-2 pr-1">
            {editing ? (
              <div className="space-y-2">
                <Label>供应商</Label>
                <div className="flex items-center gap-2 rounded-md border px-2.5 py-2 text-sm">
                  <QuotaProviderIcon provider={account.provider} className="size-4" />
                  {account.provider_name}
                </div>
              </div>
            ) : (
              <div className="space-y-2">
                <Label htmlFor="quota-account-provider">供应商</Label>
                <OptionSelect
                  id="quota-account-provider"
                  aria-label="供应商"
                  className="w-full"
                  value={providerId}
                  onValueChange={(next) => {
                    setProviderId(next);
                    setValues({});
                    probe.reset();
                  }}
                  placeholder="选择供应商"
                  options={(providers ?? []).map((item) => ({
                    value: item.id,
                    label: item.name,
                    itemLabel: (
                      <span className="flex items-center gap-2">
                        <QuotaProviderIcon
                          provider={item.id}
                          className="size-3.5"
                        />
                        <span>{item.name}</span>
                        {item.plan_hint && (
                          <span className="text-xs text-muted-foreground">
                            {item.plan_hint}
                          </span>
                        )}
                      </span>
                    ),
                  }))}
                />
              </div>
            )}

            <div className="space-y-2">
              <Label htmlFor="quota-account-label">标签</Label>
              <Input
                id="quota-account-label"
                placeholder="例如 我的 Pro"
                value={label}
                onChange={(event) => setLabel(event.target.value)}
              />
            </div>

            {provider ? (
              <QuotaCredentialFields
                provider={provider}
                values={values}
                onChange={setFieldValue}
                idPrefix="quota-account-field"
              />
            ) : (
              !editing && (
                <p className="text-xs text-muted-foreground">
                  选择供应商后显示对应的凭据填写说明
                </p>
              )
            )}

            <ProbeResultPanel
              result={probe.data?.data}
              error={probe.error}
              isError={probe.isError}
            />
          </div>
          <DialogFooter className="mt-4">
            {provider && (
              <Button
                type="button"
                variant="outline"
                disabled={!probeReady || probe.isPending}
                onClick={() => void handleProbe()}
              >
                {probe.isPending ? "测试中..." : "测试"}
              </Button>
            )}
            <Button type="button" variant="outline" onClick={handleClose}>
              取消
            </Button>
            <Button type="submit" disabled={submitting}>
              {submitting ? "保存中..." : "保存"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
