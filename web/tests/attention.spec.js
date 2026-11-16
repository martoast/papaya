import { test, expect } from '@playwright/test'
import { resetSessions, newTab } from './helpers.js'

// bell delivers the server's bell event to a session, which is what the remote
// ringing the terminal bell produces.
async function ringBell(page, index) {
  await page.evaluate((i) => {
    const ids = [...document.querySelectorAll('[data-tab-id]')].map((e) => e.dataset.tabId)
    window.__papaya_test_bell?.(ids[i])
  }, index)
}

test.beforeEach(async ({ page }) => {
  await resetSessions(page)
  await page.evaluate(() => {
    localStorage.removeItem('papaya.layout.v1')
    localStorage.removeItem('papaya.view.v1')
  })
})

// With several agents running, the point is being told which one wants you
// rather than having to watch all of them.
test('a bell badges the tab that rang it', async ({ page }) => {
  await newTab(page, { name: 'ringer' })
  await newTab(page, { name: 'other' }) // 'ringer' is now unfocused

  const ringer = page.locator('[data-tab-id]').first()
  await ringBell(page, 0)

  await expect(ringer).toHaveAttribute('data-attention', 'true', { timeout: 10_000 })
})

test('a focused session does not badge itself', async ({ page }) => {
  await newTab(page, { name: 'only' })

  await ringBell(page, 0)
  await page.waitForTimeout(500)

  // You are already looking at it; a badge would be noise.
  await expect(page.locator('[data-tab-id]').first()).not.toHaveAttribute(
    'data-attention',
    'true',
  )
})

test('looking at a session clears its badge', async ({ page }) => {
  await newTab(page, { name: 'a' })
  await newTab(page, { name: 'b' })

  const first = page.locator('[data-tab-id]').first()
  await ringBell(page, 0)
  await expect(first).toHaveAttribute('data-attention', 'true', { timeout: 10_000 })

  await first.click()
  await expect(first).toHaveAttribute('data-attention', 'false')
})

test('a bell badges the panel in grid view', async ({ page }) => {
  await newTab(page, { name: 'p1' })
  await newTab(page, { name: 'p2' })
  await page.keyboard.press('Control+g')
  await expect(page.locator('#grid')).toBeVisible()
  await page.waitForTimeout(400)

  // Focus the second panel so the first is unfocused and can raise attention.
  await page.locator('[data-panel-id]').nth(1).click()
  await page.waitForTimeout(200)

  const ids = await page.evaluate(() =>
    [...document.querySelectorAll('[data-panel-id]')].map((e) => e.dataset.panelId),
  )
  await page.evaluate((id) => window.__papaya_test_bell?.(id), ids[0])

  const panel = page.locator(`[data-panel-id="${ids[0]}"]`)
  await expect(panel).toHaveAttribute('data-attention', 'true', { timeout: 10_000 })

  await panel.click()
  await expect(panel).toHaveAttribute('data-attention', 'false')
})

// --- touch key pad ---

test('the key pad is hidden with a mouse', async ({ page }) => {
  await newTab(page)
  await expect(page.locator('#keypad')).toBeHidden()
})

test('the key pad sends terminal keys on a touch device', async ({ browser }) => {
  // A phone: coarse pointer, no hover, small screen.
  const ctx = await browser.newContext({
    hasTouch: true,
    isMobile: true,
    viewport: { width: 390, height: 780 },
  })
  const page = await ctx.newPage()

  await resetSessions(page)
  await newTab(page, { name: 'touch' })

  const pad = page.locator('#keypad')
  await expect(pad).toBeVisible()
  // Esc and the four arrows — the keys the soft keyboard does not have.
  await expect(pad.locator('button[data-key="esc"]')).toBeVisible()
  await expect(pad.locator('button[data-key="↑"]')).toBeVisible()

  // Each pad key sends the byte sequence for that key. Capture exactly what is
  // written to the socket rather than inferring it from terminal output.
  const sent = await page.evaluate(() => {
    const out = []
    const h = document.querySelector('.term-host[data-active="true"]')
    const tab = window.__papaya_tab_for(h.dataset.terminalId)
    const real = tab.send.bind(tab)
    tab.send = (d) => {
      out.push(d)
      return real(d)
    }
    window.__sent = out
    return true
  })
  expect(sent).toBe(true)

  // Tap all five keys; each must write its own escape sequence to the socket.
  for (const k of ['esc', '↑', '↓', '←', '→']) {
    await pad.locator(`button[data-key="${k}"]`).dispatchEvent('pointerdown')
  }
  await page.waitForTimeout(100)

  const bytes = await page.evaluate(() => window.__sent)
  expect(bytes).toEqual(['\x1b', '\x1b[A', '\x1b[B', '\x1b[D', '\x1b[C'])

  await ctx.close()
})

test('the key pad can be hidden and stays hidden', async ({ browser }) => {
  const ctx = await browser.newContext({
    hasTouch: true,
    isMobile: true,
    viewport: { width: 390, height: 780 },
  })
  const page = await ctx.newPage()

  await resetSessions(page)
  await newTab(page, { name: 'touch' })

  const pad = page.locator('#keypad')
  const esc = pad.locator('button[data-key="esc"]')
  const toggle = pad.locator('button[data-key="toggle"]')

  await expect(esc).toBeVisible()

  // Put it away — the keys go, the chip stays.
  await toggle.dispatchEvent('pointerdown')
  await expect(esc).toBeHidden()
  await expect(toggle).toBeVisible()

  // The choice survives a reload.
  await page.reload()
  await newTab(page, { name: 'touch2' })
  await expect(page.locator('#keypad button[data-key="esc"]')).toBeHidden()

  // And it comes back.
  await page.locator('#keypad button[data-key="toggle"]').dispatchEvent('pointerdown')
  await expect(page.locator('#keypad button[data-key="esc"]')).toBeVisible()

  await ctx.close()
})

test('panels stack instead of tiling on a phone', async ({ browser }) => {
  const ctx = await browser.newContext({
    hasTouch: true,
    isMobile: true,
    viewport: { width: 390, height: 780 },
  })
  const page = await ctx.newPage()

  await resetSessions(page)
  await page.evaluate(() => localStorage.removeItem('papaya.layout.v1'))
  await newTab(page, { name: 'a' })
  await newTab(page, { name: 'b' })
  await page.keyboard.press('Control+g')
  await page.waitForTimeout(600)

  const boxes = await page.evaluate(() =>
    [...document.querySelectorAll('[data-panel-id]')].map((e) => {
      const r = e.getBoundingClientRect()
      return { x: Math.round(r.x), y: Math.round(r.y), w: Math.round(r.width) }
    }),
  )

  expect(boxes).toHaveLength(2)
  // Stacked, not tiled: same x, increasing y, each usably wide.
  expect(boxes[0].x).toBe(boxes[1].x)
  expect(boxes[1].y).toBeGreaterThan(boxes[0].y)
  expect(boxes[0].w).toBeGreaterThan(300)

  await ctx.close()
})
