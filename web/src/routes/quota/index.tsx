import { useMemo, useState } from "react";
import { Gauge, Plus } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Header } from "@/components/layout/header";
import { HeaderActions } from "@/components/layout/header-actions";
import { useQuotaAccounts } from "@/lib/hooks/use-quota";
import {
  quotaAccountGroup,
  sortExhaustedAccounts,
  sortUsableAccounts,
  type QuotaAccountGroup,
} from "@/lib/quota";
import { cn } from "@/lib/utils";
import { QuotaAccountCard } from "./components/account-card";
import { QuotaAccountFormDialog } from "./components/account-form-dialog";
import { QuotaAccountTestDialog } from "./components/account-test-dialog";
import { QuotaAttentionBanner } from "./components/attention-banner";
import { QuotaExhaustedRow } from "./components/exhausted-row";

const GROUP_META: Record<
  QuotaAccountGroup,
  { label: string; empty: string }
> = {
  usable: { label: "可用", empty: "没有可用账号" },
  exhausted: { label: "已用尽", empty: "没有已用尽的账号" },
  attention: { label: "需处理", empty: "没有需处理的账号" },
};

const CHIP_ORDER: QuotaAccountGroup[] = ["usable", "exhausted", "attention"];

// 套餐额度（INT-109/INT-130）：顶部计数 chip 兼做筛选；
// 需处理收成横幅在最前，可用账号卡片按有效剩余降序，已用尽收成单行排最后。
export default function QuotaPage() {
  const { data: accounts, isLoading, isError, refetch } = useQuotaAccounts();

  const [formOpen, setFormOpen] = useState(false);
  const [editing, setEditing] = useState<QuotaAccount | null>(null);
  const [testing, setTesting] = useState<QuotaAccount | null>(null);
  const [filter, setFilter] = useState<QuotaAccountGroup | null>(null);

  const groups = useMemo(() => {
    const usable: QuotaAccount[] = [];
    const exhausted: QuotaAccount[] = [];
    const attention: QuotaAccount[] = [];
    for (const account of accounts ?? []) {
      const group = quotaAccountGroup(account);
      if (group === "attention") attention.push(account);
      else if (group === "exhausted") exhausted.push(account);
      else usable.push(account);
    }
    return {
      usable: sortUsableAccounts(usable),
      exhausted: sortExhaustedAccounts(exhausted),
      attention,
    };
  }, [accounts]);

  const openCreate = () => {
    setEditing(null);
    setFormOpen(true);
  };
  const openEdit = (account: QuotaAccount) => {
    setEditing(account);
    setFormOpen(true);
  };

  const visible = (group: QuotaAccountGroup) =>
    filter == null || filter === group;

  return (
    <div className="flex flex-col">
      <Header
        title="套餐额度"
        actions={
          <HeaderActions
            primary={
              <Button size="sm" onClick={openCreate}>
                <Plus className="mr-1.5 h-3.5 w-3.5" />
                添加账号
              </Button>
            }
          />
        }
      />
      <div className="mx-auto w-full max-w-6xl space-y-6 p-4 md:p-6">
        <QuotaAccountFormDialog
          open={formOpen}
          account={editing}
          onOpenChange={(open) => {
            setFormOpen(open);
            if (!open) setEditing(null);
          }}
        />
        <QuotaAccountTestDialog
          account={testing}
          onOpenChange={(open) => {
            if (!open) setTesting(null);
          }}
        />

        {isLoading ? (
          <div className="grid items-start gap-4 sm:grid-cols-2 xl:grid-cols-3">
            {Array.from({ length: 3 }).map((_, i) => (
              <Skeleton key={i} className="h-44 rounded-xl" />
            ))}
          </div>
        ) : isError ? (
          <div className="rounded-lg border bg-muted/40 py-12 text-center">
            <p className="text-sm text-muted-foreground">额度数据加载失败</p>
            <Button
              variant="outline"
              size="sm"
              className="mt-3"
              onClick={() => void refetch()}
            >
              重试
            </Button>
          </div>
        ) : !accounts?.length ? (
          <div className="rounded-lg border bg-muted/40 py-12 text-center">
            <Gauge className="mx-auto h-8 w-8 text-muted-foreground" />
            <p className="mt-2 text-sm text-muted-foreground">
              还没有额度账号
            </p>
            <p className="mx-auto mt-1 max-w-md text-xs text-muted-foreground">
              粘贴各供应商账号凭据后，服务端会定时拉取额度并在此展示；
              凭据加密保存，任何位置不回显明文。
            </p>
            <Button size="sm" className="mt-4" onClick={openCreate}>
              <Plus className="mr-1.5 h-3.5 w-3.5" />
              添加账号
            </Button>
          </div>
        ) : (
          <>
            <div className="flex flex-wrap items-center gap-1.5">
              {CHIP_ORDER.map((key) => (
                <button
                  key={key}
                  type="button"
                  aria-pressed={filter === key}
                  data-testid={`quota-chip-${key}`}
                  onClick={() =>
                    setFilter((prev) => (prev === key ? null : key))
                  }
                  className={cn(
                    "inline-flex h-7 items-center gap-1.5 rounded-full border px-2.5 text-xs transition-colors",
                    filter === key
                      ? "border-primary/40 bg-primary/10 text-foreground"
                      : "border-transparent bg-muted/60 text-muted-foreground hover:bg-muted hover:text-foreground",
                  )}
                >
                  {GROUP_META[key].label}
                  <span
                    className={cn(
                      "tabular-nums",
                      key === "attention" && groups.attention.length > 0
                        ? "text-destructive"
                        : "text-muted-foreground",
                    )}
                  >
                    {groups[key].length}
                  </span>
                </button>
              ))}
            </div>

            {filter != null && groups[filter].length === 0 && (
              <p className="py-10 text-center text-sm text-muted-foreground">
                {GROUP_META[filter].empty}
              </p>
            )}

            {visible("attention") && groups.attention.length > 0 && (
              <QuotaAttentionBanner
                accounts={groups.attention}
                onEdit={openEdit}
                onTest={setTesting}
              />
            )}

            {visible("usable") && groups.usable.length > 0 && (
              <div className="grid items-start gap-4 sm:grid-cols-2 xl:grid-cols-3">
                {groups.usable.map((account) => (
                  <QuotaAccountCard
                    key={account.id}
                    account={account}
                    onTest={setTesting}
                    onEdit={openEdit}
                  />
                ))}
              </div>
            )}

            {visible("exhausted") && groups.exhausted.length > 0 && (
              <div className="space-y-1.5">
                {groups.exhausted.map((account) => (
                  <QuotaExhaustedRow
                    key={account.id}
                    account={account}
                    onEdit={openEdit}
                    onTest={setTesting}
                  />
                ))}
              </div>
            )}
          </>
        )}
      </div>
    </div>
  );
}
