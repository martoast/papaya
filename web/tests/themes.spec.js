import { test, expect } from '@playwright/test'
import { execFileSync } from 'node:child_process'
import { mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { resetSessions, newTab } from './helpers.js'

// Themes are made by agents with bin/papaya-theme. These tests drive that real
// script against the dev server while a browser has Papaya open, and check the
// page follows live, that the scene runs, and that it runs locked away.

const CLI = resolve(process.cwd(), '../bin/papaya-theme')

function theme(args, { ok = true } = {}) {
  try {
    return execFileSync(CLI, args, {
      env: { ...process.env, PAPAYA_URL: test.info().project.use.baseURL },
      encoding: 'utf8',
      timeout: 30_000,
    })
  } catch (e) {
    if (ok) throw new Error(`papaya-theme ${args.join(' ')} failed:\n${e.stdout}${e.stderr}`)
    return `${e.stdout}${e.stderr}`
  }
}

function themeDir(name, json, scene) {
  const dir = join(mkdtempSync(join(tmpdir(), 'papaya-theme-')), name)
  execFileSync('mkdir', ['-p', dir])
  writeFileSync(join(dir, 'theme.json'), JSON.stringify({ name, ...json }))
  if (scene !== undefined) writeFileSync(join(dir, 'scene.js'), scene)
  return dir
}

const accent = (page) =>
  page.evaluate(() => getComputedStyle(document.documentElement).getPropertyValue('--color-accent').trim())

test.beforeEach(async ({ page }) => {
  theme(['use', 'default'])
  await resetSessions(page)
})

test.afterAll(() => {
  theme(['use', 'default'])
})

test('an agent pushes a theme and the open page switches to it, scene and all', async ({ page }) => {
  await newTab(page, { name: 'api' })
  await expect(page.locator('#scene')).toHaveCount(0)

  const dir = themeDir(
    'test-glow',
    { opacity: 0.6, colors: { accent: '#ff00aa', 'term-bg': '#101010' } },
    `papaya.scene({
      draw(s) {
        s.ctx.fillStyle = '#20ff40'
        s.ctx.fillRect(0, 0, s.w, s.h)
      },
      on(s, ev) { s.state.last = ev.type },
    })`,
  )
  const out = theme(['push', dir])

  // The CLI waited for the browser and reports the scene running.
  expect(out).toContain('pushed test-glow')
  expect(out).toMatch(/scene: +running at \d+ fps/)
  expect(out).toContain('viewers: 1')

  // The page took the colours and mounted the scene, with no reload.
  await expect.poll(() => accent(page)).toBe('#ff00aa')
  const frame = page.locator('#scene')
  await expect(frame).toHaveCount(1)
  await expect(frame).toHaveAttribute('sandbox', 'allow-scripts')
  await expect(frame).toHaveAttribute('inert', '')
  await expect(page.locator('html')).toHaveAttribute('data-backdrop', 'on')

  // Typing still goes to the terminal, not the frame.
  await page.locator('.term-host[data-active="true"] .xterm').click()
  const focused = await page.evaluate(() => document.activeElement?.closest('.term-host') !== null)
  expect(focused).toBe(true)

  theme(['use', 'default'])
  await expect(page.locator('#scene')).toHaveCount(0)
  await expect.poll(() => accent(page)).not.toBe('#ff00aa')
})

test('a broken scene is stopped, and the agent reads the error with its own line numbers', async ({ page }) => {
  await page.goto('/')
  const dir = themeDir('test-broken', { colors: {} }, 'papaya.scene({\n  draw(s) {\n    notDefined()\n  },\n})\n')
  const out = theme(['push', dir], { ok: false })
  expect(out).toContain('scene:   ERROR')
  expect(out).toContain('notDefined is not defined')
  expect(out).toContain('scene.js:3')
  // The page does not keep a dead frame over a blank background.
  await expect(page.locator('#scene')).toHaveCount(0)
})

test('a scene cannot reach the network or the page', async ({ page }) => {
  await page.goto('/')
  const dir = themeDir(
    'test-escape',
    { colors: {} },
    `papaya.scene({
      setup(s) {
        let reached = []
        try { parent.document.body; reached.push('parent DOM') } catch (e) {}
        try { localStorage.length; reached.push('storage') } catch (e) {}
        try { document.cookie; reached.push('cookies') } catch (e) {}
        if (reached.length) throw new Error('reached ' + reached.join(', '))
        // The network: this must be refused by the frame's policy.
        fetch('/api/sessions').then(
          () => { throw new Error('fetch went through') },
          () => { throw new Error('network blocked as it should be') },
        )
      },
      draw(s) {},
    })`,
  )
  const out = theme(['push', dir], { ok: false })
  expect(out).toContain('network blocked as it should be')
  expect(out).not.toContain('reached ')
  expect(out).not.toContain('fetch went through')
})

test('scenes hear about tabs, bells and cues', async ({ page }) => {
  await newTab(page, { name: 'web' })
  // The scene reports what it heard by crashing on purpose with it.
  const dir = themeDir(
    'test-events',
    { colors: {} },
    `papaya.scene({
      draw(s) {},
      on(s, ev) {
        if (ev.type === 'cue') throw new Error('heard cue ' + ev.name + ' with tabs ' + s.tabs.map(t => t.name).join(','))
      },
    })`,
  )
  const out = theme(['push', dir])
  expect(out).toMatch(/running/)
  theme(['cue', 'merged'])
  await expect.poll(() => theme(['status', 'test-events'], { ok: false }), { timeout: 10_000 })
    .toContain('heard cue merged with tabs web')
})

test('list, pull and the built-ins', async ({ page }) => {
  await page.goto('/')
  const list = theme(['list'])
  expect(list).toContain('orchard')
  expect(list).toContain('* default')
  const dir = mkdtempSync(join(tmpdir(), 'papaya-pull-'))
  theme(['pull', 'orchard', dir])
  const out = theme(['push', dir], { ok: false })
  expect(out).toContain('built-in theme') // must be saved under another name

  for (const name of ['orchard', 'night-sky', 'synthwave']) {
    theme(['use', name])
    await expect(page.locator('#scene')).toHaveCount(1)
    await expect.poll(() => theme(['status', name], { ok: false }), { timeout: 12_000 }).toMatch(/running at \d+ fps/)
  }
})

// A person switches themes from the app: ••• → Themes → tap. It changes at
// once, the card shows it is on, and the sheet stays for the next try.
test('themes can be switched from the menu', async ({ page }) => {
  await resetSessions(page) // no tabs: the picker sets the theme every tab follows
  try {
    await page.click('#more')
    await page.click('#themes-item')
    const picker = page.locator('#theme-picker')
    await expect(picker).toBeVisible()
    await expect(picker.locator('[data-theme="default"]')).toHaveAttribute('aria-pressed', 'true')
    await expect(picker.locator('[data-theme="synthwave"]')).toBeVisible()
    await picker.locator('[data-theme="synthwave"]').click()
    await expect(picker.locator('[data-theme="synthwave"]')).toHaveAttribute('aria-pressed', 'true')
    await expect
      .poll(() => page.evaluate(async () => (await (await fetch('/api/theme')).json()).name))
      .toBe('synthwave')
    await expect(page.locator('html')).toHaveAttribute('data-backdrop', 'on', { timeout: 10_000 })
    await picker.locator('[data-act="done"]').click()
    await expect(picker).toHaveCount(0)
  } finally {
    await page.evaluate(() =>
      fetch('/api/themes/active', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name: 'default' }) }),
    )
  }
})

// Each tab wears its own theme: picking one changes only the tab in front, the
// page follows whichever tab is in front, and "all tabs" puts one on every tab.
test('each tab can have its own theme', async ({ page }) => {
  await resetSessions(page)
  const reset = () =>
    page.evaluate(() =>
      fetch('/api/themes/active', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name: 'default' }) }),
    )
  try {
    const first = await (await newTab(page, { name: 'neon' })).getAttribute('data-tab-id')
    await page.click('#more')
    await page.click('#themes-item')
    const picker = page.locator('#theme-picker')
    await expect(picker).toContainText('Theme for neon')
    await picker.locator('[data-theme="synthwave"]').click()
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'synthwave')
    await picker.locator('[data-act="done"]').click()
    const sessionTheme = (id) =>
      page.evaluate(async (id) => (await (await fetch('/api/sessions')).json()).find((s) => s.id === id)?.theme || '', id)
    await expect.poll(() => sessionTheme(first)).toBe('synthwave')
    expect(await page.evaluate(async () => (await (await fetch('/api/theme')).json()).name)).toBe('default')

    // A second tab keeps the look every tab follows.
    await newTab(page, { name: 'plain' })
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'default')
    // Back to the first: its theme again.
    await page.locator(`[data-tab-id="${first}"]`).click()
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'synthwave')

    // One theme on every tab.
    await page.click('#more')
    await page.click('#themes-item')
    await picker.locator('[data-theme="night-sky"]').click()
    await picker.locator('[data-act="all"]').click()
    await expect(picker.locator('[data-act="all"]')).toContainText('On all tabs')
    await expect.poll(() => sessionTheme(first)).toBe('')
    expect(await page.evaluate(async () => (await (await fetch('/api/theme')).json()).name)).toBe('night-sky')
    await picker.locator('[data-act="done"]').click()
    await page.locator('[data-tab-id]').last().click()
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'night-sky')
  } finally {
    await reset()
  }
})
