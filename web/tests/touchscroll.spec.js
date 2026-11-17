import { test, expect } from '@playwright/test'
import { resetSessions, newTab } from './helpers.js'

// xterm has no touch support, so before this a finger swipe over the terminal
// did nothing and the scrollback — and a full-screen app's own scroll, like
// Claude Code's conversation — was unreachable on a phone. A vertical drag is
// turned into wheel events on xterm's element; here we prove it moves the
// scrollback on the normal buffer. (The alternate-buffer path forwards the same
// wheel to the far side, which needs a real full-screen app to exercise.)

// fireDrag dispatches a genuine touch drag over the active terminal.
async function fireDrag(page, fromY, toY, steps = 8) {
  await page.evaluate(
    ([fromY, toY, steps]) => {
      const el = document.querySelector('.term-host[data-active="true"]')
      const mk = (type, y) => {
        const t = new Touch({ identifier: 1, target: el, clientX: 120, clientY: y })
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
      mk('touchstart', fromY)
      for (let i = 1; i <= steps; i++) {
        mk('touchmove', fromY + ((toY - fromY) * i) / steps)
      }
      mk('touchend', toY)
    },
    [fromY, toY, steps],
  )
}

const viewportY = (page) =>
  page.evaluate(() => {
    const h = document.querySelector('.term-host[data-active="true"]')
    return h._terminal.buffer.active.viewportY
  })

test.beforeEach(async ({ page }) => {
  await resetSessions(page)
})

test('a finger swipe scrolls the terminal scrollback', async ({ browser }) => {
  const ctx = await browser.newContext({
    hasTouch: true,
    isMobile: true,
    viewport: { width: 390, height: 780 },
  })
  const page = await ctx.newPage()
  await resetSessions(page)
  await newTab(page, { name: 'scrolltest' })

  // Wait for the shell to be ready before typing: a command sent in the first
  // milliseconds after a session opens is flushed when readline starts.
  const textarea = page.locator('.term-host[data-active="true"] textarea')
  await textarea.focus()
  await page.keyboard.type('echo READY-MARK')
  await page.keyboard.press('Enter')
  await expect
    .poll(
      () =>
        page.evaluate(() => {
          const h = document.querySelector('.term-host[data-active="true"]')
          const b = h._terminal.buffer.active
          let out = ''
          for (let i = 0; i < b.length; i++) out += (b.getLine(i)?.translateToString(true) || '') + '\n'
          return out
        }),
      { timeout: 15_000 },
    )
    .toContain('READY-MARK')

  // Fill well past a screen so there is scrollback to move through. "storm N"
  // is the harness shell's way to emit N numbered lines.
  await textarea.focus()
  await page.keyboard.type('storm 400')
  await page.keyboard.press('Enter')

  // Wait until the buffer has scrolled to the bottom of that output.
  await expect.poll(() => viewportY(page), { timeout: 15_000 }).toBeGreaterThan(50)
  const atBottom = await viewportY(page)

  // Drag DOWN (finger moves down the screen) → reveal older lines → scroll up.
  await fireDrag(page, 200, 600)
  await expect.poll(() => viewportY(page)).toBeLessThan(atBottom)
  const afterUp = await viewportY(page)

  // Drag UP → back toward the newest output.
  await fireDrag(page, 600, 200)
  await expect.poll(() => viewportY(page)).toBeGreaterThan(afterUp)

  await ctx.close()
})

// On a phone, switching tabs is looking, not typing: no terminal takes focus,
// so the keyboard stays down and nothing resizes. Tapping the terminal is what
// brings the keyboard up.
test('switching tabs on a phone does not raise the keyboard', async ({ browser }) => {
  const ctx = await browser.newContext({ hasTouch: true, isMobile: true, viewport: { width: 390, height: 780 } })
  const page = await ctx.newPage()
  await resetSessions(page)
  const first = await newTab(page, { name: 'one' })
  await newTab(page, { name: 'two' })
  await page.evaluate(() => document.activeElement?.blur())
  const typingTarget = () => page.evaluate(() => document.activeElement?.classList.contains('xterm-helper-textarea') || false)
  await first.tap()
  await expect(first).toHaveAttribute('data-active', 'true')
  await page.waitForTimeout(300)
  expect(await typingTarget()).toBe(false)
  await page.locator('[data-tab-id]').last().tap()
  await page.waitForTimeout(300)
  expect(await typingTarget()).toBe(false)
  await ctx.close()
})

// Two devices on one tab: whichever you are looking at gets a shell drawn for
// its screen. The phone switching to the tab takes the size; the desktop takes
// it back as soon as the mouse moves there.
test('the device you are looking at gets the shell drawn for its screen', async ({ page, browser }) => {
  await page.setViewportSize({ width: 1280, height: 800 })
  await resetSessions(page)
  const shared = await (await newTab(page, { name: 'shared' })).getAttribute('data-tab-id')
  await newTab(page, { name: 'other' })
  await page.locator(`[data-tab-id="${shared}"]`).click()
  const size = () => page.evaluate(async (id) => {
    const s = (await (await fetch('/api/sessions')).json()).find((x) => x.id === id)
    return s ? s.cols : 0
  }, shared)
  const desktopCols = await page.evaluate((id) => document.querySelector(`.term-host[data-terminal-id="${id}"]`)._terminal.cols, shared)
  await page.mouse.move(400, 400)
  await expect.poll(size, { timeout: 5000 }).toBe(desktopCols)

  const ctx = await browser.newContext({ hasTouch: true, isMobile: true, viewport: { width: 390, height: 780 } })
  const phone = await ctx.newPage()
  try {
    await phone.goto('/')
    await expect(phone.locator(`[data-tab-id="${shared}"]`)).toBeVisible({ timeout: 10_000 })
    await phone.locator('[data-tab-id]').last().tap()
    await phone.locator(`[data-tab-id="${shared}"]`).tap()
    const phoneCols = await phone.evaluate((id) => document.querySelector(`.term-host[data-terminal-id="${id}"]`)._terminal.cols, shared)
    expect(phoneCols).toBeLessThan(desktopCols)
    await expect.poll(size, { timeout: 5000 }).toBe(phoneCols)

    await page.waitForTimeout(1100) // claims are at most one a second
    await page.mouse.move(500, 420)
    await page.mouse.move(520, 440)
    await expect.poll(size, { timeout: 5000 }).toBe(desktopCols)
  } finally {
    await ctx.close()
  }
})
