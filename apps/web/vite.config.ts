import { fileURLToPath } from 'node:url'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { defineConfig } from 'vite'

const repoRoot = fileURLToPath(new URL('../..', import.meta.url))

export default defineConfig({
  plugins: [react(), tailwindcss()],
  worker: { format: 'es' },
  // Templates are imported from engine/examples (one source of truth).
  server: { fs: { allow: [repoRoot] } },
  build: { target: 'es2022', chunkSizeWarningLimit: 1500 },
})
