import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    // The browser calls /api/... on the dev server, which forwards to pos-api.
    // Same origin, so pos-api needs no CORS setup.
    proxy: {
      '/api': {
        target: process.env.API_URL ?? 'http://localhost:8080',
        rewrite: (path) => path.replace(/^\/api/, ''),
      },
    },
  },
})
