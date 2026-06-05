import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import fs from 'fs'

// Dev-server TLS: only enabled when the mounted certs are present. `vite build`
// (and any environment without /certs) must not fail trying to read them.
function devHttps() {
  try {
    return {
      key: fs.readFileSync('/certs/frontend.key'),
      cert: fs.readFileSync('/certs/frontend.crt'),
    }
  } catch {
    return undefined
  }
}

// https://vitejs.dev/config/
export default defineConfig({
  plugins: [react()],
  server: {
    host: '0.0.0.0',
    port: 5173,
    https: devHttps(),
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
