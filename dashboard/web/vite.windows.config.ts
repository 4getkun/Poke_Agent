import { fileURLToPath, URL } from 'node:url'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

const root = fileURLToPath(new URL('.', import.meta.url))

export default defineConfig({
  root,
  base: './',
  publicDir: false,
  plugins: [react()],
  build: {
    outDir: 'dist-windows',
    emptyOutDir: true,
    rollupOptions: {
      input: fileURLToPath(new URL('./windows.html', import.meta.url)),
    },
  },
})
