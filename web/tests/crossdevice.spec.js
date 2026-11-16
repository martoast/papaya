import { test, expect } from '@playwright/test'
import { resetSessions, newTab, waitForConnected } from './helpers.js'

// The session list at /api/sessions is keyed by tailnet identity, not by
// browser, so every device signed in as the same user sees the same sessions.
// These tests pin that two browsers converge on one set — a session opened on
// one appears on the other, which is the whole point of "the same connections
// everywhere".

test('a second device shows the sessions the first one opened', async ({ browser }) => {
  const laptop = await browser.newContext()
  const laptopPage = await laptop.newPage()
  await resetSessions(laptopPage)
  await newTab(laptopPage, { name: 'shared-work' })

  // A different browser entirely — the phone — opening fresh.
  const phone = await browser.newContext({
    hasTouch: true,
    isMobile: true,
    viewport: { width: 390, height: 780 },
  })
  const phonePage = await phone.newPage()
  await phonePage.goto('/')

  // On boot it adopts the shared list, no action needed.
  await expect(phonePage.locator('[data-tab-id]')).toHaveCount(1)
  await expect(phonePage.locator('[data-tab-id]').first()).toContainText('shared-work')

  await laptop.close()
  await phone.close()
})

test('a session opened on one device appears on the other when it refocuses', async ({
  browser,
}) => {
  const laptop = await browser.newContext()
  const laptopPage = await laptop.newPage()
  await resetSessions(laptopPage)
  await newTab(laptopPage, { name: 'first' })

  const phone = await browser.newContext({
    hasTouch: true,
    isMobile: true,
    viewport: { width: 390, height: 780 },
  })
  const phonePage = await phone.newPage()
  await phonePage.goto('/')
  await expect(phonePage.locator('[data-tab-id]')).toHaveCount(1)

  // The phone is now backgrounded on the laptop's desk. Open more work on the
  // laptop while it sits there.
  await newTab(laptopPage, { name: 'second' })
  await newTab(laptopPage, { name: 'third' })

  // Picking the phone back up: a focus/visibility event, no reload — exactly
  // what iOS delivers when the app comes forward.
  await phonePage.evaluate(() => window.dispatchEvent(new Event('focus')))

  await expect(phonePage.locator('[data-tab-id]')).toHaveCount(3)
  // textContent, not innerText: on a phone the inactive tabs collapse to a dot
  // and their name is display:none, so innerText would return nothing for them.
  const names = await phonePage.locator('[data-tab-id] .tab-name').allTextContents()
  expect(names).toContain('second')
  expect(names).toContain('third')

  await laptop.close()
  await phone.close()
})

test('closing a session on one device removes it from the other', async ({ browser }) => {
  const laptop = await browser.newContext()
  const laptopPage = await laptop.newPage()
  await resetSessions(laptopPage)
  await newTab(laptopPage, { name: 'keep' })
  await newTab(laptopPage, { name: 'discard' })

  const phone = await browser.newContext()
  const phonePage = await phone.newPage()
  await phonePage.goto('/')
  await expect(phonePage.locator('[data-tab-id]')).toHaveCount(2)

  // Close one on the laptop — closing kills the tmux session, so it is gone for
  // everyone, not just hidden here.
  await laptopPage.locator('[data-tab-id]', { hasText: 'discard' })
    .locator('button[aria-label="Close"]')
    .click()
  await expect(laptopPage.locator('[data-tab-id]')).toHaveCount(1)

  // The phone drops it on its next resync rather than reconnecting a dead id.
  await phonePage.evaluate(() => window.dispatchEvent(new Event('focus')))
  await expect(phonePage.locator('[data-tab-id]')).toHaveCount(1)
  await expect(phonePage.locator('[data-tab-id]').first()).toContainText('keep')

  await laptop.close()
  await phone.close()
})
