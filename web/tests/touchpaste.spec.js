import { test, expect } from '@playwright/test'
import { resetSessions, newTab } from './helpers.js'

// The terminal is a canvas, so iOS shows no native paste callout on a long
// press. A touch-and-hold is detected here and pastes the system clipboard
// through xterm. These pin that a hold pastes and a quick tap does not.

// fireHold dispatches a stationary touch held for `holdMs`, then released.
async function fireHold(page, holdMs) {
  await page.evaluate(async (holdMs) => {
    const el = document.querySelector('.term-host[data-active="true"]')
    const r = el.getBoundingClientRect()
    const x = r.x + r.width / 2
    const y = r.y + r.height / 2
    const mk = (type) => {
      const t = new Touch({ identifier: 1, target: el, clientX: x, clientY: y })
      el.dispatchEvent(
        new TouchEvent(type, {
          touches: type === 'touchend' ? [] : [t],
          targetTouches: type === 'touchend' ? [] : [t],
          changedTouches: [t],
          bubbles: true,
          cancelable: true,
        }),
      )
    }
    mk('touchstart')
    await new Promise((res) => setTimeout(res, holdMs))
    mk('touchend')
  }, holdMs)
}

// Watch exactly what the active tab writes to the socket.
async function spySends(page) {
  await page.evaluate(() => {
    const h = document.querySelector('.term-host[data-active="true"]')
    const tab = window.__papaya_tab_for(h.dataset.terminalId)
    const real = tab.send.bind(tab)
    window.__sent = []
    tab.send = (d) => {
      window.__sent.push(d)
      return real(d)
    }
  })
}

test('touch-and-hold pastes the clipboard; a quick tap does not', async ({ browser }) => {
  const ctx = await browser.newContext({
    hasTouch: true,
    isMobile: true,
    viewport: { width: 390, height: 780 },
    permissions: ['clipboard-read', 'clipboard-write'],
  })
  const page = await ctx.newPage()
  await resetSessions(page)
  await newTab(page, { name: 'paste' })

  await page.evaluate(() => navigator.clipboard.writeText('CLIP-PASTE-MARKER'))
  await spySends(page)

  // A quick tap must not paste.
  await fireHold(page, 120)
  await page.waitForTimeout(200)
  expect(await page.evaluate(() => window.__sent.join(''))).not.toContain('CLIP-PASTE-MARKER')

  // A real hold pastes the clipboard text down the socket.
  await fireHold(page, 650)
  await expect
    .poll(() => page.evaluate(() => window.__sent.join('')))
    .toContain('CLIP-PASTE-MARKER')

  await ctx.close()
})

test('the keypad Paste button pastes the clipboard', async ({ browser }) => {
  const ctx = await browser.newContext({
    hasTouch: true,
    isMobile: true,
    viewport: { width: 390, height: 780 },
    permissions: ['clipboard-read', 'clipboard-write'],
  })
  const page = await ctx.newPage()
  await resetSessions(page)
  await newTab(page, { name: 'paste-btn' })

  const btn = page.locator('#keypad button[data-key="paste"]')
  await expect(btn).toBeVisible()

  await page.evaluate(() => navigator.clipboard.writeText('BUTTON-PASTE-MARKER'))
  await spySends(page)

  await btn.click()
  await expect
    .poll(() => page.evaluate(() => window.__sent.join('')))
    .toContain('BUTTON-PASTE-MARKER')

  await ctx.close()
})
