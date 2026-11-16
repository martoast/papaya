import { test, expect } from '@playwright/test'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { resetSessions, newTab, termText } from './helpers.js'

// A real 64x64 PNG, the same bytes the integration test checksums.
const pngPath = fileURLToPath(new URL('../../test/fixtures/screenshot.png', import.meta.url))
const pngBase64 = readFileSync(pngPath).toString('base64')

// dropFile dispatches a genuine DragEvent carrying a File, the way a real
// drag from the desktop arrives.
async function dropFile(page, selector, { name, base64, type = 'image/png' }) {
  await page.evaluate(
    ([sel, fileName, b64, mime]) => {
      const bytes = Uint8Array.from(atob(b64), (c) => c.charCodeAt(0))
      const file = new File([bytes], fileName, { type: mime })

      const dt = new DataTransfer()
      dt.items.add(file)

      const target = document.querySelector(sel)
      const fire = (kind) =>
        target.dispatchEvent(
          new DragEvent(kind, { bubbles: true, cancelable: true, dataTransfer: dt }),
        )

      fire('dragenter')
      fire('dragover')
      fire('drop')
    },
    [selector, name, base64, type],
  )
}

test.beforeEach(async ({ page }) => {
  await resetSessions(page)
})

test('dragging an image onto the terminal uploads it and inserts the remote path', async ({
  page,
}) => {
  await newTab(page, { name: 'agent-tab' })

  const uploadResponse = page.waitForResponse(
    (r) => r.url().includes('/upload') && r.request().method() === 'POST',
  )

  await dropFile(page, '#stack', { name: 'screenshot.png', base64: pngBase64 })

  const res = await uploadResponse
  expect(res.status()).toBe(200)

  const body = await res.json()
  expect(body.name).toBe('screenshot.png')
  expect(body.path).toMatch(/screenshot\.png$/)

  // The remote path must land at the shell cursor, so the agent running in that
  // tab receives it as input the moment the user presses enter.
  await expect.poll(() => termText(page), { timeout: 15_000 }).toContain(body.path)

  // No newline is injected: nothing may run until the user chooses to.
  const text = await termText(page)
  expect(text).not.toContain('GOT:')
})

test('a drop that misses the terminal does not navigate away', async ({ page }) => {
  await newTab(page, { name: 'stay-put' })
  const urlBefore = page.url()

  // The tab bar and status bar are outside #stack. Without a window-level
  // handler the browser would open the dropped file and destroy the session view.
  for (const selector of ['#tabbar', 'body']) {
    await dropFile(page, selector, { name: 'stray.png', base64: pngBase64 })
  }

  await page.waitForTimeout(500)

  expect(page.url()).toBe(urlBefore)
  await expect(page.locator('[data-tab-id]')).toHaveCount(1)
  await expect(page.locator('#tabs')).toBeVisible()
})

test('dropping on the tab bar still uploads to the active session', async ({ page }) => {
  await newTab(page, { name: 'tabbar-drop' })

  const uploadResponse = page.waitForResponse(
    (r) => r.url().includes('/upload') && r.request().method() === 'POST',
  )

  await dropFile(page, '#tabbar', { name: 'from-tabbar.png', base64: pngBase64 })

  const res = await uploadResponse
  expect(res.status()).toBe(200)
  expect((await res.json()).name).toBe('from-tabbar.png')
})

test('the upload goes to the active tab, not the first one', async ({ page }) => {
  await newTab(page, { name: 'tab-one' })
  const second = await newTab(page, { name: 'tab-two' })
  await expect(second).toHaveAttribute('data-active', 'true')

  const sessions = await page.evaluate(() =>
    fetch('/api/sessions').then((r) => r.json()),
  )
  const activeId = sessions.find((s) => s.name === 'tab-two').id

  const uploadRequest = page.waitForRequest(
    (r) => r.url().includes('/upload') && r.method() === 'POST',
  )

  await dropFile(page, '#stack', { name: 'target.png', base64: pngBase64 })

  const req = await uploadRequest
  expect(new URL(req.url()).searchParams.get('session')).toBe(activeId)
})

test('a hostile filename is sanitized before it reaches the shell', async ({ page }) => {
  await newTab(page, { name: 'hostile' })

  const uploadResponse = page.waitForResponse(
    (r) => r.url().includes('/upload') && r.request().method() === 'POST',
  )

  await dropFile(page, '#stack', {
    name: '../../../.ssh/authorized_keys',
    base64: pngBase64,
  })

  const res = await uploadResponse
  expect(res.status()).toBe(200)

  const body = await res.json()
  expect(body.path).toContain('uploads')
  expect(body.path).not.toContain('.ssh/')
  expect(body.path.split('/').pop()).not.toContain('..')
})

test('dropping several images uploads all of them', async ({ page }) => {
  await newTab(page, { name: 'multi' })

  const uploads = []
  page.on('response', (r) => {
    if (r.url().includes('/upload') && r.request().method() === 'POST') uploads.push(r)
  })

  await page.evaluate(
    ([b64]) => {
      const bytes = Uint8Array.from(atob(b64), (c) => c.charCodeAt(0))
      const dt = new DataTransfer()
      for (const n of ['one.png', 'two.png', 'three.png']) {
        dt.items.add(new File([bytes], n, { type: 'image/png' }))
      }
      const stack = document.getElementById('stack')
      for (const kind of ['dragenter', 'dragover', 'drop']) {
        stack.dispatchEvent(
          new DragEvent(kind, { bubbles: true, cancelable: true, dataTransfer: dt }),
        )
      }
    },
    [pngBase64],
  )

  await expect.poll(() => uploads.length, { timeout: 20_000 }).toBe(3)
  for (const r of uploads) expect(r.status()).toBe(200)
})

test('the drop overlay appears and clears around a real drop', async ({ page }) => {
  await newTab(page)

  const overlay = page.locator('#drop-overlay')
  await expect(overlay).toHaveAttribute('data-on', 'false')

  const during = await page.evaluate(
    ([b64]) => {
      const bytes = Uint8Array.from(atob(b64), (c) => c.charCodeAt(0))
      const dt = new DataTransfer()
      dt.items.add(new File([bytes], 'x.png', { type: 'image/png' }))

      const stack = document.getElementById('stack')
      stack.dispatchEvent(
        new DragEvent('dragenter', { bubbles: true, cancelable: true, dataTransfer: dt }),
      )
      return document.getElementById('drop-overlay').dataset.on
    },
    [pngBase64],
  )
  expect(during).toBe('true')

  await dropFile(page, '#stack', { name: 'x.png', base64: pngBase64 })
  await expect(overlay).toHaveAttribute('data-on', 'false')
})

test('the attach button uploads a chosen file and inserts the remote path', async ({
  browser,
}) => {
  // A phone: the attach button is touch-only, and this is the flow it exists
  // for — pick a screenshot from the picker, no drag, no clipboard.
  const ctx = await browser.newContext({
    hasTouch: true,
    isMobile: true,
    viewport: { width: 390, height: 780 },
  })
  const page = await ctx.newPage()

  await resetSessions(page)
  await newTab(page, { name: 'attach-tab' })

  await expect(page.locator('#attach')).toBeVisible()

  const uploadResponse = page.waitForResponse(
    (r) => r.url().includes('/upload') && r.request().method() === 'POST',
  )

  // Clicking #attach opens the hidden input; setInputFiles is the picker.
  await page.setInputFiles('#file-input', {
    name: 'screenshot.png',
    mimeType: 'image/png',
    buffer: Buffer.from(pngBase64, 'base64'),
  })

  const res = await uploadResponse
  expect(res.status()).toBe(200)
  const body = await res.json()
  expect(body.name).toBe('screenshot.png')

  // The remote path is injected at the shell cursor. At phone width the line
  // wraps across terminal rows, so compare with whitespace stripped — the path
  // itself has none.
  const noWs = (s) => s.replace(/\s+/g, '')
  await expect
    .poll(() => termText(page).then(noWs), { timeout: 15_000 })
    .toContain(noWs(body.path))

  await ctx.close()
})
