import { test, expect } from '@playwright/test'
import { resetSessions, newTab, waitForConnected } from './helpers.js'

// Reloading always restored the tabs, because the server holds the session
// list — but that list lives in memory and dies with the process. Restarting
// papaya therefore emptied the tab bar while every remote shell carried on
// running in tmux, and the only way back in was to remember the workspace name
// and type it exactly. These cover the browser remembering it instead.

test.beforeEach(async ({ page }) => {
  await resetSessions(page)
})

const STORAGE_KEY = 'papaya.tabs.v1'

const remembered = (page) =>
  page.evaluate((k) => JSON.parse(localStorage.getItem(k) || 'null'), STORAGE_KEY)

test('an open tab is remembered by its workspace, not its session id', async ({ page }) => {
  await newTab(page, { name: 'ledger' })

  // The sweep runs on a one second tick.
  await expect.poll(async () => (await remembered(page))?.entries?.length ?? 0).toBe(1)

  const saved = await remembered(page)
  const entry = saved.entries[0]
  expect(entry.workspace).toBe('papaya-ledger')
  expect(entry.host).toBe('devbox-1')
  expect(entry.user).toBeTruthy()
  // A session id would be worthless here: it dies with the process that issued
  // it, while the workspace outlives papaya on the remote host.
  expect(entry.id).toBeUndefined()
})

test('a remembered workspace is reattached when the server has no record', async ({ page }) => {
  // The state after a papaya restart: no sessions server-side, while the remote
  // workspace carries on running. "papaya-leftover" is exactly that — a
  // workspace the harness leaves running that no session is attached to.
  await expect(page.locator('#empty-state')).toBeVisible()

  await page.evaluate(
    ([k, value]) => localStorage.setItem(k, value),
    [
      STORAGE_KEY,
      JSON.stringify({
        at: Date.now(),
        entries: [
          { host: 'devbox-1', user: 'tester', workspace: 'papaya-leftover', name: 'leftover', order: 0 },
        ],
      }),
    ],
  )

  await page.reload()

  // Back on the same shell, with nobody typing a workspace name.
  await expect(page.locator('[data-tab-id]')).toHaveCount(1)
  await waitForConnected(page)
  await expect(page.locator('[data-tab-id]').first()).toContainText('leftover')

  // And it is no longer offered as resumable, being attached now.
  await expect(page.locator('#resumable-list button', { hasText: 'leftover' })).toHaveCount(0)
})

test('a workspace that is gone is not resurrected as an empty tab', async ({ page }) => {
  // A remembered entry is a hint, never an instruction. Reattaching a workspace
  // that has since died would open an empty shell wearing the name of work that
  // no longer exists, which is worse than an empty tab bar.
  await page.evaluate(
    ([k, value]) => localStorage.setItem(k, value),
    [
      STORAGE_KEY,
      JSON.stringify({
        at: Date.now(),
        entries: [
          { host: 'devbox-1', user: 'tester', workspace: 'papaya-vanished', name: 'ghost', order: 0 },
        ],
      }),
    ],
  )

  await page.reload()

  await expect(page.locator('#empty-state')).toBeVisible()
  await expect(page.locator('[data-tab-id]')).toHaveCount(0)
})

test('a remembered set older than a month is discarded', async ({ page }) => {
  await page.evaluate(
    ([k, value]) => localStorage.setItem(k, value),
    [
      STORAGE_KEY,
      JSON.stringify({
        at: Date.now() - 31 * 24 * 60 * 60 * 1000,
        entries: [
          { host: 'devbox-1', user: 'tester', workspace: 'papaya-leftover', name: 'old', order: 0 },
        ],
      }),
    ],
  )

  await page.reload()

  // "leftover" really is still running on devbox-1, so this proves the entry was
  // rejected on age rather than for being unresumable.
  await expect(page.locator('[data-tab-id]')).toHaveCount(0)
  await expect(page.locator('#resumable-list button', { hasText: 'leftover' })).toBeVisible()
})

// The grid arrangement had the same flaw as the tab bar, one layer down. It was
// already persisted, so an ordinary reload kept it — but its leaves hold session
// ids, and those are reissued when papaya restarts. Every saved leaf then looks
// dead, gets dropped, and the panels rebuild as a default tiling: the sessions
// come back, arranged wrongly. Pinning each leaf to its workspace fixes it.
test('the grid arrangement survives the server reissuing session ids', async ({ page }) => {
  await expect(page.locator('#empty-state')).toBeVisible()

  await page.evaluate(
    ([tabsKey, entries, layout, view]) => {
      localStorage.setItem(tabsKey, entries)
      localStorage.setItem('papaya.layout.v1', layout)
      localStorage.setItem('papaya.view.v1', view)
    },
    [
      STORAGE_KEY,
      JSON.stringify({
        at: Date.now(),
        entries: [
          { host: 'devbox-1', user: 'tester', workspace: 'papaya-leftover', name: 'left', order: 0 },
          { host: 'devbox-1', user: 'tester', workspace: 'papaya-devbox-1-2', name: 'right', order: 1 },
        ],
      }),
      // Ids from a process that no longer exists — exactly what a restart
      // leaves behind. Only the workspace stamps can resolve these.
      JSON.stringify({
        type: 'split',
        dir: 'row',
        ratio: 0.75,
        a: { type: 'leaf', id: 'dead-id-a', w: 'papaya-leftover' },
        b: { type: 'leaf', id: 'dead-id-b', w: 'papaya-devbox-1-2' },
      }),
      'grid',
    ],
  )

  await page.reload()

  await expect(page.locator('#grid')).toBeVisible()
  await expect(page.locator('[data-panel-id]')).toHaveCount(2)
  await page.waitForTimeout(600)

  // 0.75, not the 0.5 a rebuilt-from-scratch layout would give.
  const first = await page.locator('[data-panel-id]').nth(0).boundingBox()
  const second = await page.locator('[data-panel-id]').nth(1).boundingBox()
  const share = first.width / (first.width + second.width)
  expect(share).toBeGreaterThan(0.6)
  expect(share).toBeLessThan(0.9)
})
