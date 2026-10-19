import { api, decodeApiError } from "./client";

// 成员与邀请（INT-129）：全部要求管理员身份，接口错误码 40305 表示非管理员。
// 邀请明文 token 只在 createInvite 响应里出现一次，列表不返回 token。

export const memberApi = {
  listMembers: () =>
    decodeApiError(api.get("members").json<ApiResponse<MemberListData>>()),

  updateMemberRole: (uid: string, input: UpdateMemberRoleRequest) =>
    decodeApiError(
      api
        .put(`members/${encodeURIComponent(uid)}/role`, { json: input })
        .json<ApiResponse<Member>>(),
    ),

  listInvites: () =>
    decodeApiError(api.get("invites").json<ApiResponse<InviteListData>>()),

  createInvite: (input: CreateInviteRequest = {}) =>
    decodeApiError(
      api.post("invites", { json: input }).json<ApiResponse<CreatedInvite>>(),
    ),

  // 幂等：已撤销的邀请再撤销仍返回成功
  revokeInvite: (id: string) =>
    decodeApiError(
      api.delete(`invites/${encodeURIComponent(id)}`).json<ApiResponse<null>>(),
    ),
};
