import { test, expect } from '@playwright/test'
import { resetSessions, newTab } from './helpers.js'

test.beforeEach(async ({ page }) => {
  await resetSessions(page)
})

// Writes bytes into the active terminal exactly as the websocket data path
// does, which is the only honest way to test a sequence that arrives from the
// far side.
async function emit(page, seq) {
  await page.evaluate((s) => {
    const host = document.querySelector('.term-host[data-active="true"]')
    host._terminal.write(s)
  }, seq)
}

const osc52 = (text) =>
  `\x1b]52;c;${Buffer.from(text, 'utf8').toString('base64')}\x07`

// The remote host cannot reach the clipboard of the machine the user is sitting
// at. OSC 52 is the only route, and tmux forwards it precisely so that copying
// over SSH works. Dropping it is why a copy inside tmux appeared to succeed and
// then pasted nothing locally.
test('a copy on the remote host lands on the local clipboard', async ({ page }) => {
  await newTab(page, { name: 'clip' })
  await emit(page, osc52('copied on the far side'))

  await expect
    .poll(() => page.evaluate(() => navigator.clipboard.readText()))
    .toBe('copied on the far side')
})

test('multi-byte text survives the round trip', async ({ page }) => {
  await newTab(page, { name: 'clip-utf8' })

  // Base64 carries bytes, not characters. Decoding it as latin-1 — which is
  // what atob alone does — turns every non-ASCII character into mojibake.
  const text = 'árbol — 日本語 — 🎧'
  await emit(page, osc52(text))

  await expect
    .poll(() => page.evaluate(() => navigator.clipboard.readText()))
    .toBe(text)
})

// The dangerous half of OSC 52. A read request asks the terminal to send the
// local clipboard back to the remote host, which would turn any file containing
// the right escape sequence into an exfiltration tool.
test('a clipboard read request is never answered', async ({ page }) => {
  await newTab(page, { name: 'clip-read' })

  await page.evaluate(() => {
    window.__sent = []
    const host = document.querySelector('.term-host[data-active="true"]')
    host._terminal.onData((d) => window.__sent.push(d))
  })

  await page.evaluate(() => navigator.clipboard.writeText('a password, say'))

  // The query, then a sequence xterm is known to answer. Waiting for the second
  // reply proves the first was processed and declined, rather than merely slow.
  await emit(page, '\x1b]52;c;?\x07\x1b[c')

  await expect.poll(() => page.evaluate(() => window.__sent.length)).toBeGreaterThan(0)

  const sent = await page.evaluate(() => window.__sent.join(''))
  expect(sent).toContain('\x1b[?') // the device-attributes reply arrived
  expect(sent).not.toContain('52;')
  expect(sent).not.toContain('password')
  expect(sent).not.toContain(Buffer.from('a password, say').toString('base64'))
})

test('a malformed payload is ignored rather than thrown', async ({ page }) => {
  await newTab(page, { name: 'clip-bad' })
  await page.evaluate(() => navigator.clipboard.writeText('untouched'))

  await emit(page, '\x1b]52;c;!!!not base64!!!\x07\x1b[c')

  // Still responsive, and the clipboard was left alone.
  await expect
    .poll(() => page.evaluate(() => navigator.clipboard.readText()))
    .toBe('untouched')
})

// Safari refuses a clipboard write that has no recent user gesture behind it,
// and OSC 52 arrives from the network, which never does. The write cannot be
// completed at that moment by any means, so the text is held and a button
// offered — pressing it is the gesture. Without this, a copy in tmux copy-mode
// on a Mac reported success on the remote and pasted nothing locally.
//
// The refusal is forced here rather than waited for: Chromium grants the write,
// so the branch this covers is unreachable on the only engine this suite can
// run.
test('a refused clipboard write is held until a gesture can complete it', async ({
  page,
}) => {
  await newTab(page, { name: 'clip-gesture' })
  await page.evaluate(() => navigator.clipboard.writeText('nothing yet'))

  await page.evaluate(() => {
    window.__realWrite = navigator.clipboard.writeText.bind(navigator.clipboard)
    window.__realExec = document.execCommand.bind(document)
    // Both routes writeClipboard can take, refused exactly as WebKit refuses
    // them outside a gesture.
    navigator.clipboard.writeText = () => Promise.reject(new Error('no gesture'))
    document.execCommand = () => false
  })

  await emit(page, osc52('copied inside tmux'))

  const button = page.locator('#clipboard-pending')
  await expect(button).toBeVisible()
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe('nothing yet')

  // The gesture arrives; the browser allows the write again.
  await page.evaluate(() => {
    navigator.clipboard.writeText = window.__realWrite
    document.execCommand = window.__realExec
  })
  await button.click()

  await expect
    .poll(() => page.evaluate(() => navigator.clipboard.readText()))
    .toBe('copied inside tmux')
  await expect(button).toBeHidden()
})

// The other half of copying, and the half a person actually uses: select text,
// press the copy key.
//
// xterm.js does not implement that chord. It listens for the browser's own
// `copy` event and fills in the clipboard data when one arrives. The event only
// fires if the browser believes something is selected — and with the WebGL
// renderer nothing in the document is: the selection is painted onto a canvas,
// and xterm's helper textarea is empty. Chromium dispatches `copy` to the
// focused element anyway, so it works there, and every test in this suite runs
// on Chromium. WebKit does not, which is every Safari tab and every installed
// PWA on a Mac: select, press Cmd+C, and nothing whatsoever happens — no error,
// no copy.
//
// A synthetic keydown is untrusted, so it drives no native copy here either.
// That is the point: what this asserts is that the app copies on its own,
// rather than relying on an event one engine sends and another does not.
async function pressCopyKey(page, init) {
  await page.evaluate((k) => {
    const host = document.querySelector('.term-host[data-active="true"]')
    // xterm binds its key handling to the helper textarea, so a keydown
    // dispatched anywhere else never reaches the handler under test.
    host.querySelector('.xterm-helper-textarea').dispatchEvent(
      new KeyboardEvent('keydown', {
        key: 'c',
        code: 'KeyC',
        bubbles: true,
        cancelable: true,
        ...k,
      }),
    )
  }, init)
}

test('the copy key copies the selection without the browser doing it for us', async ({
  page,
}) => {
  await newTab(page, { name: 'copy-key' })
  await emit(page, 'COPY-THIS-LINE\r\n')
  await page.evaluate(() => navigator.clipboard.writeText('nothing yet'))

  await page.evaluate(() => {
    document.querySelector('.term-host[data-active="true"]')._terminal.selectAll()
  })
  await pressCopyKey(page, { metaKey: true })

  await expect
    .poll(() => page.evaluate(() => navigator.clipboard.readText()))
    .toContain('COPY-THIS-LINE')
})

// Plain Ctrl+C must stay the interrupt. A terminal that copies on it takes away
// the one key that stops a runaway program, so the chord away from the Mac is
// Ctrl+Shift+C — the same one gnome-terminal and VS Code use.
//
// What this can prove is that the copy path declines the key: an untrusted
// keydown reaches our handler but drives none of xterm's own key processing, so
// the interrupt byte itself is not observable here. That half is xterm's, and
// stays xterm's precisely because we decline.
test('plain Ctrl+C is left alone rather than turned into a copy', async ({ page }) => {
  await newTab(page, { name: 'copy-sigint' })
  await emit(page, 'DO-NOT-COPY-ME\r\n')
  await page.evaluate(() => navigator.clipboard.writeText('untouched'))

  await page.evaluate(() => {
    document.querySelector('.term-host[data-active="true"]')._terminal.selectAll()
  })
  await pressCopyKey(page, { ctrlKey: true })

  // Long enough that an async clipboard write would have landed.
  await page.waitForTimeout(500)
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe('untouched')
})

test('Ctrl+Shift+C copies the selection', async ({ page }) => {
  await newTab(page, { name: 'copy-ctrl-shift' })
  await emit(page, 'COPY-ON-LINUX\r\n')
  await page.evaluate(() => navigator.clipboard.writeText('nothing yet'))

  await page.evaluate(() => {
    document.querySelector('.term-host[data-active="true"]')._terminal.selectAll()
  })
  await pressCopyKey(page, { ctrlKey: true, shiftKey: true })

  await expect
    .poll(() => page.evaluate(() => navigator.clipboard.readText()))
    .toContain('COPY-ON-LINUX')
})

// Full-screen programs (Claude Code, vim, htop) turn on mouse reporting, and a
// drag then belongs to the remote program: nothing is selected locally, so
// there is nothing for Cmd+C to copy. Option takes the mouse back for one
// gesture. Without this the only way to copy from those programs is OSC 52,
// which not every program emits.
test('Option+drag selects locally even while the remote holds the mouse', async ({
  page,
}) => {
  await newTab(page, { name: 'mouse' })

  await emit(page, 'SELECTABLE-TEXT-HERE\r\n')
  await page.evaluate(() => {
    // 1006 (SGR) + 1003 (any-event) is what those programs enable.
    const host = document.querySelector('.term-host[data-active="true"]')
    host._terminal.write('\x1b[?1003h\x1b[?1006h')
  })

  const box = await page.locator('.term-host[data-active="true"] .xterm-screen').boundingBox()

  // xterm.js forces a local selection with Option on macOS and Shift elsewhere.
  const force = process.platform === 'darwin' ? 'Alt' : 'Shift'
  await page.keyboard.down(force)
  await page.mouse.move(box.x + 4, box.y + 6)
  await page.mouse.down()
  await page.mouse.move(box.x + 160, box.y + 6, { steps: 12 })
  await page.mouse.up()
  await page.keyboard.up(force)

  const selection = await page.evaluate(() => {
    const host = document.querySelector('.term-host[data-active="true"]')
    return host._terminal.getSelection()
  })
  expect(selection.trim().length).toBeGreaterThan(0)
  expect(selection).toContain('SELECTABLE')
})
