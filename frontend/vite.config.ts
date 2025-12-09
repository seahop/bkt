import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import fs from 'fs'

// https://vitejs.dev/config/
export default defineConfig({
  plugins: [react()],
  server: {
    host: '0.0.0.0',
    port: 5173,
    https: {
      key: fs.readFileSync('/certs/frontend.key'),
      cert: fs.readFileSync('/certs/frontend.crt'),
    },
    proxy: {
      '/api': {
        target: 'https://backend:9443',
        changeOrigin: true,
        secure: false, // Accept self-signed certificates in development
      },
    },
    watch: {
      usePolling: true,
    },
  },
})
