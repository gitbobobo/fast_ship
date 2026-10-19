/// <reference types="vitest/config" />
import fs from "fs"
import path from "path"
import { defineConfig } from "vite"
import react from "@vitejs/plugin-react"
import tailwindcss from "@tailwindcss/vite"

const appVersion = fs.readFileSync(path.resolve(import.meta.dirname, "../VERSION"), "utf-8").trim()

export default defineConfig({
  define: {
    __APP_VERSION__: JSON.stringify(appVersion),
  },
  plugins: [react(), tailwindcss()],
  // markdown-editor（@uiw/react-md-editor）本身就是单个懒加载 chunk，约 730 kB；
  // 它只在文档编辑页加载。拆小需要换编辑器或深改依赖，是独立任务——阈值放到
  // 800 kB 只覆盖这一块，其余 chunk 超限仍会告警。
  build: {
    chunkSizeWarningLimit: 800,
  },
  resolve: {
    alias: {
      "@": path.resolve(import.meta.dirname, "./src"),
    },
  },
  server: {
    host: true,
    port: 4999,
    proxy: {
      "/api": {
        target: "http://localhost:4888",
        changeOrigin: true,
      },
    },
  },
  test: {
    environment: "happy-dom",
    pool: "forks",
    globals: true,
    setupFiles: "./src/test/setup.ts",
  },
})
