import { fileURLToPath, URL } from 'node:url'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'
import ui from '@nuxt/ui/vite'
import { defineConfig } from 'vitest/config'

export default defineConfig({
  base: '/admin/static/web/',
  plugins: [vue(), tailwindcss(), ui({
    colorMode: false,
    ui: {
      colors: { primary: 'green', neutral: 'zinc' },
      // Prevent Nuxt UI from injecting automatic slot dividers; keep only explicit page separators.
      card: {
        variants: {
          variant: {
            outline: { root: 'divide-y-0' },
            soft: { root: 'divide-y-0' },
            subtle: { root: 'divide-y-0' },
          },
        },
      },
      slideover: {
        slots: { content: 'divide-y-0', body: 'border-t-0' },
      },
    },
    icon: { clientBundle: { scan: { globInclude: ['src/**/*.{vue,ts}'] } } },
  })],
  resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
  build: {
    outDir: '../internal/api/dashboard/static/web',
    emptyOutDir: true,
    assetsDir: 'assets',
    rollupOptions: { output: { entryFileNames: 'assets/[name]-[hash].js', chunkFileNames: 'assets/[name]-[hash].js', assetFileNames: 'assets/[name]-[hash][extname]' } },
  },
  test: { environment: 'jsdom', include: ['tests/**/*.test.ts'] },
})
