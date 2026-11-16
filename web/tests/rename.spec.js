import { test, expect } from '@playwright/test'
import { resetSessions, newTab } from './helpers.js'

// Renaming existed, by F2 and by double-click, and neither was discoverable:
// nothing on screen mentioned F2, a Mac keyboard puts it behind Fn, and a
// double-click is not a thing anyone goes looking for. On touch there was no
// way to rename at all, and in the grid view there was none on any device.

test.beforeEach(async ({ page }) => {
  await resetSessions(page)
  await page.evaluate(() => {
    localStorage.removeItem('papaya.layout.v1')
    localStorage.removeItem('papaya.view.v1')
  })
})

const renameInput = (page) => page.locator('[data-rename-input="true"]')

test('the tab says out loud that it can be renamed', async ({ page }) => {
  await newTab(page, { name: 'ledger' })
  const title = await page.locator('[data-tab-id] .tab-name').first().evaluate(
    (el) => el.closest('[data-tab-id]').title,
  )
  expect(title).toContain('rename')
})

test('holding a tab renames it, the way touch has to', async ({ page }) => {
  await newTab(page, { name: 'ledger' })
  const label = page.locator('[data-tab-id] .tab-name').first()

  const box = await label.boundingBox()
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2)
  await page.mouse.down()
  await page.waitForTimeout(700)
  await page.mouse.up()

  await expect(renameInput(page)).toBeVisible()

  await renameInput(page).fill('books')
  await renameInput(page).press('Enter')
  await expect(page.locator('[data-tab-id]').first()).toContainText('books')
})

test('the hold that renames does not also switch tabs by accident', async ({ page }) => {
  await newTab(page, { name: 'first' })
  await newTab(page, { name: 'second' })

  // Hold the inactive tab: it should be renamed, not merely activated and left
  // alone, and the click that follows the hold must not close the input.
  const first = page.locator('[data-tab-id]').first()
  const box = await first.locator('.tab-name').boundingBox()
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2)
  await page.mouse.down()
  await page.waitForTimeout(700)
  await page.mouse.up()
  await page.waitForTimeout(200)

  await expect(renameInput(page)).toBeVisible()
})

test('a panel in the grid can be renamed too', async ({ page }) => {
  await newTab(page, { name: 'alpha' })
  await newTab(page, { name: 'beta' })

  await page.keyboard.press('Control+g')
  await expect(page.locator('#grid')).toBeVisible()

  await page.locator('.panel-name', { hasText: 'alpha' }).dblclick()
  await expect(renameInput(page)).toBeVisible()

  await renameInput(page).fill('renamed-in-grid')
  await renameInput(page).press('Enter')

  // The panel header follows the tab, so the new name shows where it was typed.
  await expect(page.locator('.panel-name', { hasText: 'renamed-in-grid' })).toBeVisible()
})
