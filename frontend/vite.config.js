import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

// https://vitejs.dev/config/
export default defineConfig({
  plugins: [react(), tailwindcss()],
  // 开发服务器（make dev / docker-compose.yaml）
  server: {
    host: '0.0.0.0',
    port: 5173,
    hmr: {
      clientPort: 5173
    },
    proxy: {
      '/api': {
        target: 'http://api:3000',
        changeOrigin: true,
        rewrite: (path) => path.replace(/^\/api/, ''),
      },
      '/v1': {
        target: 'http://api:3000',
        changeOrigin: true,
      },
    }
  },
  // 生产预览服务器（docker-compose.pi.yaml — vite preview）
  preview: {
    host: '0.0.0.0',
    port: 4173,
    proxy: {
      '/api': {
        target: 'http://api:3000',
        changeOrigin: true,
        rewrite: (path) => path.replace(/^\/api/, ''),
      },
      '/v1': {
        target: 'http://api:3000',
        changeOrigin: true,
      },
    }
  }
})
