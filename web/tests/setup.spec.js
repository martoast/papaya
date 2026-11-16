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
}

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

for (const width of [1440, 390]) {
  test(`a new user goes from sign-in to a terminal (${width}px)`, async ({ page }) => {
    await page.setViewportSize({ width, height: width === 390 ? 844 : 900 })
    await freshSetup(page)
    const setup = page.locator('[data-modal="setup"]')

    // 1. Machines: the tailnet's devices, with what matters for choosing.
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

    // 2. Let Papaya in: one account and one command per machine.
    await expect(setup).toHaveAttribute('data-step', 'access')
    await expect(setup.locator('[data-access="web-server"]')).toContainText('Tailscale SSH is on')
    await expect(setup.locator('[data-access="macbook"]')).toContainText('authorized_keys')
    await expect(setup.locator('[data-access="macbook"]')).toContainText('Remote Login')
    await expect(setup.locator('[data-policy]')).toContainText('"accept"')
    await expect(setup.locator('[data-act="next"]')).toBeDisabled()
    await setup.locator('[data-user="web-server"]').fill('tester')
    await setup.locator('[data-user="macbook"]').fill('tester')
    await expect(setup.locator('[data-act="next"]')).toHaveText('Next: test')
    await shot(page, width, '2-access')
    await setup.locator('[data-act="next"]').click()

    // 3. Test: the Tailscale SSH machine passes outright; the Mac shows its
    // fingerprint once (unless an earlier run already trusted it).
    await expect(setup).toHaveAttribute('data-step', 'test')
    await expect(setup.locator('[data-test="web-server"]')).toHaveAttribute('data-status', 'ok', { timeout: 20_000 })
    const mac = setup.locator('[data-test="macbook"]')
    await expect(mac).toHaveAttribute('data-status', /^(trust|ok)$/, { timeout: 20_000 })
    if ((await mac.getAttribute('data-status')) === 'trust') {
      await expect(mac.locator('[data-fingerprint]')).toContainText('SHA256:')
      await shot(page, width, '3-test-trust')
      await mac.locator('[data-trust]').click()
    }
    await expect(mac).toHaveAttribute('data-status', 'ok', { timeout: 20_000 })
    await shot(page, width, '3-test-ready')
    await setup.locator('[data-act="finish"]').click()

    // 4. Done: one button to the first terminal.
    await expect(setup).toHaveAttribute('data-step', 'done')
    await shot(page, width, '4-done')
    await setup.locator('[data-act="open"]').click()
    await expect(setup).toHaveCount(0)
    await expect(page.locator('[data-tab-id]')).toHaveCount(1)
    await waitForConnected(page)

    // Finished for good: a reload goes straight to the app.
    await page.reload()
    await expect(page.locator('[data-modal="setup"]')).toHaveCount(0)
  })
}

test('an offline machine says what to do', async ({ page }) => {
  await freshSetup(page)
  const setup = page.locator('[data-modal="setup"]')
  await setup.locator('[data-pick="nas"]').check()
  await setup.locator('[data-act="next"]').click()
  await setup.locator('[data-user="nas"]').fill('tester')
  await setup.locator('[data-act="next"]').click()
  const row = setup.locator('[data-test="nas"]')
  await expect(row).toHaveAttribute('data-status', 'error', { timeout: 30_000 })
  await expect(row).toContainText(/can't be reached|didn't answer|Nothing answered/)
  await expect(setup.locator('[data-act="finish"]')).toBeDisabled()
  await shot(page, 1280, 'error-offline')
})

test('root is refused with a reason', async ({ page }) => {
  await freshSetup(page)
  const setup = page.locator('[data-modal="setup"]')
  await setup.locator('[data-pick="web-server"]').check()
  await setup.locator('[data-act="next"]').click()
  await setup.locator('[data-user="web-server"]').fill('root')
  await setup.locator('[data-act="next"]').click()
  await expect(setup.locator('[data-error]')).toContainText('everyday account')
  await expect(setup).toHaveAttribute('data-step', 'access')
})

test('the settings button reopens setup, and it can be closed', async ({ page }) => {
  await page.goto('/')
  await expect(page.locator('[data-modal="setup"]')).toHaveCount(0)
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
  const input = setup.locator('[data-user="web-server"]')
  await input.click()
  await page.keyboard.type('tes')
  await page.waitForTimeout(4500) // one refresh cycle
  await page.keyboard.type('ter')
  await expect(input).toHaveValue('tester')
})
