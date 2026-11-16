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
  await expect(page.locator('meta[name="theme-color"]')).toHaveAttribute('content', '#08090d')
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

// Home screen install: a real one-click install where the browser offers it,
// the Add to Home Screen guide on iPhone, where nothing else is possible.
test('on an iPhone, Papaya shows how to add it to the Home Screen, once', async ({ browser }) => {
  const ctx = await browser.newContext({
    userAgent: 'Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1',
    viewport: { width: 390, height: 844 },
    hasTouch: true,
    isMobile: true,
  })
  const page = await ctx.newPage()
  await resetSessions(page)
  const guide = page.locator('#install-guide')
  await expect(guide).toBeVisible({ timeout: 8000 })
  await expect(guide).toContainText('Add to Home Screen')
  await expect(page.locator('#install-app')).toBeVisible()

  await guide.locator('.ig-close').click()
  await expect(guide).toHaveCount(0)
  await page.reload()
  await page.waitForTimeout(5000)
  await expect(guide).toHaveCount(0) // dismissed stays dismissed

  // The status-bar button brings it back on request.
  await page.locator('#install-app').click()
  await expect(guide).toBeVisible()
  await ctx.close()
})

test('where the browser can install apps, one click installs Papaya', async ({ page }) => {
  await expect(page.locator('#install-app')).toBeHidden()
  await page.evaluate(() => {
    const e = new Event('beforeinstallprompt', { cancelable: true })
    e.prompt = () => { window.__prompted = true }
    e.userChoice = Promise.resolve({ outcome: 'accepted' })
    window.dispatchEvent(e)
  })
  await expect(page.locator('#install-app')).toBeVisible()
  await page.locator('#install-app').click()
  expect(await page.evaluate(() => window.__prompted)).toBe(true)
  await expect(page.locator('#install-app')).toBeHidden()
})
