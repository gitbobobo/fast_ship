import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { QuotaProviderIcon } from "@/components/quota/provider-icons";
import { QuotaCredentialFields } from "./credential-fields";
import { ProbeResultPanel } from "./probe-result-panel";
import { useQuotaProbe, useQuotaProviders } from "@/lib/hooks/use-quota";
import { canProbe, collectCredentialValues } from "@/lib/quota";

/**
 * 卡片上的「测试」：凭据不在前端保存，探测接口需要请求体带凭据，
 * 所以让用户重新粘贴后调 /api/quota/test 就地看结果（不落库、不改账号）。
 */
export function QuotaAccountTestDialog({
  account,
  onOpenChange,
}: {
  /** null = 关闭 */
  account: QuotaAccount | null;
  onOpenChange: (open: boolean) => void;
}) {
  const { data: providers } = useQuotaProviders();
  const probe = useQuotaProbe();
  const [values, setValues] = useState<Record<string, string>>({});
  const resetProbe = probe.reset;

  useEffect(() => {
    setValues({});
    resetProbe();
  }, [account, resetProbe]);

  const provider = providers?.find((item) => item.id === account?.provider);
  const fields = provider?.credential_fields ?? [];
  const probeReady = canProbe(provider, values);

  const handleProbe = async () => {
    if (!account || !provider || !probeReady) return;
    try {
      await probe.mutateAsync({
        provider: provider.id,
        credentials: collectCredentialValues(fields, values),
      });
    } catch {
      // mutation.error 已在下方就地展示
    }
  };

  return (
    <Dialog
      open={account !== null}
      onOpenChange={(next) => (next ? undefined : onOpenChange(false))}
    >
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>测试凭据连通性</DialogTitle>
          <DialogDescription>
            {account &&
              `${account.provider_name} · ${account.label}${account.credential_hint ? ` · 当前 ${account.credential_hint}` : ""}；凭据不落库，仅本次测试`}
          </DialogDescription>
        </DialogHeader>
        <div className="max-h-[60vh] space-y-4 overflow-y-auto py-2 pr-1">
          {account && provider ? (
            <QuotaCredentialFields
              provider={provider}
              values={values}
              onChange={(key, value) => {
                setValues((prev) => ({ ...prev, [key]: value }));
                resetProbe();
              }}
              idPrefix="quota-test-field"
            />
          ) : (
            account && (
              <div className="flex items-center gap-2 text-sm text-muted-foreground">
                <QuotaProviderIcon provider={account.provider} className="size-4" />
                未找到该供应商的凭据说明，无法测试
              </div>
            )
          )}

          <ProbeResultPanel
            result={probe.data?.data}
            error={probe.error}
            isError={probe.isError}
          />
        </div>
        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            onClick={() => onOpenChange(false)}
          >
            关闭
          </Button>
          <Button
            type="button"
            disabled={!probeReady || probe.isPending}
            onClick={() => void handleProbe()}
          >
            {probe.isPending ? "测试中..." : "测试"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
