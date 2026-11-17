import { test, expect } from '@playwright/test'
import { waitForConnected } from './helpers.js'
import fs from 'node:fs'
import path from 'node:path'

// The first-run setup screen against the dev server's fake tailnet:
//   web-server  online, Tailscale SSH on (its host key comes from the tailnet)
//   macbook     online, its own sshd (its host key must be confirmed once)
//   nas         offline
//
// The dev server starts with setup finished so every other test sees the
// normal app; these tests reopen it through the API and close it again after.

const SHOTS = process.env.PAPAYA_SHOTS || ''

async function api(page, method, url, body) {
  return page.evaluate(
    async ([method, url, body]) => {
      const res = await fetch(url, {
        method,
        headers: body ? { 'Content-Type': 'application/json' } : undefined,
        body: body ? JSON.stringify(body) : undefined,
      })
      return res.status
    },
    [method, url, body],
  )
}

async function freshSetup(page) {
  await page.goto('/')
  await page.evaluate(async () => {
    const sessions = await fetch('/api/sessions').then((r) => r.json())
    await Promise.all(sessions.map((s) => fetch(`/api/sessions/${s.id}`, { method: 'DELETE' })))
  })
  await api(page, 'PUT', '/api/setup/machines', { machines: [] })
  await api(page, 'POST', '/api/setup/complete', { complete: false })
  await page.reload()
  await expect(page.locator('[data-modal="setup"]')).toBeVisible()
  // First run opens on the welcome; these tests are about what comes after.
  await page.locator('[data-modal="setup"] [data-act="skip"]').click()
}

test('first run explains Papaya, its privacy and Tailscale before anything else', async ({ page }) => {
  await page.goto('/')
  await api(page, 'PUT', '/api/setup/machines', { machines: [] })
  await api(page, 'POST', '/api/setup/complete', { complete: false })
  await page.reload()
  const setup = page.locator('[data-modal="setup"]')
  await expect(setup).toHaveAttribute('data-step', 'welcome')
  await expect(setup).toContainText('Welcome to Papaya')
  await setup.locator('[data-act="continue"]').click()
  await expect(setup).toContainText('Nothing you do here is sent to us')
  await expect(setup).toContainText('Your keys stay home')
  await expect(setup.locator('a[href*="github.com/martoast/papaya"]')).toHaveCount(1)
  await setup.locator('[data-act="continue"]').click()
  await expect(setup).toContainText('Tailscale')
  await expect(setup.locator('a[href*="tailscale.com/download"]')).toHaveCount(1)
  await setup.locator('[data-act="prev"]').click()
  await expect(setup).toContainText('Yours, and only yours')
  await setup.locator('[data-act="continue"]').click()
  await setup.locator('[data-act="continue"]').click()
  await expect(setup).toHaveAttribute('data-step', 'machines')
  await expect(setup).toContainText('Choose your machines')
})

test.afterEach(async ({ page }) => {
  await api(page, 'POST', '/api/setup/complete', { complete: true })
  await page.evaluate(async () => {
    const sessions = await fetch('/api/sessions').then((r) => r.json())
    await Promise.all(sessions.map((s) => fetch(`/api/sessions/${s.id}`, { method: 'DELETE' })))
  })
})

async function shot(page, width, name) {
  if (!SHOTS) return
  fs.mkdirSync(SHOTS, { recursive: true })
  await page.screenshot({ path: path.join(SHOTS, `setup-${name}-${width}.png`), fullPage: true })
}

// joinFrom plays a machine running the one-line join: the script reports the
// account it ran as, and Papaya learns which machine from Tailscale (here the
// dev server's test header).
async function joinFrom(page, machine, { user = 'tester', sshd = true, os = 'Linux' } = {}) {
  return page.evaluate(
    async ([machine, body]) =>
      (await fetch('/api/setup/join', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-Papaya-Test-Caller': machine },
        body: JSON.stringify(body),
      })).status,
    [machine, { user, sshd, os }],
  )
}

async function settleReady(card) {
  await expect(card).toHaveAttribute('data-status', /^(trust|ok)$/, { timeout: 20_000 })
  if ((await card.getAttribute('data-status')) === 'trust') {
    await expect(card.locator('[data-fingerprint]')).toContainText('SHA256:')
    await card.locator('[data-trust]').click()
  }
  await expect(card).toHaveAttribute('data-status', 'ok', { timeout: 20_000 })
}

for (const width of [1440, 390]) {
  test(`a new user goes from sign-in to a terminal by pasting one line (${width}px)`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 900 })
    await freshSetup(page)
    const setup = page.locator('[data-modal="setup"]')

    // 1. Choose machines.
    await expect(setup).toHaveAttribute('data-step', 'machines')
    await expect(setup.locator('[data-machine]')).toHaveCount(3)
    await expect(setup.locator('[data-machine="web-server"]')).toContainText('Tailscale SSH on')
    await expect(setup.locator('[data-machine="nas"]')).toContainText('offline')
    await expect(setup.locator('[data-act="next"]')).toBeDisabled()
    await setup.locator('[data-pick="web-server"]').check()
    await setup.locator('[data-pick="macbook"]').check()
    await expect(setup.locator('[data-act="next"]')).toHaveText(/Next \(2\)/)
    await shot(page, width, '1-machines')
    await setup.locator('[data-act="next"]').click()

    // 2. Connect: one line per machine, no username to type.
    await expect(setup).toHaveAttribute('data-step', 'access')
    const mac = setup.locator('[data-access="macbook"]')
    const srv = setup.locator('[data-access="web-server"]')
    await expect(mac).toContainText('/join | sh')
    await expect(mac).toContainText('Terminal')
    await expect(setup.locator('[data-user]')).toHaveCount(0)
    await expect(setup.locator('[data-act="finish"]')).toBeDisabled()
    await shot(page, width, '2-connect')

    // Pasting it on the Mac: its card connects by itself.
    expect(await joinFrom(page, 'macbook', { os: 'Darwin' })).toBe(200)
    await expect(mac).toHaveAttribute('data-joined', 'true', { timeout: 10_000 })
    await settleReady(mac)
    await expect(mac).toContainText('Connected as tester')
    await expect(setup.locator('[data-act="finish"]')).toBeEnabled()

    // And on the server.
    expect(await joinFrom(page, 'web-server')).toBe(200)
    await settleReady(srv)
    await shot(page, width, '3-ready')
    await setup.locator('[data-act="finish"]').click()

    // 3. Done: one button to the first terminal.
    await expect(setup).toHaveAttribute('data-step', 'done')
    await shot(page, width, '4-done')
    await setup.locator('[data-act="open"]').click()
    await expect(setup).toHaveCount(0)
    await expect(page.locator('[data-tab-id]')).toHaveCount(1)
    await waitForConnected(page)

    await page.reload()
    await expect(page.locator('[data-modal="setup"]')).toHaveCount(0)
  })
}

test('a Mac without Remote Login is told the one thing to do', async ({ page }) => {
  await freshSetup(page)
  const setup = page.locator('[data-modal="setup"]')
  await setup.locator('[data-pick="macbook"]').check()
  await setup.locator('[data-act="next"]').click()
  expect(await joinFrom(page, 'macbook', { sshd: false, os: 'Darwin' })).toBe(200)
  const mac = setup.locator('[data-access="macbook"]')
  await expect(mac).toContainText('Remote Login', { timeout: 10_000 })
  await expect(mac).toContainText('One more step')
  // Turned on: one tap checks again and connects.
  await mac.locator('[data-retest]').click()
  await settleReady(mac)
})

test('running the line on a machine that was not ticked chooses it', async ({ page }) => {
  await freshSetup(page)
  const setup = page.locator('[data-modal="setup"]')
  await setup.locator('[data-pick="macbook"]').check()
  await setup.locator('[data-act="next"]').click()
  expect(await joinFrom(page, 'web-server')).toBe(200)
  await expect(setup.locator('[data-access="web-server"]')).toBeVisible({ timeout: 10_000 })
})

test('the other way in still works: a username and Tailscale SSH', async ({ page }) => {
  await freshSetup(page)
  const setup = page.locator('[data-modal="setup"]')
  await setup.locator('[data-pick="web-server"]').check()
  await setup.locator('[data-act="next"]').click()
  const card = setup.locator('[data-access="web-server"]')
  await card.locator('[data-manual]').click()
  await expect(card).toContainText('Tailscale SSH is on')
  await expect(card.locator('[data-policy]')).toContainText('"accept"')
  await card.locator('[data-user="web-server"]').fill('tester')
  await card.locator('button[data-retest]').click()
  await expect(card).toHaveAttribute('data-status', 'ok', { timeout: 20_000 })
})

test('an offline machine says what to do', async ({ page }) => {
  await freshSetup(page)
  const setup = page.locator('[data-modal="setup"]')
  await setup.locator('[data-pick="nas"]').check()
  await setup.locator('[data-act="next"]').click()
  const card = setup.locator('[data-access="nas"]')
  await expect(card).toContainText('offline')
  await card.locator('[data-manual]').click()
  await card.locator('[data-user="nas"]').fill('tester')
  await card.locator('button[data-retest]').click()
  await expect(card).toHaveAttribute('data-status', 'error', { timeout: 30_000 })
  await expect(card).toContainText(/can't be reached|didn't answer|Nothing answered/)
  await expect(setup.locator('[data-act="finish"]')).toBeDisabled()
})

test('root is welcome: the one-line join works as root', async ({ page }) => {
  await freshSetup(page)
  const setup = page.locator('[data-modal="setup"]')
  await setup.locator('[data-pick="web-server"]').check()
  await setup.locator('[data-act="next"]').click()
  expect(await joinFrom(page, 'web-server', { user: 'root' })).toBe(200)
  await expect(setup.locator('[data-access="web-server"]')).toHaveAttribute('data-joined', 'true', { timeout: 10_000 })
})

test('Machines in the menu reopens setup, and it can be closed', async ({ page }) => {
  await page.goto('/')
  await expect(page.locator('[data-modal="setup"]')).toHaveCount(0)
  await page.click('#more')
  await page.click('#settings')
  const setup = page.locator('[data-modal="setup"]')
  await expect(setup).toHaveAttribute('data-step', 'machines')
  await expect(setup.locator('[data-machine]')).toHaveCount(3)
  await setup.locator('[data-act="close"]').click()
  await expect(setup).toHaveCount(0)
})

test('typing an account survives the background refresh', async ({ page }) => {
  await freshSetup(page)
  const setup = page.locator('[data-modal="setup"]')
  await setup.locator('[data-pick="web-server"]').check()
  await setup.locator('[data-act="next"]').click()
  await setup.locator('[data-access="web-server"] [data-manual]').click()
  const input = setup.locator('[data-user="web-server"]')
  await input.click()
  await page.keyboard.type('tes')
  await page.waitForTimeout(3000) // a refresh cycle
  await page.keyboard.type('ter')
  await expect(input).toHaveValue('tester')
})
