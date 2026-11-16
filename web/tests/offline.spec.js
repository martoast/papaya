import { test, expect } from '@playwright/test'
import { resetSessions, termText } from './helpers.js'

// What happens when the connection cannot be made is as much a feature as what
// happens when it can. Both failures below used to look identical on screen —
// a tab stuck on "detached" forever — and neither said what to do about it.

test.beforeEach(async ({ page }) => {
  await resetSessions(page)
})

test('a session the server has forgotten stops retrying and says so', async ({ page }) => {
  // The real scenario: papaya restarts, its in-memory session list is empty,
  // and a browser tab still holds ids from before. Reproduced by serving one
  // phantom session to the boot request only — every later call reaches the
  // real server, which has never heard of it.
  let injected = false
  await page.route('**/api/sessions', async (route) => {
    if (route.request().method() === 'GET' && !injected) {
      injected = true
      await route.fulfill({
        json: [
          {
            id: '00000000-dead-4000-8000-000000000000',
            name: 'ghost',
            host: 'devbox-1',
            user: 'dev',
            state: 'connecting',
            order: 0,
            createdAt: '2026-01-01T00:00:00Z',
            attention: false,
          },
        ],
      })
      return
    }
    await route.continue()
  })

  await page.goto('/')

  // The tab gives up rather than retrying on a 10s ceiling until the heat death
  // of the universe.
  await expect(page.locator('[data-tab-id] span[data-state]')).toHaveAttribute(
    'data-state',
    'closed',
    { timeout: 20_000 },
  )

  // And it explains itself where the user is already looking.
  expect(await termText(page)).toContain('Papaya restarted')
})

test('losing the tailnet is named on screen, and clears itself when it returns', async ({
  page,
}) => {
  await page.goto('/')
  const banner = page.locator('#offline-banner')
  await expect(banner).toBeHidden()

  // Nothing answers: exactly what an unreachable 100.x address looks like.
  await page.route('**/healthz', (route) => route.abort('failed'))
  await page.evaluate(() => window.dispatchEvent(new Event('offline')))

  await expect(banner).toBeVisible()
  await expect(banner).toContainText('Tailscale')

  // Tailscale comes back. The banner must go on its own — a user who fixed the
  // connection should not also have to work out that a reload is needed.
  await page.unroute('**/healthz')
  await page.evaluate(() => window.dispatchEvent(new Event('online')))
  await expect(banner).toBeHidden({ timeout: 15_000 })
})

test('the offline page explains Tailscale, then steps aside when the server answers', async ({
  page,
}) => {
  // This page is what the service worker hands back when the server cannot be
  // reached at all, so it has to name the cause without loading anything else.
  await page.route('**/healthz', (route) => route.abort('failed'))

  const res = await page.goto('/offline.html')
  expect(res.status()).toBe(200)
  await expect(page.locator('h1')).toContainText("Can't reach Papaya")
  await expect(page.locator('body')).toContainText('Tailscale')
  await expect(page.locator('#status')).toContainText('still unreachable')

  // Tailscale comes back. That happens in a different window entirely, so the
  // page has to return to the app on its own rather than stranding the user on
  // an error screen that is no longer true.
  await page.unroute('**/healthz')
  await page.waitForURL((url) => url.pathname === '/', { timeout: 15_000 })
})

// The failure that matters most, and the one the first attempt at this got
// wrong. Leaving the tailnet does not produce an error: the address stops being
// routable and requests hang until the TCP stack gives up, which on macOS is
// over a minute. Anything that waits for a rejection waits exactly as long as
// having no fallback at all — which is what "the page just hangs" looks like.
test('a request that hangs rather than fails still reaches the explanation', async ({
  page,
}) => {
  await page.goto('/')
  await page.evaluate(async () => navigator.serviceWorker.ready)

  // Never resolve, never reject: an unroutable 100.x address, exactly.
  await page.route('**/healthz', () => {})
  await page.evaluate(() => window.dispatchEvent(new Event('offline')))

  // The banner must appear on the probe deadline, not on a rejection that is
  // never coming.
  await expect(page.locator('#offline-banner')).toBeVisible({ timeout: 12_000 })
  await expect(page.locator('#offline-banner')).toContainText('Tailscale')
})

test('a page load that hangs falls back instead of spinning', async ({ page, context }) => {
  await page.goto('/')
  // Waiting for `ready` is not enough: it resolves on activation, while taking
  // control of this page happens a moment later via clients.claim(). Hanging
  // the network in that gap leaves the navigation with no worker to answer it,
  // which fails as a 30s timeout and looks exactly like the bug under test.
  await page.waitForFunction(() => Boolean(navigator.serviceWorker.controller))

  // The navigation itself never answers. Without a deadline in the worker this
  // spins for as long as the browser is willing to wait.
  await context.route('**/index.html', () => {})
  await context.route(`${new URL(page.url()).origin}/`, () => {})

  // The navigation cannot resolve until the worker gives up on the network, so
  // both waits have to clear that deadline with room to spare on a loaded
  // machine — the whole point is that this takes seconds rather than the
  // minute-plus the browser would otherwise spend.
  await page.goto('/', { waitUntil: 'commit', timeout: 30_000 })
  await expect(page.locator('h1')).toContainText("Can't reach Papaya", { timeout: 25_000 })
})

test('the service worker registers and serves only the fallback', async ({ page }) => {
  await page.goto('/')

  const scope = await page.evaluate(async () => {
    const reg = await navigator.serviceWorker.ready
    return reg.scope
  })
  expect(scope).toContain('/')

  // It must cache the fallback and nothing else: caching the app bundle would
  // let a stale build shadow a deploy.
  const cached = await page.evaluate(async () => {
    const names = await caches.keys()
    const out = []
    for (const n of names) {
      const keys = await caches.open(n).then((c) => c.keys())
      out.push(...keys.map((r) => new URL(r.url).pathname))
    }
    return out.sort()
  })
  expect(cached).toEqual(['/offline.html', '/offline.js'])
})
