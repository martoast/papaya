import { test, expect } from '@playwright/test'
import { resetSessions, newTab } from './helpers.js'

// A tab says what its program is doing, worked out from the output stream:
// working while output keeps coming, "done" (your turn) when a stretch of work
// ends while nobody is looking, idle otherwise. The fake shell's `work N`
// prints a spinner frame every 100 ms for N seconds, like an agent.

async function typeIn(page, text) {
  await page.locator('.term-host[data-active="true"] .xterm').click()
  await page.keyboard.type(text)
  await page.keyboard.press('Enter')
}

test.beforeEach(async ({ page }) => {
  await resetSessions(page)
})

test('an agent that finishes while you look elsewhere leaves its tab saying "your turn"', async ({ page }) => {
  const first = await newTab(page, { name: 'one' })
  const second = await newTab(page, { name: 'two' })
  await page.waitForTimeout(2200) // past the reconnect replay window

  await typeIn(page, 'work 5')
  await first.click() // look at the other tab while two works

  await expect(second).toHaveAttribute('data-activity', 'working', { timeout: 6000 })
  await expect(second.locator('.tab-dot')).toHaveCSS('animation-name', 'papaya-spin')
  await expect(second).toHaveAttribute('data-activity', 'done', { timeout: 12_000 })
  await expect(page).toHaveTitle(/^\(1\) /)
  await expect(second).toHaveAttribute('title', /your turn/)

  // Looking at it answers it.
  await second.click()
  await expect(second).toHaveAttribute('data-activity', 'idle')
  await expect(page).not.toHaveTitle(/^\(\d+\) /)
})

test('work you watched does not ask for your attention afterwards', async ({ page }) => {
  const tab = await newTab(page, { name: 'watched' })
  await page.waitForTimeout(2200)
  await typeIn(page, 'work 4')
  await expect(tab).toHaveAttribute('data-activity', 'working', { timeout: 6000 })
  await expect(tab).toHaveAttribute('data-activity', 'idle', { timeout: 12_000 })
  await expect(page).not.toHaveTitle(/^\(\d+\) /)
})

test('typing and a quick command are not "working"', async ({ page }) => {
  const tab = await newTab(page, { name: 'quick' })
  await page.waitForTimeout(2200)
  await typeIn(page, 'echo hello there, this is just typing')
  await page.waitForTimeout(2500)
  await expect(tab).toHaveAttribute('data-activity', 'idle')
})
