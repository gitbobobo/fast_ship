import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { oauthApi, type OAuthAuthorizeParams } from "@/lib/api/oauth";

/** 参数齐全才发起预览；不全时 query 挂起，由页面展示参数错误态。 */
export function useOAuthAuthorizePreview(params: OAuthAuthorizeParams | null) {
  return useQuery({
    queryKey: ["oauth-authorize-preview", params],
    enabled: !!params,
    queryFn: async () => {
      const res = await oauthApi.preview(params!);
      return res.data;
    },
  });
}

export function useOAuthAuthorizations() {
  return useQuery({
    queryKey: ["oauth-authorizations"],
    queryFn: async () => {
      const res = await oauthApi.list();
      return res.data;
    },
  });
}

export function useRevokeOAuthAuthorization() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: oauthApi.revoke,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["oauth-authorizations"] });
    },
  });
}
