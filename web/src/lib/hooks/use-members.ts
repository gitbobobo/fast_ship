import {
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient,
} from "@tanstack/react-query";
import { memberApi } from "@/lib/api/members";
import { useAuthStore } from "@/lib/store/auth-store";

const ROOT = "members";

function useUserId() {
  return useAuthStore((state) => state.user?.id ?? null);
}

/**
 * 用户身份变化（登出、切换账号）时清掉成员与邀请缓存——它们是管理员数据，
 * 后一个登录者不应看到前一个管理员拉取的成员列表与邀请记录。
 */
export function watchMembersIdentity(queryClient: QueryClient) {
  return useAuthStore.subscribe((state, previous) => {
    if (state.user?.id === previous.user?.id) return;
    void queryClient.cancelQueries({ queryKey: [ROOT] });
    queryClient.removeQueries({ queryKey: [ROOT] });
  });
}

// 管理员数据按登录用户隔离缓存（与 github-tokens/integrations 同一口径）。
// 列表端点统一 `{items}` 信封，hooks 直接解包成数组供页面消费。
export function useMembers() {
  const userId = useUserId();
  return useQuery({
    queryKey: [ROOT, userId],
    queryFn: async () => {
      const res = await memberApi.listMembers();
      return res.data.items;
    },
    enabled: Boolean(userId),
  });
}

export function useInvites() {
  const userId = useUserId();
  return useQuery({
    queryKey: [ROOT, userId, "invites"],
    queryFn: async () => {
      const res = await memberApi.listInvites();
      return res.data.items;
    },
    enabled: Boolean(userId),
  });
}

export function useUpdateMemberRole() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ uid, role }: { uid: string; role: UserRole }) =>
      memberApi.updateMemberRole(uid, { role }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: [ROOT] });
    },
  });
}

export function useCreateInvite() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: CreateInviteRequest = {}) =>
      memberApi.createInvite(input),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: [ROOT] });
    },
  });
}

export function useRevokeInvite() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => memberApi.revokeInvite(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: [ROOT] });
    },
  });
}
