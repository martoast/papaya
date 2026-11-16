import { test, expect } from '@playwright/test'
import { resetSessions, newTab, waitForConnected } from './helpers.js'

// A closed lid suspends everything. On wake the sockets are dead, and if papaya
// restarted meanwhile its session list is empty — so the ids the tabs hold refer
// to nothing and reconnecting can never work. An installed app has no address
// bar, so there was no reload to fall back on either: the tabs simply went stale
// and stayed that way.

test.beforeEach(async ({ page }) => {
  await resetSessions(page)
  await page.evaluate(() => {
    localStorage.removeItem('papaya.layout.v1')
    localStorage.removeItem('papaya.view.v1')
  })
})

const STORAGE_KEY = 'papaya.tabs.v1'

// The state a restarted papaya leaves behind: no sessions server-side, while the
// remote workspace carries on running. "papaya-leftover" is exactly that in the
// harness — running, and attached to nothing.
async function rememberLeftover(page) {
  await page.evaluate(
    ([k, value]) => localStorage.setItem(k, value),
    [
      STORAGE_KEY,
      JSON.stringify({
        at: Date.now(),
        entries: [
          {
            host: 'devbox-1',
            user: 'tester',
            workspace: 'papaya-leftover',
            name: 'leftover',
            order: 0,
          },
        ],
      }),
    ],
  )
}

test('waking up brings back work the server has no session for', async ({ page }) => {
  await expect(page.locator('#empty-state')).toBeVisible()
  await rememberLeftover(page)

  // The lid opening, as the browser reports it. No reload.
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))

  await expect(page.locator('[data-tab-id]')).toHaveCount(1)
  await waitForConnected(page)
  await expect(page.locator('[data-tab-id]').first()).toContainText('leftover')
})

test('the reconnect button does the same thing on demand', async ({ page }) => {
  await expect(page.locator('#empty-state')).toBeVisible()
  await rememberLeftover(page)

  // Clicked directly: the button is hidden until something looks wrong, and
  // what is being tested is that it runs the same recovery.
  await page.evaluate(() => document.getElementById('reconnect').click())

  await expect(page.locator('[data-tab-id]')).toHaveCount(1)
  await waitForConnected(page)
})

test('the reconnect button appears only when something is wrong', async ({ page }) => {
  await newTab(page, { name: 'ledger' })
  const btn = page.locator('#reconnect')
  await expect(btn).toBeHidden()

  // Nothing answers: exactly what an unroutable tailnet address looks like.
  await page.route('**/healthz', (route) => route.abort('failed'))
  await page.evaluate(() => window.dispatchEvent(new Event('offline')))

  await expect(btn).toBeVisible()
  await expect(btn).toContainText('Reconnect')
})

test('a healthy wake leaves the tabs exactly as they were', async ({ page }) => {
  await newTab(page, { name: 'alpha' })
  await newTab(page, { name: 'beta' })
  const before = await page
    .locator('[data-tab-id]')
    .evaluateAll((els) => els.map((e) => e.dataset.tabId))

  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await page.waitForTimeout(800)

  // Same sessions, same ids: waking must not churn tabs that are perfectly fine.
  const after = await page
    .locator('[data-tab-id]')
    .evaluateAll((els) => els.map((e) => e.dataset.tabId))
  expect(after).toEqual(before)
  await expect(page.locator('#reconnect')).toBeHidden()
})

// The safety property. close() sends a delete, and a delete kills the remote
// workspace — so if the session list were ever wrong about a session being gone,
// recovering from a stale tab would destroy the very work it was recovering.
test('a forgotten tab is dropped locally and never deleted server-side', async ({ page, request }) => {
  await newTab(page, { name: 'ledger' })

  const deletes = []
  await page.route('**/api/sessions/*', async (route) => {
    if (route.request().method() === 'DELETE') deletes.push(route.request().url())
    await route.continue()
  })

  // Claim, once, that the server has nothing — the shape of a bad response as
  // much as of a restart.
  let emptied = false
  await page.route('**/api/sessions', async (route) => {
    if (route.request().method() === 'GET' && !emptied) {
      emptied = true
      await route.fulfill({ json: [] })
      return
    }
    await route.continue()
  })

  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await page.waitForTimeout(1200)

  expect(deletes, 'no session may be deleted while recovering').toEqual([])

  // And the session is still there — untouched, which is what makes the work
  // recoverable. It is absent from /api/resumable only because it is still
  // attached, which is the server being right rather than anything going wrong.
  const sessions = await (await request.get('/api/sessions')).json()
  expect(sessions.some((s) => s.workspace === 'papaya-ledger')).toBe(true)
})
