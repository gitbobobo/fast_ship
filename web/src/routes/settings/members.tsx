import { useState } from "react";
import { Copy, UserPlus } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { OptionSelect } from "@/components/ui/option-select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { SettingsPageShell } from "@/routes/settings/layout";
import {
  useCreateInvite,
  useInvites,
  useMembers,
  useRevokeInvite,
  useUpdateMemberRole,
} from "@/lib/hooks/use-members";
import { useDeploymentSettings } from "@/lib/hooks/use-deployment";
import { useAuthStore } from "@/lib/store/auth-store";
import { copyWithToast } from "@/lib/copy";
import { formatDate } from "@/lib/utils/format";

const expiresOptions = [
  { value: "24", label: "1 天" },
  { value: "72", label: "3 天" },
  { value: "168", label: "7 天" },
  { value: "720", label: "30 天" },
] as const;

const roleMeta: Record<
  UserRole,
  { label: string; variant: "default" | "secondary" }
> = {
  admin: { label: "管理员", variant: "default" },
  member: { label: "成员", variant: "secondary" },
};

function inviteStatus(invite: Invite): {
  label: string;
  variant: "default" | "secondary" | "outline";
} {
  if (invite.used_at) return { label: "已使用", variant: "secondary" };
  if (invite.revoked_at) return { label: "已撤销", variant: "outline" };
  if (new Date(invite.expires_at).getTime() <= Date.now()) {
    return { label: "已过期", variant: "outline" };
  }
  return { label: "有效", variant: "default" };
}

function errorText(error: unknown, fallback: string) {
  return error instanceof Error && error.message ? error.message : fallback;
}

function MembersTable({ members }: { members: Member[] }) {
  const currentUser = useAuthStore((state) => state.user);
  const setUser = useAuthStore((state) => state.setUser);
  const updateRole = useUpdateMemberRole();
  const adminCount = members.filter((m) => m.role === "admin").length;

  const handleRoleChange = async (member: Member) => {
    const role: UserRole = member.role === "admin" ? "member" : "admin";
    try {
      const res = await updateRole.mutateAsync({ uid: member.id, role });
      // 改的是自己（如被别的管理员降级）：用响应里的新角色同步 auth store，
      // 侧栏的管理入口立即收敛，不用等下一次 /auth/me 回读。
      if (currentUser && member.id === currentUser.id) {
        setUser({ ...currentUser, role: res.data.role });
      }
      toast.success(
        role === "admin"
          ? `已将 ${member.username} 设为管理员`
          : `已将 ${member.username} 降为成员`,
      );
    } catch (error) {
      toast.error(errorText(error, "角色更新失败"));
    }
  };

  return (
    <div className="rounded-lg border overflow-hidden">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>用户名</TableHead>
            <TableHead>邮箱</TableHead>
            <TableHead>角色</TableHead>
            <TableHead>注册时间</TableHead>
            <TableHead className="text-right">操作</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {members.map((member) => {
            const isLastAdmin = member.role === "admin" && adminCount === 1;
            return (
              <TableRow key={member.id}>
                <TableCell className="font-medium whitespace-normal break-words">
                  {member.username}
                  {member.id === currentUser?.id && (
                    <span className="ml-1 text-xs text-muted-foreground">
                      （我）
                    </span>
                  )}
                </TableCell>
                <TableCell className="whitespace-normal break-words">
                  {member.email}
                </TableCell>
                <TableCell>
                  <Badge variant={roleMeta[member.role].variant}>
                    {roleMeta[member.role].label}
                  </Badge>
                </TableCell>
                <TableCell className="text-sm">
                  {formatDate(member.created_at)}
                </TableCell>
                <TableCell className="text-right">
                  <div className="flex flex-col items-end gap-1">
                    <Button
                      variant="outline"
                      size="sm"
                      disabled={isLastAdmin || updateRole.isPending}
                      onClick={() => handleRoleChange(member)}
                    >
                      {member.role === "admin" ? "降为成员" : "设为管理员"}
                    </Button>
                    {isLastAdmin && (
                      <p className="text-xs text-muted-foreground">
                        最后一个管理员，不能降级
                      </p>
                    )}
                  </div>
                </TableCell>
              </TableRow>
            );
          })}
        </TableBody>
      </Table>
    </div>
  );
}

function InvitesSection() {
  const { data: invites, isLoading } = useInvites();
  const createInvite = useCreateInvite();
  const revokeInvite = useRevokeInvite();
  // 邀请链接优先用部署 public_url（新成员从外网打开），未配置再回落当前站点源。
  // public_url 还没成功拿到时不能创建（加载中或查询已失败）：token 只回一次，用当前
  // 站点源拼出的链接对走内网/代理访问的管理员可能是收件人打不开的地址。
  const { data: settings } = useDeploymentSettings();
  const inviteOrigin = settings?.public_url || window.location.origin;
  // 以「拿到部署设置」为准而不是 !isLoading：查询重试用尽后 isLoading 会变 false
  // 但 data 仍为空，那时不能放行。
  const canCreate = Boolean(settings);
  const [expiresHours, setExpiresHours] = useState<string>("72");
  // 明文 token 只在创建响应里出现一次：创建成功后立即展示链接供复制。
  const [created, setCreated] = useState<{
    url: string;
    expires_at: string;
  } | null>(null);

  const handleCreate = async () => {
    try {
      const res = await createInvite.mutateAsync({
        expires_in_hours: Number(expiresHours),
      });
      setCreated({
        url: `${inviteOrigin}/register?invite=${res.data.token}`,
        expires_at: res.data.expires_at,
      });
      toast.success("邀请链接已创建，仅显示一次");
    } catch (error) {
      toast.error(errorText(error, "创建邀请失败"));
    }
  };

  const handleRevoke = async (invite: Invite) => {
    try {
      await revokeInvite.mutateAsync(invite.id);
      toast.success("邀请已撤销");
    } catch (error) {
      toast.error(errorText(error, "撤销失败"));
    }
  };

  return (
    <div className="space-y-4 rounded-xl border p-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <p className="font-medium">邀请</p>
          <p className="text-sm text-muted-foreground">
            公开注册已关闭，新成员只能通过邀请链接注册。
          </p>
        </div>
        <div className="flex items-center gap-2">
          <OptionSelect
            value={expiresHours}
            onValueChange={(value) => setExpiresHours(value ?? "72")}
            options={expiresOptions.map((o) => ({ value: o.value, label: o.label }))}
            aria-label="邀请有效期"
            size="sm"
          />
          <Button
            size="sm"
            onClick={handleCreate}
            disabled={createInvite.isPending || !canCreate}
          >
            <UserPlus className="mr-1.5 h-3.5 w-3.5" />
            {createInvite.isPending ? "创建中..." : "创建邀请"}
          </Button>
        </div>
      </div>

      {created && (
        <div className="space-y-2 rounded-lg border bg-muted/30 p-3">
          <p className="text-sm font-medium">
            邀请链接已创建，仅显示这一次，请复制发给新成员：
          </p>
          <div className="flex items-center gap-2">
            <code className="min-w-0 flex-1 truncate rounded-lg border bg-background px-2.5 py-1.5 font-mono text-xs">
              {created.url}
            </code>
            <Button
              variant="outline"
              size="sm"
              onClick={() => void copyWithToast(created.url, "已复制邀请链接")}
            >
              <Copy className="mr-1.5 h-3.5 w-3.5" />
              复制
            </Button>
          </div>
          <p className="text-xs text-muted-foreground">
            有效至 {formatDate(created.expires_at)}
          </p>
        </div>
      )}

      {isLoading ? (
        <div className="space-y-2">
          <Skeleton className="h-10 rounded" />
          <Skeleton className="h-10 rounded" />
        </div>
      ) : !invites?.length ? (
        <div className="rounded-lg border bg-muted/40 py-8 text-center">
          <p className="text-sm text-muted-foreground">暂无邀请记录</p>
        </div>
      ) : (
        <div className="rounded-lg border overflow-hidden">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>状态</TableHead>
                <TableHead>过期时间</TableHead>
                <TableHead>使用者</TableHead>
                <TableHead>创建时间</TableHead>
                <TableHead className="text-right">操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {invites.map((invite) => {
                const status = inviteStatus(invite);
                return (
                  <TableRow key={invite.id}>
                    <TableCell>
                      <Badge variant={status.variant}>{status.label}</Badge>
                    </TableCell>
                    <TableCell className="text-sm">
                      {formatDate(invite.expires_at)}
                    </TableCell>
                    <TableCell className="text-sm">
                      {invite.used_by ?? (
                        <span className="text-muted-foreground">—</span>
                      )}
                    </TableCell>
                    <TableCell className="text-sm">
                      {formatDate(invite.created_at)}
                    </TableCell>
                    <TableCell className="text-right">
                      {status.label === "有效" && (
                        <AlertDialog>
                          <AlertDialogTrigger
                            render={<Button variant="outline" size="sm" />}
                          >
                            撤销
                          </AlertDialogTrigger>
                          <AlertDialogContent>
                            <AlertDialogHeader>
                              <AlertDialogTitle>撤销邀请？</AlertDialogTitle>
                              <AlertDialogDescription>
                                撤销后该邀请链接立即失效，可以再创建新的邀请。
                              </AlertDialogDescription>
                            </AlertDialogHeader>
                            <AlertDialogFooter>
                              <AlertDialogCancel>取消</AlertDialogCancel>
                              <AlertDialogAction
                                onClick={() => handleRevoke(invite)}
                              >
                                确认撤销
                              </AlertDialogAction>
                            </AlertDialogFooter>
                          </AlertDialogContent>
                        </AlertDialog>
                      )}
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </div>
      )}
    </div>
  );
}

function MembersAdmin() {
  const { data: members, isLoading, isError, isFetching, refetch } =
    useMembers();

  return (
    <SettingsPageShell wide>
      <div className="space-y-6">
        <div>
          <h2 className="text-lg font-medium">成员</h2>
          <p className="text-sm text-muted-foreground">
            管理本部署的账号与角色；新成员通过邀请链接加入。
          </p>
        </div>

        {isLoading ? (
          <div className="space-y-3">
            {Array.from({ length: 3 }).map((_, i) => (
              <Skeleton key={i} className="h-12 rounded" />
            ))}
          </div>
        ) : isError || !members ? (
          <div
            role="alert"
            className="rounded-lg border bg-muted/40 py-12 text-center"
          >
            <p className="text-sm text-muted-foreground">成员列表加载失败</p>
            <Button
              variant="outline"
              size="sm"
              className="mt-3"
              onClick={() => refetch()}
              disabled={isFetching}
            >
              重试
            </Button>
          </div>
        ) : (
          <MembersTable members={members} />
        )}

        <InvitesSection />
      </div>
    </SettingsPageShell>
  );
}

export default function MembersPage() {
  const isAdmin = useAuthStore((state) => state.user?.role === "admin");

  if (!isAdmin) {
    return (
      <SettingsPageShell>
        <div className="space-y-6">
          <div>
            <h2 className="text-lg font-medium">成员</h2>
          </div>
          <div className="rounded-lg border bg-muted/40 py-12 text-center">
            <p className="text-sm text-muted-foreground">
              需要管理员权限，请联系管理员管理成员与邀请。
            </p>
          </div>
        </div>
      </SettingsPageShell>
    );
  }

  return <MembersAdmin />;
}
