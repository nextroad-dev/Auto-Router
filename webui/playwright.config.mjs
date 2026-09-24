import { defineConfig } from '@playwright/test'

export default defineConfig({
  testDir: './e2e',
  timeout: 45_000,
  expect: { timeout: 10_000 },
  reporter: 'list',
  workers: 1,
  use: {
    baseURL: 'http://127.0.0.1:41737',
    browserName: 'chromium',
    headless: true,
    trace: 'retain-on-failure',
  },
  webServer: {
    command: 'npm run dev -- --config vite.e2e.config.ts --host 127.0.0.1 --port 41737 --strictPort',
    url: 'http://127.0.0.1:41737/admin/login',
    timeout: 60_000,
    reuseExistingServer: false,
    stdout: 'pipe',
    stderr: 'pipe',
  },
})
