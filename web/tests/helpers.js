import { expect } from '@playwright/test'

// resetSessions clears server-side state so each test starts clean. Sessions
// deliberately outlive the browser, so a reload alone is not a reset.
export async function resetSessions(page) {
  await page.goto('/')
  await page.evaluate(async () => {
    const sessions = await fetch('/api/sessions').then((r) => r.json())
    await Promise.all(
      sessions.map((s) => fetch(`/api/sessions/${s.id}`, { method: 'DELETE' })),
    )
  })
  await page.reload()
  await expect(page.locator('#empty-state')).toBeVisible()
}

// newTab opens a session through the real modal flow.
export async function newTab(page, { name = '', host = 'devbox-1' } = {}) {
  const before = await page.locator('[data-tab-id]').count()

  await page.click('#new-tab')
  await expect(page.locator('[data-modal="new-session"]')).toBeVisible()

  await page.selectOption('#modal-host', host)
  if (name) await page.fill('#modal-name', name)
  await page.click('[data-act="connect"]')

  await expect(page.locator('[data-tab-id]')).toHaveCount(before + 1)
  await waitForConnected(page)
  return page.locator('[data-tab-id]').nth(before)
}

// waitForConnected waits until the active tab's state dot reports connected.
export async function waitForConnected(page) {
  await expect(
    page.locator('[data-tab-id][data-active="true"] span[data-state]'),
  ).toHaveAttribute('data-state', 'connected', { timeout: 20_000 })
}

// readBuffer extracts a terminal's scrollback via the xterm API.
//
// The WebGL and canvas renderers paint to a canvas and leave the .xterm-rows
// DOM empty, so scraping the DOM silently returns "" and every assertion
// against it passes vacuously. The buffer is the only reliable source.
const readBuffer = `(host) => {
  const t = host && host._terminal
  if (!t) return ''
  const buf = t.buffer.active
  const lines = []
  for (let i = 0; i < buf.length; i++) {
    const line = buf.getLine(i)
    if (line) lines.push(line.translateToString(true))
  }
  return lines.join('\\n')
}`

// termText returns the buffer of the active terminal.
export async function termText(page) {
  return page.evaluate(
    ([fn]) => {
      const read = eval(fn)
      return read(document.querySelector('.term-host[data-active="true"]'))
    },
    [readBuffer],
  )
}

// allTermText returns the buffers of every terminal, active or not.
export async function allTermText(page) {
  return page.evaluate(
    ([fn]) => {
      const read = eval(fn)
      return Array.from(document.querySelectorAll('.term-host'))
        .map(read)
        .join('\n---\n')
    },
    [readBuffer],
  )
}

// runCommand types into the focused terminal and waits for expected output.
export async function runCommand(page, cmd, expected) {
  await page.locator('.term-host[data-active="true"] textarea').focus()
  await page.keyboard.type(cmd)
  await page.keyboard.press('Enter')

  if (expected) {
    await expect
      .poll(() => termText(page), { timeout: 15_000 })
      .toContain(expected)
  }
}

// readDims asks the remote for its own geometry, which is the only trustworthy
// way to verify a resize without being able to see the screen.
export async function readDims(page) {
  const dimsRe = /^(\d+) (\d+)$/gm

  // Earlier readings are still in the scrollback, so waiting for "a match" would
  // return a stale value immediately. Wait for a *new* one.
  const before = [...(await termText(page)).matchAll(dimsRe)].length

  await page.locator('.term-host[data-active="true"] textarea').focus()
  await page.keyboard.type('stty size')
  await page.keyboard.press('Enter')

  let last = null
  await expect
    .poll(
      async () => {
        const text = await termText(page)
        // The reply is a line of exactly "<rows> <cols>"; the echoed command
        // sits on its own line and must not be mistaken for it.
        const matches = [...text.matchAll(dimsRe)]
        if (matches.length) last = matches[matches.length - 1]
        return matches.length
      },
      { timeout: 15_000 },
    )
    .toBeGreaterThan(before)

  return { rows: Number(last[1]), cols: Number(last[2]) }
}

// gridSize reports the terminal's own row and column count.
export async function gridSize(page) {
  return page.evaluate(() => {
    const host = document.querySelector('.term-host[data-active="true"]')
    const t = host && host._terminal
    return t ? { rows: t.rows, cols: t.cols } : { rows: 0, cols: 0 }
  })
}
