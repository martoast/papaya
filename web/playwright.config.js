import { defineConfig, devices } from '@playwright/test'

const PORT = 8099

export default defineConfig({
  testDir: './tests',
  timeout: 45_000,
  expect: { timeout: 10_000 },
  fullyParallel: false, // the dev server holds shared session state
  workers: 1,
  retries: 0, // a flake is a bug; do not paper over it with retries
  reporter: [['list']],

  use: {
    baseURL: `http://127.0.0.1:${PORT}`,
    trace: 'retain-on-failure',
    video: 'off',
  },

  projects: [
    {
      name: 'chromium',
      use: {
        ...devices['Desktop Chrome'],
        // http://127.0.0.1 is a secure context, so getUserMedia is available.
        // These flags auto-grant the prompt and feed a synthetic audio track,
        // which is enough to exercise the record/upload/inject path.
        permissions: ['microphone', 'clipboard-read', 'clipboard-write'],
        launchOptions: {
          args: [
            '--use-fake-ui-for-media-stream',
            '--use-fake-device-for-media-stream',
          ],
        },
      },
    },
  ],

  // Serves the real built frontend against the real Go server, with the tailnet
  // identity faked. Building first is required: the binary embeds web/dist.
  webServer: {
    command:
      'npm run build && GOTOOLCHAIN=auto go run ../test/devserver ' +
      `-addr 127.0.0.1:${PORT}`,
    url: `http://127.0.0.1:${PORT}/healthz`,
    reuseExistingServer: !process.env.CI,
    timeout: 180_000,
    cwd: '.',
    stdout: 'pipe',
    stderr: 'pipe',
  },
})
