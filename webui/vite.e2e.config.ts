import { fileURLToPath, URL } from 'node:url'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'
import ui from '@nuxt/ui/vite'
import { defineConfig } from 'vite'

export default defineConfig({
  base: '/',
  plugins: [vue(), tailwindcss(), ui({ colorMode: false, ui: { colors: { primary: 'green', neutral: 'zinc' } }, icon: { clientBundle: { scan: { globInclude: ['src/**/*.{vue,ts}'] } } } })],
  resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
})
