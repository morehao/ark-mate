import { defineConfig } from "vite"
import react from "@vitejs/plugin-react"

export default defineConfig({
  plugins: [react()],
  build: {
    outDir: "dist",
  },
  server: {
    // dev：面板与后端 forge serve（默认 :8099）同源代理联调
    proxy: {
      "/v1": {
        target: "http://127.0.0.1:8099",
        changeOrigin: true,
        ws: true, // /v1/forge/ws/events 升级为 WS
      },
    },
  },
})
