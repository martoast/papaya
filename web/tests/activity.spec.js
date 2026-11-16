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

// papaya-status (Claude Code hooks call it) says exactly what the agent is
// doing. It outranks the guess from the output stream.
test('an agent that reports its status is shown exactly, message and all', async ({ page }) => {
  const first = await newTab(page, { name: 'lead' })
  const agent = await newTab(page, { name: 'helper' })
  await first.click()
  const status = (state, msg = '') =>
    page.evaluate(([state, msg]) => fetch('/api/status', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ tab: 'helper', state, msg }),
    }).then((r) => r.status), [state, msg])

  expect(await status('working')).toBe(204)
  await expect(agent).toHaveAttribute('data-activity', 'working')
  await page.waitForTimeout(3500) // no output at all: an explicit "working" still holds
  await expect(agent).toHaveAttribute('data-activity', 'working')

  expect(await status('needs-you', 'Claude needs your permission to use Bash')).toBe(204)
  await expect(agent).toHaveAttribute('data-attention', 'true')
  await expect(agent).toHaveAttribute('title', /needs you: Claude needs your permission to use Bash/)
  await expect(page).toHaveTitle(/^\(1\) /)

  expect(await status('done')).toBe(204)
  await expect(agent).toHaveAttribute('data-activity', 'done')

  await agent.click()
  await expect(agent).toHaveAttribute('data-attention', 'false')
  await expect(agent).toHaveAttribute('data-activity', 'idle')
})
