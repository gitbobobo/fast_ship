import { useMemo } from "react";
import { ShieldOff } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
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
import { Skeleton } from "@/components/ui/skeleton";
import { SettingsPageShell } from "@/routes/settings/layout";
import {
  useOAuthAuthorizations,
  useRevokeOAuthAuthorization,
} from "@/lib/hooks/use-oauth";
import { useProjects } from "@/lib/hooks/use-projects";
import { formatDate } from "@/lib/utils/format";
import { toast } from "sonner";
import type { components } from "@/types/api.gen";

type OAuthAuthorization = components["schemas"]["OAuthAuthorization"];
type Project = components["schemas"]["Project"];

/** 解析 scope 串（project:<uuid>:read|write，空格分隔）为「项目 id → 读写档」。 */
function parseScopes(scopes: string): Map<string, boolean> {
  const map = new Map<string, boolean>();
  for (const scope of scopes.split(/\s+/).filter(Boolean)) {
    const match = /^project:([^:]+):(read|write)$/.exec(scope);
    if (!match) continue;
    // 任一 write 即视整体为读写档，并覆盖该项目的只读记录。
    map.set(match[1], match[2] === "write" || (map.get(match[1]) ?? false));
  }
  return map;
}

/** 授权范围友好展示：读写档 + 覆盖的项目名（id 对不上名字时退回截断 id）。 */
function ScopeSummary({
  authorization,
  projects,
}: {
  authorization: OAuthAuthorization;
  projects: Project[] | undefined;
}) {
  const { write, projectLabel } = useMemo(() => {
    const scopeMap = parseScopes(authorization.scopes);
    const anyWrite = [...scopeMap.values()].some(Boolean);
    const nameById = new Map((projects ?? []).map((p) => [p.id, p.name]));
    const names = [...scopeMap.keys()].map(
      (id) => nameById.get(id) ?? `项目 ${id.slice(0, 8)}…`,
    );
    return { write: anyWrite, projectLabel: names.join("、") };
  }, [authorization.scopes, projects]);

  return (
    <div className="flex items-center gap-2">
      <Badge variant={write ? "default" : "secondary"} className="px-2 py-0.5">
        {write ? "读写" : "只读"}
      </Badge>
      <span className="text-sm text-muted-foreground">· {projectLabel}</span>
    </div>
  );
}

export default function OAuthAppsPage() {
  const { data: authorizations, isLoading } = useOAuthAuthorizations();
  const { data: projectsPage } = useProjects();
  const projects = projectsPage?.items;
  const revoke = useRevokeOAuthAuthorization();

  const handleRevoke = async (id: string) => {
    try {
      await revoke.mutateAsync(id);
      toast.success("已吊销该应用的授权");
    } catch {
      toast.error("吊销失败，请重试");
    }
  };

  return (
    <SettingsPageShell>
      <div className="space-y-6">
        <div>
          <h2 className="text-lg font-medium">已授权的应用</h2>
          <p className="text-sm text-muted-foreground">
            通过 OAuth 接入 Fast Ship 的 MCP
            客户端会出现在这里，吊销后其授权码与会话立即失效。
          </p>
        </div>

        {isLoading ? (
          <div className="space-y-3">
            {Array.from({ length: 3 }).map((_, i) => (
              <Skeleton key={i} className="h-10 rounded" />
            ))}
          </div>
        ) : !authorizations?.length ? (
          <div className="rounded-lg border bg-muted/40 py-12 text-center">
            <p className="text-sm text-muted-foreground">还没有已授权的应用</p>
            <p className="mt-1 text-xs text-muted-foreground">
              当 MCP 客户端请求访问你的项目时，可在这里统一管理或吊销
            </p>
          </div>
        ) : (
          <div className="overflow-hidden rounded-lg border">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>应用</TableHead>
                  <TableHead>授权范围</TableHead>
                  <TableHead>授权时间</TableHead>
                  <TableHead className="text-right">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {authorizations.map((auth) => (
                  <TableRow key={auth.id}>
                    <TableCell className="font-medium">
                      {auth.client_name}
                    </TableCell>
                    <TableCell>
                      <ScopeSummary authorization={auth} projects={projects} />
                    </TableCell>
                    <TableCell>{formatDate(auth.created_at)}</TableCell>
                    <TableCell className="text-right">
                      <AlertDialog>
                        <AlertDialogTrigger
                          render={
                            <Button
                              variant="ghost"
                              size="sm"
                              className="text-destructive hover:bg-destructive/10 hover:text-destructive"
                              aria-label={`吊销 ${auth.client_name} 的授权`}
                            />
                          }
                        >
                          <ShieldOff className="mr-1.5 h-3.5 w-3.5" />
                          吊销
                        </AlertDialogTrigger>
                        <AlertDialogContent>
                          <AlertDialogHeader>
                            <AlertDialogTitle>
                              吊销 {auth.client_name} 的授权？
                            </AlertDialogTitle>
                            <AlertDialogDescription>
                              吊销后该应用将立即失去对所选项目的访问，需重新授权才能恢复。
                            </AlertDialogDescription>
                          </AlertDialogHeader>
                          <AlertDialogFooter>
                            <AlertDialogCancel>取消</AlertDialogCancel>
                            <AlertDialogAction
                              variant="destructive"
                              onClick={() => handleRevoke(auth.id)}
                            >
                              确认吊销
                            </AlertDialogAction>
                          </AlertDialogFooter>
                        </AlertDialogContent>
                      </AlertDialog>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        )}
      </div>
    </SettingsPageShell>
  );
}
