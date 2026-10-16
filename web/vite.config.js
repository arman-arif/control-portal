import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

const panel = process.env.KEEL_PANEL_URL || 'http://localhost:8080'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    proxy: {
      '/api': { target: panel, ws: true },
      '/install.sh': panel,
      '/downloads': panel,
    },
  },
})
