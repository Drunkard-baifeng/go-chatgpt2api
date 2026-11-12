import path from 'node:path'
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

const proxyTarget = process.env.VITE_DEV_API_TARGET || 'http://127.0.0.1:8100'
const proxy = Object.fromEntries(
  ['/api', '/auth', '/version', '/images', '/image-thumbnails', '/v1', '/files']
    .map(prefix => [prefix, { target: proxyTarget, changeOrigin: true }]),
)

export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
      '~': path.resolve(__dirname, './src'),
    },
    dedupe: ['vue', 'vue-router', 'pinia', '@iconify/vue', 'nanocat-ui', 'axios', 'vue-virtual-scroller'],
  },
  server: { host: '0.0.0.0', port: 5176, strictPort: true, proxy },
  preview: { port: 4176, strictPort: true, proxy },
  build: { outDir: 'dist', emptyOutDir: true },
})
