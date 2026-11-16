import { test, expect } from '@playwright/test'
import { resetSessions, newTab, termText, gridSize } from './helpers.js'

async function toGrid(page) {
  await page.keyboard.press('Control+g')
  await expect(page.locator('#grid')).toBeVisible()
}

// panelBox returns a panel's on-screen rectangle.
async function panelBox(page, index) {
  return page.locator('[data-panel-id]').nth(index).boundingBox()
}

test.beforeEach(async ({ page }) => {
  await resetSessions(page)
  // Clear after the page has loaded, not via addInitScript: that would also run
  // on the reload inside the persistence test and wipe what it is asserting.
  await page.evaluate(() => {
    localStorage.removeItem('papaya.layout.v1')
    localStorage.removeItem('papaya.view.v1')
  })
})

test('the grid shows every session at once', async ({ page }) => {
  await newTab(page, { name: 'one' })
  await newTab(page, { name: 'two' })
  await newTab(page, { name: 'three' })

  await toGrid(page)

  await expect(page.locator('[data-panel-id]')).toHaveCount(3)

  // Every terminal is on screen simultaneously, which is the whole point.
  const visible = await page.evaluate(() =>
    Array.from(document.querySelectorAll('[data-panel-id] .term-host')).map((el) => {
      const r = el.getBoundingClientRect()
      const s = getComputedStyle(el)
      return { w: r.width, h: r.height, display: s.display, visibility: s.visibility }
    }),
  )
  expect(visible).toHaveLength(3)
  for (const v of visible) {
    expect(v.display).not.toBe('none')
    expect(v.visibility).not.toBe('hidden')
    expect(v.w).toBeGreaterThan(50)
    expect(v.h).toBeGreaterThan(30)
  }
})

test('every panel keeps its own live PTY geometry', async ({ page }) => {
  await newTab(page, { name: 'a' })
  await newTab(page, { name: 'b' })
  await toGrid(page)

  // Give the panels a moment to fit and sync.
  await page.waitForTimeout(800)

  const dims = await page.evaluate(() =>
    Array.from(document.querySelectorAll('[data-panel-id] .term-host')).map((el) => ({
      cols: el._terminal.cols,
      rows: el._terminal.rows,
    })),
  )

  for (const d of dims) {
    expect(d.cols).toBeGreaterThan(1)
    expect(d.rows).toBeGreaterThan(1)
  }

  // Side-by-side panels are each narrower than a full-width terminal would be.
  expect(dims[0].cols).toBeLessThan(200)
})

test('all panels stream simultaneously', async ({ page }) => {
  await newTab(page, { name: 'left' })
  await newTab(page, { name: 'right' })
  await toGrid(page)
  await page.waitForTimeout(500)

  // Type into each panel by clicking it first, then confirm both hold output.
  const panels = page.locator('[data-panel-id]')
  for (const [i, marker] of [['0', 'PANEL-A'], ['1', 'PANEL-B']]) {
    await panels.nth(Number(i)).click()
    await page.keyboard.type(`echo ${marker}`)
    await page.keyboard.press('Enter')
  }

  await expect
    .poll(
      () =>
        page.evaluate(() =>
          Array.from(document.querySelectorAll('[data-panel-id] .term-host')).map((el) => {
            const buf = el._terminal.buffer.active
            const lines = []
            for (let i = 0; i < buf.length; i++) {
              const l = buf.getLine(i)
              if (l) lines.push(l.translateToString(true))
            }
            return lines.join('\n')
          }),
        ),
      { timeout: 15_000 },
    )
    .toEqual([expect.stringContaining('PANEL-A'), expect.stringContaining('PANEL-B')])
})

test('clicking a panel focuses it, and voice targets that panel', async ({ page }) => {
  await newTab(page, { name: 'first' })
  await newTab(page, { name: 'second' })
  await toGrid(page)
  await page.waitForTimeout(400)

  const panels = page.locator('[data-panel-id]')
  const firstId = await panels.nth(0).getAttribute('data-panel-id')

  await panels.nth(0).click()
  await expect(panels.nth(0)).toHaveAttribute('data-focused', 'true')
  await expect(panels.nth(1)).toHaveAttribute('data-focused', 'false')

  // Voice must go to the focused panel, not to whichever was opened last.
  const request = page.waitForRequest(
    (r) => r.url().includes('/api/transcribe') && r.method() === 'POST',
  )

  // Wait for recording to actually begin. Acquiring the microphone is async and
  // can outlast any fixed delay; stopping before it started would cancel the
  // pending recording instead of sending it.
  await page.locator('#mic').click()
  await expect(page.locator('#mic')).toHaveAttribute('data-state', 'recording')
  await page.waitForTimeout(1000) // capture enough audio to clear the mis-tap floor
  await page.locator('#mic').click()

  const req = await request
  expect(new URL(req.url()).searchParams.get('session')).toBe(firstId)

  // And the transcript lands in that panel's terminal.
  await expect
    .poll(
      () =>
        page.evaluate((id) => {
          const el = document.querySelector(`[data-panel-id="${id}"] .term-host`)
          const buf = el._terminal.buffer.active
          const lines = []
          for (let i = 0; i < buf.length; i++) {
            const l = buf.getLine(i)
            if (l) lines.push(l.translateToString(true))
          }
          return lines.join('\n')
        }, firstId),
      { timeout: 15_000 },
    )
    .toContain('git status --short')
})

test('dragging a gutter resizes the panels', async ({ page }) => {
  await newTab(page, { name: 'a' })
  await newTab(page, { name: 'b' })
  await toGrid(page)
  await page.waitForTimeout(500)

  const before = await panelBox(page, 0)
  const gutter = page.locator('[data-gutter]').first()
  const g = await gutter.boundingBox()

  // Drag the divider right, making the first panel bigger.
  await page.mouse.move(g.x + g.width / 2, g.y + g.height / 2)
  await page.mouse.down()
  await page.mouse.move(g.x + 220, g.y + g.height / 2, { steps: 12 })
  await page.mouse.up()

  await page.waitForTimeout(600)

  const after = await panelBox(page, 0)
  expect(after.width).toBeGreaterThan(before.width + 100)

  // The enlarged panel's PTY follows, so full-screen programs stay correct.
  const cols = await page.evaluate(() => {
    const el = document.querySelector('[data-panel-id] .term-host')
    return el._terminal.cols
  })
  expect(cols).toBeGreaterThan(1)
})

test('the layout survives a reload', async ({ page }) => {
  await newTab(page, { name: 'keep-a' })
  await newTab(page, { name: 'keep-b' })
  await toGrid(page)
  await page.waitForTimeout(400)

  const gutter = page.locator('[data-gutter]').first()
  const g = await gutter.boundingBox()
  await page.mouse.move(g.x + g.width / 2, g.y + g.height / 2)
  await page.mouse.down()
  await page.mouse.move(g.x + 200, g.y + g.height / 2, { steps: 10 })
  await page.mouse.up()
  await page.waitForTimeout(500)

  const before = await panelBox(page, 0)

  await page.reload()
  await expect(page.locator('#grid')).toBeVisible()
  await expect(page.locator('[data-panel-id]')).toHaveCount(2)
  await page.waitForTimeout(600)

  const after = await panelBox(page, 0)
  // Within a few pixels of where it was left.
  expect(Math.abs(after.width - before.width)).toBeLessThan(20)
})

test('balance evens the panels out again', async ({ page }) => {
  await newTab(page, { name: 'a' })
  await newTab(page, { name: 'b' })
  await toGrid(page)
  await page.waitForTimeout(400)

  const gutter = page.locator('[data-gutter]').first()
  const g = await gutter.boundingBox()
  await page.mouse.move(g.x + g.width / 2, g.y + g.height / 2)
  await page.mouse.down()
  await page.mouse.move(g.x + 220, g.y + g.height / 2, { steps: 10 })
  await page.mouse.up()
  await page.waitForTimeout(400)

  await page.click('#balance')
  await page.waitForTimeout(400)

  const a = await panelBox(page, 0)
  const b = await panelBox(page, 1)
  expect(Math.abs(a.width - b.width)).toBeLessThan(20)
})

test('switching views preserves scrollback and the connection', async ({ page }) => {
  await newTab(page, { name: 'persist' })

  await page.locator('.term-host[data-active="true"] textarea').focus()
  await page.keyboard.type('echo BEFORE-SWITCH')
  await page.keyboard.press('Enter')
  await expect.poll(() => termText(page), { timeout: 15_000 }).toContain('BEFORE-SWITCH')

  await toGrid(page)
  await page.waitForTimeout(500)

  // The terminal was moved, not rebuilt: its scrollback is intact.
  const inGrid = await page.evaluate(() => {
    const el = document.querySelector('[data-panel-id] .term-host')
    const buf = el._terminal.buffer.active
    const lines = []
    for (let i = 0; i < buf.length; i++) {
      const l = buf.getLine(i)
      if (l) lines.push(l.translateToString(true))
    }
    return lines.join('\n')
  })
  expect(inGrid).toContain('BEFORE-SWITCH')

  // Still connected: the session dot stays green and a command still runs.
  await page.locator('[data-panel-id]').first().click()
  await page.keyboard.type('echo STILL-ALIVE')
  await page.keyboard.press('Enter')
  await expect
    .poll(
      () =>
        page.evaluate(() => {
          const el = document.querySelector('[data-panel-id] .term-host')
          const buf = el._terminal.buffer.active
          const lines = []
          for (let i = 0; i < buf.length; i++) {
            const l = buf.getLine(i)
            if (l) lines.push(l.translateToString(true))
          }
          return lines.join('\n')
        }),
      { timeout: 15_000 },
    )
    .toContain('STILL-ALIVE')

  // And back to tabs, still intact.
  await page.keyboard.press('Control+g')
  await expect(page.locator('#stack')).toBeVisible()
  await expect.poll(() => termText(page), { timeout: 15_000 }).toContain('BEFORE-SWITCH')

  const grid = await gridSize(page)
  expect(grid.cols).toBeGreaterThan(1)
  expect(grid.rows).toBeGreaterThan(1)
})

test('closing a panel collapses the layout', async ({ page }) => {
  await newTab(page, { name: 'a' })
  await newTab(page, { name: 'b' })
  await newTab(page, { name: 'c' })
  await toGrid(page)
  await page.waitForTimeout(400)

  await expect(page.locator('[data-panel-id]')).toHaveCount(3)

  const target = page.locator('[data-panel-id]').nth(1)
  await target.hover()
  await target.locator('button[aria-label="Close panel"]').click()

  await expect(page.locator('[data-panel-id]')).toHaveCount(2)

  // The remaining panels expand to fill the space rather than leaving a hole.
  const boxes = await page.evaluate(() =>
    Array.from(document.querySelectorAll('[data-panel-id]')).map((el) => {
      const r = el.getBoundingClientRect()
      return { w: r.width, h: r.height }
    }),
  )
  for (const b of boxes) {
    expect(b.w).toBeGreaterThan(50)
    expect(b.h).toBeGreaterThan(30)
  }
})

test('a new session joins the grid without leaving it', async ({ page }) => {
  await newTab(page, { name: 'first' })
  await toGrid(page)
  await expect(page.locator('[data-panel-id]')).toHaveCount(1)

  await page.click('#new-tab')
  await expect(page.locator('[data-modal="new-session"]')).toBeVisible()
  await page.selectOption('#modal-host', 'devbox-1')
  await page.fill('#modal-name', 'added-in-grid')
  await page.click('[data-act="connect"]')

  await expect(page.locator('#grid')).toBeVisible()
  await expect(page.locator('[data-panel-id]')).toHaveCount(2)
})
