import { useState } from "react";
import { Gauge, Plus } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Header } from "@/components/layout/header";
import { HeaderActions } from "@/components/layout/header-actions";
import { useQuotaAccounts } from "@/lib/hooks/use-quota";
import { QuotaAccountCard } from "./components/account-card";
import { QuotaAccountFormDialog } from "./components/account-form-dialog";
import { QuotaAccountTestDialog } from "./components/account-test-dialog";

// 套餐额度（INT-109）：只读展示服务端定时抓取的各供应商额度；
// 账号与凭据在「添加账号」对话框里维护。
export default function QuotaPage() {
  const { data: accounts, isLoading, isError, refetch } = useQuotaAccounts();

  const [formOpen, setFormOpen] = useState(false);
  const [editing, setEditing] = useState<QuotaAccount | null>(null);
  const [testing, setTesting] = useState<QuotaAccount | null>(null);

  const openCreate = () => {
    setEditing(null);
    setFormOpen(true);
  };

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
        <p className="text-sm text-muted-foreground">
          粘贴各供应商账号凭据后，服务端会定时拉取额度并在此展示；
          凭据加密保存，任何位置不回显明文。
        </p>

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
          <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
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
            <p className="mt-1 text-xs text-muted-foreground">
              添加供应商账号的凭据后，这里会展示套餐名、用量窗口与重置时间
            </p>
            <Button size="sm" className="mt-4" onClick={openCreate}>
              <Plus className="mr-1.5 h-3.5 w-3.5" />
              添加账号
            </Button>
          </div>
        ) : (
          <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
            {accounts.map((account) => (
              <QuotaAccountCard
                key={account.id}
                account={account}
                onTest={setTesting}
                onEdit={(item) => {
                  setEditing(item);
                  setFormOpen(true);
                }}
              />
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
