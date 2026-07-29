/// <reference types="vitest/config" />
import fs from "fs"
import path from "path"
import { defineConfig } from "vite"
import react from "@vitejs/plugin-react"
import tailwindcss from "@tailwindcss/vite"

const appVersion = fs.readFileSync(path.resolve(__dirname, "../VERSION"), "utf-8").trim()

export default defineConfig({
  define: {
    __APP_VERSION__: JSON.stringify(appVersion),
  },
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
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
