import { test, expect } from '@playwright/test'
import { resetSessions } from './helpers.js'

// What makes "Add to Dock" produce a real app — a name, an icon and its own
// window — rather than a nameless shortcut wearing the favicon.

test.beforeEach(async ({ page }) => {
  await resetSessions(page)
})

test('the page declares a manifest and an apple touch icon', async ({ page }) => {
  await expect(page.locator('link[rel="manifest"]')).toHaveAttribute(
    'href',
    '/manifest.webmanifest',
  )
  await expect(page.locator('link[rel="apple-touch-icon"]')).toHaveCount(1)
  await expect(page.locator('meta[name="theme-color"]')).toHaveAttribute('content', '#0d1117')
})

test('the manifest is served as a manifest, not as text', async ({ page, request }) => {
  const res = await request.get('/manifest.webmanifest')
  expect(res.status()).toBe(200)
  // Sniffed as text/plain, some browsers refuse it outright. The runtime image
  // has no system mime table, so this type is set explicitly server-side.
  expect(res.headers()['content-type']).toContain('application/manifest+json')

  const m = await res.json()
  expect(m.display).toBe('standalone')
  expect(m.start_url).toBe('/')
  expect(m.name).toBeTruthy()
})

test('every icon the manifest names actually exists', async ({ request }) => {
  // The classic silent failure: a manifest pointing at an icon that 404s
  // installs happily and then shows a blank tile.
  const m = await (await request.get('/manifest.webmanifest')).json()
  expect(m.icons.length).toBeGreaterThan(0)

  for (const icon of m.icons) {
    const res = await request.get(icon.src)
    expect(res.status(), `${icon.src} must resolve`).toBe(200)
    expect(res.headers()['content-type']).toContain('image/png')
  }

  // A maskable icon is what keeps the mark from being cropped into a circle.
  expect(m.icons.some((i) => i.purpose === 'maskable')).toBe(true)

  const apple = await request.get('/apple-touch-icon.png')
  expect(apple.status()).toBe(200)
})

test('the full screen button asks the browser for full screen', async ({ page }) => {
  const btn = page.locator('#fullscreen')
  await expect(btn).toBeVisible()

  // Stubbed rather than actually entering full screen: a headless window may
  // decline, and what matters here is that the button is wired to the API.
  await page.evaluate(() => {
    window.__fs = 0
    document.documentElement.requestFullscreen = () => {
      window.__fs++
      return Promise.resolve()
    }
  })

  await btn.click()
  expect(await page.evaluate(() => window.__fs)).toBe(1)
})

test('the button says how to get back out once full screen', async ({ page }) => {
  await page.evaluate(() => {
    // Pretend the browser granted it, then announce the change the way it does.
    Object.defineProperty(document, 'fullscreenElement', {
      configurable: true,
      get: () => document.documentElement,
    })
    document.dispatchEvent(new Event('fullscreenchange'))
  })

  await expect(page.locator('#fullscreen-label')).toHaveText('Exit')
  await expect(page.locator('#fullscreen')).toHaveAttribute('data-on', 'true')
})
