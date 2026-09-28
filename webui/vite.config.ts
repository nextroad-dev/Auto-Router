import { fileURLToPath, URL } from 'node:url'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'
import { defineConfig } from 'vitest/config'

export default defineConfig({
  base: '/admin/static/web/',
  plugins: [vue(), tailwindcss()],
  resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
  build: {
    outDir: '../internal/api/dashboard/static/web',
    emptyOutDir: true,
    // Font faces are CSP-restricted to same-origin files and need their own immutable URLs;
    // do not inline small WOFF2 slices as data: URLs.
    assetsInlineLimit: 0,
    assetsDir: 'assets',
    rollupOptions: { output: { entryFileNames: 'assets/[name]-[hash].js', chunkFileNames: 'assets/[name]-[hash].js', assetFileNames: 'assets/[name]-[hash][extname]' } },
  },
  test: { environment: 'jsdom', include: ['tests/**/*.test.ts'] },
})
