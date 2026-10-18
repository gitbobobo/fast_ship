import { useCallback, useEffect, useMemo, useRef, useState } from "react";
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
import { Separator } from "@/components/ui/separator";
import { QuotaProviderIcon } from "@/components/quota/provider-icons";
import { QuotaCredentialFields } from "./credential-fields";
import { QuotaDeviceAuthPanel } from "./device-auth-panel";
import { ProbeResultPanel } from "./probe-result-panel";
import {
  useCancelQuotaDeviceAuthorization,
  useCreateQuotaAccount,
  useInvalidateQuota,
  useQuotaProbe,
  useQuotaProviders,
  useStartQuotaDeviceAuthorization,
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
  const startAuth = useStartQuotaDeviceAuthorization();
  const cancel = useCancelQuotaDeviceAuthorization();
  const invalidateQuota = useInvalidateQuota();

  const editing = account !== null;
  const [providerId, setProviderId] = useState<string | null>(null);
  const [label, setLabel] = useState("");
  const [values, setValues] = useState<Record<string, string>>({});
  const [submitting, setSubmitting] = useState(false);
  // 非 null 时对话框切到设备授权面板（INT-117），返回/关闭回到表单
  const [authSession, setAuthSession] =
    useState<QuotaDeviceAuthorization | null>(null);
  const resetProbe = probe.reset;
  const resetStartAuth = startAuth.reset;

  // 会话收尾与在途响应都走 ref：关闭路径在 effect 里读最新会话，
  // 迟到的 POST 响应用代次判断是否已被作废（B2）。
  const authSessionRef = useRef<QuotaDeviceAuthorization | null>(null);
  useEffect(() => {
    authSessionRef.current = authSession;
  });
  const cancelMutateRef = useRef(cancel.mutate);
  useEffect(() => {
    cancelMutateRef.current = cancel.mutate;
  });
  const authGenerationRef = useRef(0);

  // 只清会话 ref 与 state，不发请求。approved 路径用它：面板轮询到
  // approved 时这里存的仍是 POST 返回的 pending 视图，若按这个 status 判断
  // 会向一个已被服务端消费的会话发 DELETE（只会撞 404）。
  const clearSession = useCallback(() => {
    authSessionRef.current = null;
    setAuthSession(null);
  }, []);

  // 丢弃未完成的授权会话：pending 时 DELETE 通知服务端销毁（失败静默），
  // 再清 state；「取消」「重新发起」「关闭」共用。
  const discardSession = useCallback(() => {
    const session = authSessionRef.current;
    if (session?.status === "pending") cancelMutateRef.current(session.id);
    clearSession();
  }, [clearSession]);

  // 打开时从账号/空态重建表单；关闭即丢掉凭据明文与探测结果，
  // 不让用户输入留在 state 里等下一次打开。
  useEffect(() => {
    // 代次递增：任何打开/关闭/换账号都让在途的发起响应作废
    authGenerationRef.current += 1;
    if (open) {
      setProviderId(account?.provider ?? null);
      setLabel(account?.label ?? "");
      setSubmitting(false);
    }
    setValues({});
    discardSession();
    resetProbe();
    resetStartAuth();
  }, [open, account, resetProbe, resetStartAuth, discardSession]);

  const provider: QuotaProvider | undefined = useMemo(() => {
    const id = editing ? account.provider : providerId;
    return providers?.find((item) => item.id === id);
  }, [providers, providerId, editing, account]);

  const fields = provider?.credential_fields ?? [];
  const missingRequired = missingRequiredCredentialFields(fields, values);
  // 测试需要完整必填凭据：编辑模式下留空表示沿用已存值，而探测接口只接受
  // 请求体里的凭据，所以测试前必须先粘贴完整必填字段。
  const probeReady = canProbe(provider, values);
  // 供应商声明 device_authorization 即可用；编辑模式走换凭据（A5/B4）
  const deviceAuthAvailable = provider?.device_authorization === true;

  const setFieldValue = (key: string, value: string) => {
    setValues((prev) => ({ ...prev, [key]: value }));
    probe.reset();
  };

  const handleClose = () => onOpenChange(false);

  const beginDeviceAuth = async () => {
    if (!provider) return;
    const trimmedLabel = label.trim();
    if (!trimmedLabel) {
      toast.error("请填写账号标签");
      return;
    }
    // 「重新发起」先收尾旧会话；编辑模式带 account_id 走换凭据
    discardSession();
    const generation = ++authGenerationRef.current;
    try {
      const res = await startAuth.mutateAsync({
        provider: provider.id,
        label: trimmedLabel,
        ...(account ? { account_id: account.id } : {}),
      });
      // 响应回来时对话框已关闭/重开或又发起了一次：会话立刻 DELETE 丢弃，
      // 不 setState，避免旧响应劫持新表单（B2）
      if (generation !== authGenerationRef.current) {
        cancelMutateRef.current(res.data.id);
        return;
      }
      setAuthSession(res.data);
    } catch (err) {
      if (generation === authGenerationRef.current) {
        toast.error(errorText(err, "发起授权失败，请稍后重试"));
      }
    }
  };

  const handleAuthApproved = () => {
    toast.success("授权成功，额度稍后自动拉取");
    clearSession();
    void invalidateQuota();
    handleClose();
  };

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
        {authSession ? (
          <div className="max-h-[60vh] overflow-y-auto py-2 pr-1">
            <QuotaDeviceAuthPanel
              authorization={authSession}
              restarting={startAuth.isPending}
              onApproved={handleAuthApproved}
              onRestart={() => void beginDeviceAuth()}
              onCancel={discardSession}
            />
          </div>
        ) : (
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

              {deviceAuthAvailable && (
                <div className="space-y-3">
                  <Button
                    type="button"
                    className="w-full"
                    disabled={startAuth.isPending}
                    onClick={() => void beginDeviceAuth()}
                  >
                    {startAuth.isPending ? "发起授权中..." : "使用浏览器授权"}
                  </Button>
                  <p className="text-xs text-muted-foreground">
                    在打开的页面点批准，完成后这里自动继续
                  </p>
                  <div className="flex items-center gap-3" aria-hidden>
                    <Separator className="flex-1" />
                    <span className="shrink-0 text-xs text-muted-foreground">
                      或粘贴控制面密钥
                    </span>
                    <Separator className="flex-1" />
                  </div>
                </div>
              )}

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
        )}
      </DialogContent>
    </Dialog>
  );
}
