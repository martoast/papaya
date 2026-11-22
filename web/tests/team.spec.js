import { test, expect } from '@playwright/test'

// The Team panel (admin) and what a teammate's app leaves out.

async function call(page, method, url, body) {
  return page.evaluate(
    async ([method, url, body]) => {
      const res = await fetch(url, {
        method,
        headers: body ? { 'Content-Type': 'application/json' } : undefined,
        body: body ? JSON.stringify(body) : undefined,
      })
      return { status: res.status, body: await res.text() }
    },
    [method, url, body],
  )
}

test.describe('team', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/healthz')
    await call(page, 'PUT', '/api/team', { members: [] })
    await call(page, 'PUT', '/api/setup/machines', { machines: [{ name: 'macbook', user: 'tester', method: 'key' }] })
  })
  test.afterEach(async ({ page }) => {
    await page.goto('/healthz')
    await call(page, 'PUT', '/api/team', { members: [] })
    await call(page, 'PUT', '/api/setup/machines', { machines: [] })
  })

  test('the admin adds a teammate and gives them a machine', async ({ page }) => {
    await page.goto('/')
    await page.click('#more')
    await expect(page.locator('#team-item')).toBeVisible()
    await page.click('#team-item')
    const panel = page.locator('[data-modal="team"]')
    await expect(panel).toBeVisible()
    await expect(panel).toContainText('No teammates yet')

    await panel.locator('#team-login').fill('ariosto@example.com')
    await panel.locator('[data-act="add"]').click()
    await panel.locator('input[data-machine="macbook"]').check()
    await panel.locator('[data-act="save"]').click()
    await expect(panel).toBeHidden()

    const team = JSON.parse((await call(page, 'GET', '/api/team')).body)
    expect(team.members).toEqual([{ login: 'ariosto@example.com', machines: ['macbook'] }])
  })

  test('the panel shows why a save was refused', async ({ page }) => {
    await page.goto('/')
    await page.click('#more')
    await page.click('#team-item')
    const panel = page.locator('[data-modal="team"]')
    // An admin's own login cannot become a member entry.
    await panel.locator('#team-login').fill('alice@example.com')
    await panel.locator('[data-act="add"]').click()
    await panel.locator('[data-act="save"]').click()
    await expect(panel.locator('#team-error')).toContainText('already has full access')
  })

  test("a teammate's app has no Machines or Team", async ({ page }) => {
    await page.route('**/api/capabilities', async (route) => {
      const res = await route.fetch()
      const caps = await res.json()
      await route.fulfill({ response: res, json: { ...caps, role: 'member', voiceSettable: false } })
    })
    await page.goto('/')
    await page.click('#more')
    await expect(page.locator('#settings')).toBeHidden()
    await expect(page.locator('#team-item')).toBeHidden()
  })
})
