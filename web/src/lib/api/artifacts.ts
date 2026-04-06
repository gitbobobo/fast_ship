import { useAuthStore } from "@/lib/store/auth-store";
import { api } from "./client";

function handleUnauthorizedUpload() {
  useAuthStore.getState().logout();
  window.location.href = "/login";
}

export const artifactApi = {
  upload: (
    vid: string,
    formData: FormData,
    onProgress?: (percent: number) => void,
  ) =>
    new Promise<ApiResponse<Artifact>>((resolve, reject) => {
      const xhr = new XMLHttpRequest();
      xhr.open("POST", `/api/versions/${vid}/artifacts`);

      const token = useAuthStore.getState().token;
      if (token) {
        xhr.setRequestHeader("Authorization", `Bearer ${token}`);
      }

      xhr.upload.addEventListener("progress", (event) => {
        if (!event.lengthComputable || !onProgress) return;
        const percent = Math.round((event.loaded / event.total) * 100);
        onProgress(percent);
      });

      xhr.addEventListener("load", () => {
        if (xhr.status === 401) {
          handleUnauthorizedUpload();
          reject(new Error("Unauthorized"));
          return;
        }

        let response: ApiResponse<Artifact> | null = null;
        if (xhr.responseText) {
          response = JSON.parse(xhr.responseText) as ApiResponse<Artifact>;
        }

        if (xhr.status >= 200 && xhr.status < 300 && response) {
          resolve(response);
          return;
        }

        reject(new Error(response?.message || "上传失败"));
      });

      xhr.addEventListener("error", () => {
        reject(new Error("上传失败"));
      });

      xhr.send(formData);
    }),

  delete: (aid: string) =>
    api.delete(`artifacts/${aid}`).json<ApiResponse<null>>(),

  downloadUrl: (aid: string) => `/api/artifacts/${aid}/download`,
};
