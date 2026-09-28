import { fileURLToPath, URL } from 'node:url'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'
import { defineConfig } from 'vite'

// The e2e server serves from the root so Playwright can reach /admin/* shells without the
// production asset base; the plugin set must stay identical to vite.config.ts.
export default defineConfig({
  base: '/',
  plugins: [vue(), tailwindcss()],
  resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
})
