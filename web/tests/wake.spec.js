import { test, expect } from '@playwright/test'
import { resetSessions, newTab, waitForConnected, runCommand, termText } from './helpers.js'

// Reopening the app on a phone. iOS suspends the page; when it comes back the
// sockets are either already closed or, worse, still claiming to be open while
// the far side dropped them long ago. Both must be back in well under a second
// of round trips, not after a reachability check, a session list and (for the
// zombie) however long TCP takes to give up.

test.beforeEach(async ({ page }) => {
  await resetSessions(page)
})

const activeId = (page) =>
  page.locator('[data-tab-id][aria-selected="true"]').getAttribute('data-tab-id')

function countSockets(page) {
  const opened = []
  page.on('websocket', (ws) => opened.push(ws.url()))
  return opened
}

const comeBack = (page) =>
  page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')))

test('a socket that answers the wake ping is left alone', async ({ page }) => {
  await newTab(page, { name: 'calm' })
  await waitForConnected(page)
  const opened = countSockets(page)

  await comeBack(page)
  await page.waitForTimeout(2600) // past the probe deadline

  expect(opened).toEqual([])
  await expect(page.locator('#reconnect')).toBeHidden()
})

test('a socket that looks open but is dead is replaced at once', async ({ page }) => {
  await newTab(page, { name: 'zombie' })
  await waitForConnected(page)

  // What a suspended phone leaves behind: readyState OPEN, nothing gets
  // through. Pings simply vanish, as they would into a dropped connection.
  const id = await activeId(page)
  await page.evaluate((id) => {
    const ws = window.__papaya_tab_for(id).ws
    const send = ws.send.bind(ws)
    ws.send = (data) => {
      if (typeof data === 'string' && data.includes('"ping"')) return
      send(data)
    }
  }, id)

  const opened = countSockets(page)
  const t0 = Date.now()
  await comeBack(page)
  await expect.poll(() => opened.length, { timeout: 4000 }).toBe(1)
  expect(Date.now() - t0).toBeLessThan(3500)

  // And the new socket is a working terminal.
  await waitForConnected(page)
  await runCommand(page, 'echo back-from-the-dead', 'back-from-the-dead')
})

test('closed sockets reconnect without waiting on the server checks', async ({ page }) => {
  await newTab(page, { name: 'one' })
  await newTab(page, { name: 'two' })
  await waitForConnected(page)

  // Sockets the OS closed while the page was frozen: no close event ever ran.
  await page.evaluate(() => {
    for (const el of document.querySelectorAll('[data-tab-id]')) {
      const tab = window.__papaya_tab_for(el.dataset.tabId)
      tab.ws.onclose = null
      tab.ws.close()
    }
  })
  await page.waitForTimeout(300)

  // The reachability check and the session list answer slowly, as they do on
  // a phone over a relayed path. The sockets must not wait for them.
  const slow = (route) => setTimeout(() => route.continue(), 3000)
  await page.route('**/healthz', slow)
  await page.route('**/api/sessions', slow)

  const opened = countSockets(page)
  const t0 = Date.now()
  await comeBack(page)
  await expect.poll(() => opened.length, { timeout: 2500 }).toBe(2)
  expect(Date.now() - t0).toBeLessThan(2500)
})

// Coming back from the background reconnects, and the server replays recent
// output. It used to be written over the screen as it was, so the last lines
// appeared twice; the replay now redraws the screen once.
test('a reconnect does not repeat what is on screen', async ({ page }) => {
  await resetSessions(page)
  await newTab(page, { name: 'once' })
  await page.locator('.term-host[data-active="true"] .xterm').click()
  await page.keyboard.type('echo ONCE-MARK\n')
  const count = async () => (await termText(page)).split('ONCE-MARK').length - 1
  await expect.poll(count, { timeout: 10_000 }).toBeGreaterThanOrEqual(2)
  const before = await count()
  await page.evaluate(() => {
    const h = document.querySelector('.term-host[data-active="true"]')
    window.__papaya_tab_for(h.dataset.terminalId).replaceSocket()
  })
  await page.waitForTimeout(2500)
  await expect.poll(count, { timeout: 10_000 }).toBe(before)
})
