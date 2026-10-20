import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// In development the Go server runs on :8080 and Vite proxies /api to it.
// In production the Go binary embeds dist/ and serves everything from one origin.
export default defineConfig({
  plugins: [react()],
  server: { port: 5173, proxy: { '/api': { target: 'http://localhost:8080', changeOrigin: false } } },
  build: { outDir: 'dist', emptyOutDir: true, sourcemap: false, chunkSizeWarningLimit: 900 },
})
