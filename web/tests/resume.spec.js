import { test, expect } from '@playwright/test'
import { resetSessions, newTab, waitForConnected } from './helpers.js'

test.beforeEach(async ({ page }) => {
  await resetSessions(page)
})

// Remote shells keep running when papaya stops. Nothing in the browser knows
// about them, so without this the only way back in is to recreate a tab with
// exactly the original name — including the "-2" a second unnamed tab was
// silently given, which nobody would guess.
test('work left running is offered on the empty state', async ({ page }) => {
  const panel = page.locator('#resumable')
  await expect(panel).toBeVisible()

  const rows = page.locator('#resumable-list button')
  await expect(rows).toHaveCount(2)
  await expect(rows.first()).toContainText('devbox-1')

  const labels = await rows.allInnerTexts()
  expect(labels.join(' ')).toContain('leftover')
  expect(labels.join(' ')).toContain('devbox-1-2')
})

test('clicking one opens a tab attached to it', async ({ page }) => {
  const row = page.locator('#resumable-list button', { hasText: 'leftover' })
  await row.click()

  await expect(page.locator('[data-tab-id]')).toHaveCount(1)
  await waitForConnected(page)
  await expect(page.locator('[data-tab-id]').first()).toContainText('leftover')

  // It is on screen now, so it is no longer something to reopen.
  await expect(page.locator('#resumable-list button', { hasText: 'leftover' })).toHaveCount(0)
})

test('resume all opens every one of them', async ({ page }) => {
  await page.click('#resume-all')
  await expect(page.locator('[data-tab-id]')).toHaveCount(2)
  await expect(page.locator('#resumable')).toBeHidden()
})

// The empty state is where this belongs; it must not clutter a working session.
test('the offer is hidden while a tab is open and returns when the last closes',
  async ({ page }) => {
    await newTab(page, { name: 'working' })
    await expect(page.locator('#empty-state')).toBeHidden()

    // The tab's own × closes without asking; the confirmation only guards the
    // keyboard path, where there is no deliberate click on a specific tab.
    await page.locator('[data-tab-id] button[aria-label="Close"]').first().click()
    await expect(page.locator('[data-tab-id]')).toHaveCount(0)

    await expect(page.locator('#empty-state')).toBeVisible()
    await expect(page.locator('#resumable')).toBeVisible()
  })
