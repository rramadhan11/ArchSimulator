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
  build: {
    target: 'es2022',
    chunkSizeWarningLimit: 600, // the lazy ECharts chunk is ~550 kB
    rollupOptions: {
      output: {
        // Libraries change less often than the app: their own chunk stays cached across deploys.
        manualChunks: (id) => (/node_modules\/(react|react-dom|scheduler|@xyflow|d3-|zustand|classcat)\//.test(id) ? 'vendor' : undefined),
      },
    },
  },
})
