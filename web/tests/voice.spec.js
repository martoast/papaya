import { test, expect } from '@playwright/test'
import { resetSessions, newTab, termText } from './helpers.js'

test.beforeEach(async ({ page }) => {
  await resetSessions(page)
})

test('the mic button appears when the server supports transcription', async ({ page }) => {
  const mic = page.locator('#mic')
  await expect(mic).toBeVisible()
  await expect(mic).toBeEnabled()
  await expect(mic).toHaveAttribute('data-state', 'idle')
})

test('clicking the mic records, then transcribes and inserts the text', async ({ page }) => {
  await newTab(page, { name: 'voice' })

  const mic = page.locator('#mic')
  const request = page.waitForRequest(
    (r) => r.url().includes('/api/transcribe') && r.method() === 'POST',
  )

  await mic.click()
  await expect(mic).toHaveAttribute('data-state', 'recording')
  // The elapsed timer replaces the label while recording.
  await expect(page.locator('#mic-label')).toHaveText(/\d\d:\d\d/)

  // Record long enough to clear the "that was a mis-tap" threshold.
  await page.waitForTimeout(1200)
  await mic.click()

  const req = await request
  expect(new URL(req.url()).searchParams.get('session')).toBeTruthy()

  await expect(mic).toHaveAttribute('data-state', 'idle')

  // The stub transcriber returns a fixed phrase; it must land at the cursor.
  await expect.poll(() => termText(page), { timeout: 15_000 }).toContain('git status --short')
  // And only there: no bubble repeating it.
  await expect(page.locator('#status-text')).toHaveText('')
})

test('push-to-talk records while the hotkey is held', async ({ page }) => {
  await newTab(page, { name: 'ptt' })

  const mic = page.locator('#mic')

  await page.keyboard.down('Control')
  await page.keyboard.down('Space')
  await expect(mic).toHaveAttribute('data-state', 'recording')

  await page.waitForTimeout(1200)

  const request = page.waitForRequest(
    (r) => r.url().includes('/api/transcribe') && r.method() === 'POST',
  )
  await page.keyboard.up('Space')
  await page.keyboard.up('Control')

  await request
  await expect(mic).toHaveAttribute('data-state', 'idle')
  await expect.poll(() => termText(page), { timeout: 15_000 }).toContain('git status --short')
})

test('Ctrl+Space does not reach the shell', async ({ page }) => {
  await newTab(page, { name: 'no-leak' })

  const before = await termText(page)

  await page.keyboard.down('Control')
  await page.keyboard.down('Space')
  await page.waitForTimeout(600)
  await page.keyboard.up('Space')
  await page.keyboard.up('Control')

  await page.waitForTimeout(1500)

  // The transcript is expected; a stray NUL or space submitted to the shell is
  // not. The prompt must not have advanced on its own.
  const after = await termText(page)
  expect(after.length).toBeGreaterThanOrEqual(before.length)
  expect(after).not.toContain('GOT:')
})

test('Escape discards a recording without transcribing', async ({ page }) => {
  await newTab(page, { name: 'discard' })

  let transcribeCalls = 0
  page.on('request', (r) => {
    if (r.url().includes('/api/transcribe')) transcribeCalls += 1
  })

  const mic = page.locator('#mic')
  await mic.click()
  await expect(mic).toHaveAttribute('data-state', 'recording')

  await page.waitForTimeout(800)
  await page.keyboard.press('Escape')

  await expect(mic).toHaveAttribute('data-state', 'idle')
  await page.waitForTimeout(1000)

  expect(transcribeCalls).toBe(0)
  await expect(page.locator('#status-text')).toContainText('discarded')
})

test('recording without a session is refused', async ({ page }) => {
  // No tabs open.
  await expect(page.locator('[data-tab-id]')).toHaveCount(0)

  let transcribeCalls = 0
  page.on('request', (r) => {
    if (r.url().includes('/api/transcribe')) transcribeCalls += 1
  })

  await page.locator('#mic').click()
  await page.waitForTimeout(500)

  expect(transcribeCalls).toBe(0)
  await expect(page.locator('#status-text')).toContainText('open a session')
})

test('a very short tap does not send a request', async ({ page }) => {
  await newTab(page, { name: 'tap' })

  let transcribeCalls = 0
  page.on('request', (r) => {
    if (r.url().includes('/api/transcribe')) transcribeCalls += 1
  })

  const mic = page.locator('#mic')
  await mic.click()
  await page.waitForTimeout(80) // well under the minimum utterance length
  await mic.click()

  await page.waitForTimeout(1200)
  expect(transcribeCalls).toBe(0)
})

// --- recording indicator ---

test('the recording pill shows a live waveform while capturing', async ({ page }) => {
  await newTab(page, { name: 'wave' })

  const pill = page.locator('#recording-pill')
  await expect(pill).toBeHidden()

  await page.locator('#mic').click()
  await expect(pill).toBeVisible()
  await expect(pill).toHaveAttribute('data-state', 'recording')
  await expect(page.locator('#recording-hint')).toContainText('release to send')

  // The canvas must be sized and actually painted. Chrome's fake device emits a
  // tone, so non-blank pixels prove the meter is reading the live stream rather
  // than drawing a static placeholder.
  await page.waitForTimeout(900)

  const painted = await page.evaluate(() => {
    const c = document.getElementById('recording-wave')
    if (!c.width || !c.height) return { sized: false }
    const ctx = c.getContext('2d')
    const { data } = ctx.getImageData(0, 0, c.width, c.height)
    let nonBlank = 0
    for (let i = 3; i < data.length; i += 4) if (data[i] > 0) nonBlank++
    return { sized: true, nonBlank }
  })

  expect(painted.sized).toBe(true)
  expect(painted.nonBlank).toBeGreaterThan(0)

  // The elapsed clock runs.
  await expect(page.locator('#recording-time')).toHaveText(/00:0[0-9]/)

  await page.locator('#mic').click()
  await expect(pill).toBeHidden({ timeout: 15_000 })
})

test('the pill switches to transcribing without flickering off', async ({ page }) => {
  await newTab(page, { name: 'no-flicker' })

  const pill = page.locator('#recording-pill')
  await page.locator('#mic').click()
  await expect(pill).toHaveAttribute('data-state', 'recording')
  await page.waitForTimeout(1000)

  // Watch the state attribute across the stop transition; it must never pass
  // through "idle" on the way to "transcribing".
  const states = await page.evaluate(async () => {
    const el = document.getElementById('recording-pill')
    const seen = []
    const obs = new MutationObserver(() => seen.push(el.dataset.state))
    obs.observe(el, { attributes: true, attributeFilter: ['data-state'] })
    document.getElementById('mic').click()
    await new Promise((r) => setTimeout(r, 1200))
    obs.disconnect()
    return seen
  })

  const idleBeforeTranscribing =
    states.indexOf('idle') !== -1 &&
    states.indexOf('transcribing') !== -1 &&
    states.indexOf('idle') < states.indexOf('transcribing')

  expect(idleBeforeTranscribing).toBe(false)
  expect(states).toContain('transcribing')
})

test('the waveform is torn down when recording ends', async ({ page }) => {
  await newTab(page, { name: 'teardown' })

  await page.locator('#mic').click()
  await page.waitForTimeout(800)
  await page.keyboard.press('Escape')

  await expect(page.locator('#recording-pill')).toBeHidden()

  // The canvas is cleared, so a stale waveform is not left frozen on screen.
  const cleared = await page.evaluate(() => {
    const c = document.getElementById('recording-wave')
    const ctx = c.getContext('2d')
    const { data } = ctx.getImageData(0, 0, c.width, c.height)
    for (let i = 3; i < data.length; i += 4) if (data[i] > 0) return false
    return true
  })
  expect(cleared).toBe(true)
})

// Acquiring the microphone is asynchronous. A second press during that window
// must stop the pending recording, not start a second one — otherwise the mic
// button silently stops working as a toggle whenever the device is slow.
test('pressing the mic twice quickly does not start two recordings', async ({ page }) => {
  await newTab(page, { name: 'double-press' })

  let starts = 0
  await page.exposeFunction('__countStart', () => { starts += 1 })
  await page.evaluate(() => {
    const orig = navigator.mediaDevices.getUserMedia.bind(navigator.mediaDevices)
    navigator.mediaDevices.getUserMedia = async (c) => {
      window.__countStart()
      // Make acquisition slow enough that a second press lands mid-flight.
      await new Promise((r) => setTimeout(r, 900))
      return orig(c)
    }
  })

  const mic = page.locator('#mic')
  await mic.click()
  await page.waitForTimeout(120) // still acquiring
  await mic.click()

  await page.waitForTimeout(2500)

  expect(starts).toBe(1)
  // Cancelled mid-acquire, so it ends idle rather than stuck recording.
  await expect(mic).toHaveAttribute('data-state', 'idle')
  await expect(page.locator('#recording-pill')).toBeHidden()
})

// Voice not set up yet: the mic still shows, and tapping it asks for the key
// right there. A rejected key says so; a good one turns voice on at once.
test('tapping the mic before voice is on asks for a key, then voice works', async ({ page }) => {
  const status = await page.evaluate(async () => (await fetch('/api/voice/key', { method: 'DELETE' })).status)
  expect(status).toBe(200)
  try {
    await page.reload()
    const mic = page.locator('#mic')
    await expect(mic).toBeVisible()
    await expect(mic).toHaveAttribute('data-setup', 'true')
    await mic.click()

    const sheet = page.locator('#voice-setup')
    await expect(sheet).toBeVisible()
    await expect(sheet).toContainText('OpenAI')
    await sheet.locator('input').fill('sk-wrong-key-0123456789abcdef')
    await sheet.locator('button[type=submit]').click()
    await expect(sheet.locator('.vs-err')).toContainText("didn't accept")

    await sheet.locator('input').fill('sk-good-key-0123456789abcdef')
    await sheet.locator('button[type=submit]').click()
    await expect(sheet).toHaveCount(0, { timeout: 5_000 })
    await expect(mic).not.toHaveAttribute('data-setup', 'true')

    // And it records straight away.
    await newTab(page, { name: 'voice-on' })
    await mic.click()
    await expect(mic).toHaveAttribute('data-state', 'recording')
    await page.waitForTimeout(1200)
    await mic.click()
    await expect.poll(() => termText(page), { timeout: 15_000 }).toContain('git status --short')
  } finally {
    // Leave voice on for the other tests.
    await page.evaluate(() =>
      fetch('/api/voice/key', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ key: 'sk-devserver-stub-0123456789' }) }),
    )
  }
})

// Changing your mind: the ✕ on the recording pill throws the recording away
// while you talk, and stops the transcription before anything is typed.
test('the ✕ cancels a recording, and a transcription, without typing anything', async ({ page }) => {
  await newTab(page, { name: 'cancel' })
  const mic = page.locator('#mic')
  let posted = 0
  page.on('request', (r) => { if (r.url().includes('/api/transcribe')) posted++ })

  await mic.click()
  await expect(mic).toHaveAttribute('data-state', 'recording')
  await page.waitForTimeout(1200)
  await page.locator('#recording-cancel').click()
  await expect(mic).toHaveAttribute('data-state', 'idle')
  await expect(page.locator('#status-text')).toContainText('discarded')
  expect(posted).toBe(0)

  // While transcribing: hold the request so there is time to cancel it.
  await page.route('**/api/transcribe**', async (route) => {
    await new Promise((r) => setTimeout(r, 3000))
    await route.continue().catch(() => {})
  })
  await mic.click()
  await expect(mic).toHaveAttribute('data-state', 'recording')
  await page.waitForTimeout(1200)
  await mic.click()
  await expect(mic).toHaveAttribute('data-state', 'transcribing')
  await page.locator('#recording-cancel').click()
  await expect(mic).toHaveAttribute('data-state', 'idle')
  await expect(page.locator('#status-text')).toContainText('nothing was typed')
  await page.waitForTimeout(4000)
  expect(await termText(page)).not.toContain('git status --short')
})
