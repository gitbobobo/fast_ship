import { useAuthStore } from "@/lib/store/auth-store";
import { api, tryRefreshToken } from "./client";

interface UploadResult {
  status: number;
  response: ApiResponse<Artifact> | null;
}

function parseUploadResponse(xhr: XMLHttpRequest) {
  if (!xhr.responseText) {
    return null;
  }

  return JSON.parse(xhr.responseText) as ApiResponse<Artifact>;
}

function createUploadXhr(
  vid: string,
  token: string | null,
  onProgress?: (percent: number) => void,
) {
  const xhr = new XMLHttpRequest();
  xhr.open("POST", `/api/versions/${vid}/artifacts`);

  if (token) {
    xhr.setRequestHeader("Authorization", `Bearer ${token}`);
  }

  xhr.upload.addEventListener("progress", (event) => {
    if (!event.lengthComputable || !onProgress) return;
    const percent = Math.round((event.loaded / event.total) * 100);
    onProgress(percent);
  });

  return xhr;
}

function sendUploadRequest(
  vid: string,
  formData: FormData,
  token: string | null,
  onProgress?: (percent: number) => void,
) {
  return new Promise<UploadResult>((resolve, reject) => {
    const xhr = createUploadXhr(vid, token, onProgress);

    xhr.addEventListener("load", () => {
      resolve({
        status: xhr.status,
        response: parseUploadResponse(xhr),
      });
    });

    xhr.addEventListener("error", () => {
      reject(new Error("上传失败"));
    });

    xhr.send(formData);
  });
}

function requireUploadSuccess(result: UploadResult) {
  if (result.status >= 200 && result.status < 300 && result.response) {
    return result.response;
  }

  throw new Error(result.response?.message || "上传失败");
}

export const artifactApi = {
  upload: async (
    vid: string,
    formData: FormData,
    onProgress?: (percent: number) => void,
  ) => {
    const token = useAuthStore.getState().token;
    const result = await sendUploadRequest(vid, formData, token, onProgress);

    if (result.status !== 401) {
      return requireUploadSuccess(result);
    }

    let newToken: string;
    try {
      newToken = await tryRefreshToken();
    } catch {
      useAuthStore.getState().logout();
      window.location.href = "/login";
      throw new Error("Unauthorized");
    }

    const retryResult = await sendUploadRequest(
      vid,
      formData,
      newToken,
      onProgress,
    );
    return requireUploadSuccess(retryResult);
  },

  delete: (aid: string) =>
    api.delete(`artifacts/${aid}`).json<ApiResponse<null>>(),

  downloadUrl: (aid: string) => {
    const token = useAuthStore.getState().token;
    const url = new URL(
      `/api/artifacts/${aid}/download`,
      window.location.origin,
    );

    if (token) {
      url.searchParams.set("token", token);
    }

    return `${url.pathname}${url.search}`;
  },
};
