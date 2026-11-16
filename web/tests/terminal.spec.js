import { test, expect } from '@playwright/test'
import {
  resetSessions,
  newTab,
  termText,
  runCommand,
  allTermText,
  readDims,
  gridSize,
} from './helpers.js'

test.beforeEach(async ({ page }) => {
  await resetSessions(page)
})

// --- the hidden-terminal resize trap (SPEC 8.6) ---

test('hidden tabs are not display:none', async ({ page }) => {
  await newTab(page, { name: 'first' })
  await newTab(page, { name: 'second' })

  const inactive = page.locator('.term-host[data-active="false"]').first()
  await expect(inactive).toHaveCount(1)

  // A display:none terminal measures 0x0, and FitAddon then computes garbage
  // that would destroy the remote PTY geometry. Hiding must preserve layout.
  await expect(inactive).not.toHaveCSS('display', 'none')
})

test('an inactive tab does not corrupt its PTY dimensions', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 })

  await newTab(page, { name: 'tab-a' })
  const before = await readDims(page)
  expect(before.rows).toBeGreaterThan(1)
  expect(before.cols).toBeGreaterThan(1)

  // Tab A becomes inactive while B is created.
  await newTab(page, { name: 'tab-b' })

  // Resize the window while A is hidden: its ResizeObserver fires with the
  // element off-screen, which is exactly when bad dimensions get computed.
  await page.setViewportSize({ width: 900, height: 600 })
  await page.waitForTimeout(500)

  await page.locator('[data-tab-id]').first().click()
  const after = await readDims(page)

  // The remote must report the new window, not 1x1 and not a stale reading.
  expect(after.rows).toBeGreaterThan(1)
  expect(after.cols).toBeGreaterThan(1)
  expect(after.cols).toBeLessThan(before.cols)

  const grid = await gridSize(page)
  expect(after.rows).toBe(grid.rows)
  expect(after.cols).toBe(grid.cols)
})

test('the active terminal keeps sane dimensions across window resizes', async ({ page }) => {
  await newTab(page, { name: 'resizer' })

  for (const size of [
    { width: 1400, height: 900 },
    { width: 800, height: 600 },
    { width: 1100, height: 750 },
  ]) {
    await page.setViewportSize(size)
    await page.waitForTimeout(400)

    const grid = await gridSize(page)
    expect(grid.rows).toBeGreaterThan(1)
    expect(grid.cols).toBeGreaterThan(1)
  }

  // The remote agrees with the browser after all that resizing.
  const dims = await readDims(page)
  const grid = await gridSize(page)
  expect(dims.rows).toBe(grid.rows)
  expect(dims.cols).toBe(grid.cols)

  await runCommand(page, 'echo AFTER-RESIZES', 'AFTER-RESIZES')
})

// --- rename keystroke leakage (SPEC 8.5) ---

test('typing a tab name does not reach the shell', async ({ page }) => {
  await newTab(page, { name: 'original' })
  await runCommand(page, 'echo BASELINE', 'BASELINE')

  await page.locator('[data-tab-id] .tab-name').first().dblclick()
  const input = page.locator('input[data-rename-input="true"]')
  await expect(input).toBeVisible()

  await input.fill('')
  await page.keyboard.type('billing-api')
  await page.keyboard.press('Enter')

  await expect(page.locator('[data-tab-id] .tab-name').first()).toHaveText('billing-api')

  // The critical assertion: without stopPropagation the name is typed into the
  // shell as well as the label.
  const text = await allTermText(page)
  expect(text).not.toContain('billing-api')
})

test('Escape reverts a rename', async ({ page }) => {
  await newTab(page, { name: 'keep-me' })

  await page.locator('[data-tab-id] .tab-name').first().dblclick()
  const input = page.locator('input[data-rename-input="true"]')
  await input.fill('discard-me')
  await page.keyboard.press('Escape')

  await expect(page.locator('[data-tab-id] .tab-name').first()).toHaveText('keep-me')
})

test('blur commits a rename', async ({ page }) => {
  await newTab(page, { name: 'before-blur' })

  await page.locator('[data-tab-id] .tab-name').first().dblclick()
  const input = page.locator('input[data-rename-input="true"]')
  await input.fill('after-blur')
  await page.locator('#stack').click({ position: { x: 40, y: 40 } })

  await expect(page.locator('[data-tab-id] .tab-name').first()).toHaveText('after-blur')
})

test('a rename survives a reload', async ({ page }) => {
  await newTab(page, { name: 'persisted-name' })

  await page.reload()
  await expect(page.locator('[data-tab-id] .tab-name').first()).toHaveText('persisted-name')
})

// --- drop overlay flicker (SPEC 8.7) ---

test('the drop overlay stays visible when dragging over child elements', async ({ page }) => {
  await newTab(page)

  const overlay = page.locator('#drop-overlay')
  await expect(overlay).toHaveAttribute('data-on', 'false')

  // The depth counter must keep the overlay on while the pointer crosses into
  // children, which fire dragleave on the container.
  const states = await page.evaluate(async () => {
    const stack = document.getElementById('stack')
    const overlayEl = document.getElementById('drop-overlay')
    const child = stack.querySelector('.term-host') || stack.firstElementChild

    const dt = new DataTransfer()
    dt.items.add(new File(['x'], 'a.png', { type: 'image/png' }))

    const fire = (el, type) =>
      el.dispatchEvent(
        new DragEvent(type, { bubbles: true, cancelable: true, dataTransfer: dt }),
      )

    const seen = []
    fire(stack, 'dragenter')
    seen.push(overlayEl.dataset.on)

    fire(child, 'dragenter') // entering a child
    seen.push(overlayEl.dataset.on)

    fire(child, 'dragleave') // leaving the child, still inside the stack
    seen.push(overlayEl.dataset.on)

    fire(stack, 'dragleave') // finally leaving the stack
    seen.push(overlayEl.dataset.on)

    return seen
  })

  expect(states.slice(0, 3)).toEqual(['true', 'true', 'true'])
  expect(states[3]).toBe('false')
})

// --- paste upload (SPEC 8.7) ---

test('a pasted image uploads and inserts its path', async ({ page }) => {
  await newTab(page)

  const uploadResponse = page.waitForResponse(
    (r) => r.url().includes('/upload') && r.request().method() === 'POST',
  )

  await page.evaluate(() => {
    const dt = new DataTransfer()
    // A one-pixel PNG.
    const bytes = Uint8Array.from(
      atob(
        'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==',
      ),
      (c) => c.charCodeAt(0),
    )
    dt.items.add(new File([bytes], 'shot.png', { type: 'image/png' }))
    document.dispatchEvent(new ClipboardEvent('paste', { clipboardData: dt, bubbles: true }))
  })

  const res = await uploadResponse
  expect(res.status()).toBe(200)

  const body = await res.json()
  expect(body.path).toContain('uploads')

  // The injected path lands at the cursor in the terminal.
  await expect.poll(() => termText(page), { timeout: 15_000 }).toContain('uploads')
})

// --- tab limit UI (SPEC 8.4) ---

test('the new-tab button is disabled at ten sessions', async ({ page }) => {
  // Create through the API for speed, then verify the UI reflects the cap.
  await page.evaluate(async () => {
    for (let i = 0; i < 10; i++) {
      await fetch('/api/sessions', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ host: 'devbox-1', name: `tab-${i}`, cols: 80, rows: 24 }),
      })
    }
  })
  await page.reload()

  await expect(page.locator('[data-tab-id]')).toHaveCount(10)

  const button = page.locator('#new-tab')
  await expect(button).toBeDisabled()
  await expect(button).toHaveAttribute('title', 'Maximum 10 sessions')

  // An 11th create must be refused server-side, not merely hidden in the UI.
  const status = await page.evaluate(async () => {
    const r = await fetch('/api/sessions', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ host: 'devbox-1', cols: 80, rows: 24 }),
    })
    return r.status
  })
  expect(status).toBe(429)
})

test('tab labels truncate at ten tabs and keep the full name in the title', async ({ page }) => {
  await page.evaluate(async () => {
    for (let i = 0; i < 10; i++) {
      await fetch('/api/sessions', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          host: 'devbox-1',
          name: `a-very-long-session-name-${i}`,
          cols: 80,
          rows: 24,
        }),
      })
    }
  })
  await page.reload()
  await expect(page.locator('[data-tab-id]')).toHaveCount(10)

  const first = page.locator('[data-tab-id]').first()
  await expect(first).toHaveAttribute('title', /a-very-long-session-name-0 — devbox-1/)

  const truncated = await page.locator('[data-tab-id] .tab-name').first().evaluate(
    (el) => el.scrollWidth > el.clientWidth,
  )
  expect(truncated).toBe(true)
})

// --- restore after reload (SPEC 8) ---

test('tabs restore with names and order after a reload', async ({ page }) => {
  await newTab(page, { name: 'alpha' })
  await newTab(page, { name: 'beta' })
  await newTab(page, { name: 'gamma' })

  await page.reload()

  await expect(page.locator('[data-tab-id]')).toHaveCount(3)
  await expect(page.locator('[data-tab-id] .tab-name')).toHaveText(['alpha', 'beta', 'gamma'])

  // The restored session is live, not a stale record.
  await page.locator('[data-tab-id]').first().click()
  await runCommand(page, 'echo AFTER-RELOAD', 'AFTER-RELOAD')
})

// --- keyboard shortcuts (SPEC 8.10) ---

test('keyboard shortcuts switch tabs without reaching the shell', async ({ page }) => {
  await newTab(page, { name: 'one' })
  await newTab(page, { name: 'two' })
  await newTab(page, { name: 'three' })

  const mod = process.platform === 'darwin' ? 'Meta' : 'Control'

  await page.keyboard.press(`${mod}+1`)
  await expect(page.locator('[data-tab-id]').first()).toHaveAttribute('data-active', 'true')

  await page.keyboard.press(`${mod}+2`)
  await expect(page.locator('[data-tab-id]').nth(1)).toHaveAttribute('data-active', 'true')

  await page.keyboard.press(`${mod}+0`)
  await expect(page.locator('[data-tab-id]').nth(2)).toHaveAttribute('data-active', 'true')

  // The digits must not have been typed into any shell.
  const text = await allTermText(page)
  expect(text).not.toMatch(/GOT:[0-9]/)
})

test('F2 begins renaming the active tab', async ({ page }) => {
  await newTab(page, { name: 'rename-with-f2' })

  await page.keyboard.press('F2')
  await expect(page.locator('input[data-rename-input="true"]')).toBeVisible()

  await page.keyboard.press('Escape')
  await expect(page.locator('[data-tab-id] .tab-name').first()).toHaveText('rename-with-f2')
})

// --- accessibility (SPEC 8.12) ---

test('the tab bar exposes tablist semantics', async ({ page }) => {
  await newTab(page, { name: 'a11y' })

  await expect(page.locator('#tabs')).toHaveAttribute('role', 'tablist')
  const tab = page.locator('[data-tab-id]').first()
  await expect(tab).toHaveAttribute('role', 'tab')
  await expect(tab).toHaveAttribute('aria-selected', 'true')
})
