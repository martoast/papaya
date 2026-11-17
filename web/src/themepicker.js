// Themes, from the ••• menu: tap one and the whole app changes at once. The
// sheet stays open so the next one is a tap away. Each card is a tiny
// terminal in that theme's own colours, so you see what you will get.
//
// Agents can still build and switch themes with papaya-theme; this is the
// way for a person with a phone in one hand.

import { api } from './api.js'
import { tabThemeName, setTabTheme, tabIds } from './themes.js'

const DEFAULT = {
  name: 'default',
  description: "Papaya's own look: glass over a warm glow.",
  colors: {
    'term-bg': '#0b0c10', 'term-fg': '#ebebf0', accent: '#ff9447',
    'ansi-green': '#3fb950', 'ansi-blue': '#58a6ff', 'ansi-magenta': '#bc8cff', 'ansi-yellow': '#d29922',
  },
}

const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c])
const pretty = (n) => n.replace(/[-_]+/g, ' ').replace(/\b\w/g, (c) => c.toUpperCase())

function preview(c = {}) {
  const bg = c['term-bg'] || DEFAULT.colors['term-bg']
  const fg = c['term-fg'] || DEFAULT.colors['term-fg']
  const acc = c.accent || c['term-cursor'] || DEFAULT.colors.accent
  const g = c['ansi-green'] || DEFAULT.colors['ansi-green']
  const b = c['ansi-blue'] || DEFAULT.colors['ansi-blue']
  const m = c['ansi-magenta'] || DEFAULT.colors['ansi-magenta']
  const y = c['ansi-yellow'] || DEFAULT.colors['ansi-yellow']
  return `<div class="tp-term" style="background:${esc(bg)};color:${esc(fg)}">
      <div><span style="color:${esc(g)}">❯</span> <span style="color:${esc(b)}">claude</span> fix the tests</div>
      <div style="opacity:.75">✓ <span style="color:${esc(y)}">12 passed</span> · <span style="color:${esc(m)}">2 files</span></div>
      <div><span style="color:${esc(g)}">❯</span> <span class="tp-cursor" style="background:${esc(acc)}"></span></div>
    </div>`
}

// tab: { id, name } of the tab in front; its theme is what the picker sets.
// Without one (no tabs open) it sets the theme every tab follows.
export async function showThemePicker({ tab = null } = {}) {
  if (document.getElementById('theme-picker')) return
  const root = document.createElement('div')
  root.id = 'theme-picker'
  root.className = 'lg-scrim tp-scrim'
  root.setAttribute('role', 'dialog')
  root.setAttribute('aria-label', 'Themes')
  root.innerHTML = `
    <div class="lg-sheet tp-sheet">
      <div class="tp-head">
        <h2 class="lg-sheet-title">${tab ? `Theme for <span class="tp-tab">${esc(tab.name)}</span>` : 'Themes'}</h2>
        <button class="tp-done lg-pill-btn" data-act="done">Done</button>
      </div>
      <p class="tp-sub">${tab ? 'Each tab can have its own. Tap to try one.' : 'Tap to try one.'} Ask your agent to make you a new one any time.</p>
      ${tab ? '<button class="tp-all" data-act="all">Use this theme on all tabs</button>' : ''}
      <div class="tp-grid" data-grid><div class="tp-loading">Loading…</div></div>
      <div class="tp-section">
        <h3>From Omarchy</h3>
        <p>Beautiful themes from <a href="https://github.com/omacom/omarchy" target="_blank" rel="noopener">Omarchy</a>, with their wallpapers. Tap to bring one in.</p>
      </div>
      <div class="tp-grid" data-omarchy><div class="tp-loading">Loading Omarchy's themes…</div></div>
    </div>`
  const close = () => root.remove()
  root.addEventListener('click', (e) => {
    if (e.target === root || e.target.closest('[data-act="done"]')) close()
  })
  root.addEventListener('keydown', (e) => e.key === 'Escape' && close())
  document.body.appendChild(root)
  root.querySelector('[data-act="done"]').focus()

  const grid = root.querySelector('[data-grid]')
  const omGrid = root.querySelector('[data-omarchy]')
  let all = [DEFAULT]
  let omarchy = null
  let active = 'default'
  let busy = ''

  const card = (t, extra = '') => `<button class="tp-card ${t.name === active ? 'is-on' : ''} ${busy === t.name ? 'is-busy' : ''}"
      data-theme="${esc(t.name)}" ${extra} aria-pressed="${t.name === active}" title="${esc(t.description || '')}">
      ${preview(t.colors)}
      <span class="tp-name">${esc(t.title || pretty(t.name))}${t.hasScene ? '<span class="tp-badge">animated</span>' : ''}${t.hasImage ? '<span class="tp-badge">wallpaper</span>' : ''}</span>
      <span class="tp-check" aria-hidden="true">✓</span>
      <span class="tp-spin" aria-hidden="true"></span>
    </button>`

  const paint = () => {
    grid.innerHTML = all.map((t) => card(t)).join('')
    if (omarchy) {
      const have = new Set(all.map((t) => t.name))
      const rest = omarchy.filter((t) => !have.has(t.name))
      omGrid.innerHTML = rest.length
        ? rest.map((t) => card(t, 'data-omarchy-theme="1"')).join('')
        : '<p class="tp-loading">All of them are in your themes above.</p>'
    }
  }

  async function loadMine() {
    const list = await api.themes()
    const details = await Promise.all(
      list.themes.map((t) => api.theme(t.name).then((d) => ({ ...t, colors: d.colors })).catch(() => t)),
    )
    all = [DEFAULT, ...details]
    active = tab ? tabThemeName(tab.id) : list.active || 'default'
    paint()
  }

  try {
    await loadMine()
  } catch (err) {
    grid.innerHTML = `<p class="tp-loading">Couldn't load themes: ${esc(err.message)}</p>`
    return
  }
  api
    .omarchyThemes()
    .then((d) => {
      omarchy = d.themes || []
      paint()
    })
    .catch(() => {
      omGrid.innerHTML = `<p class="tp-loading">Couldn't reach Omarchy's themes right now.</p>`
    })

  // pick puts a theme on the tab (or, with no tab, on everything).
  const pick = (name) => (tab ? setTabTheme(tab.id, name) : api.useTheme(name))

  root.addEventListener('click', async (e) => {
    if (e.target.closest('[data-act="all"]')) {
      const btn = e.target.closest('[data-act="all"]')
      btn.disabled = true
      try {
        await api.useTheme(active)
        await Promise.all(tabIds().map((id) => setTabTheme(id, '')))
        btn.textContent = '✓ On all tabs'
      } catch {
        btn.disabled = false
      }
      return
    }
    const el = e.target.closest('[data-theme]')
    if (!el || busy || el.dataset.theme === active) return
    const name = el.dataset.theme
    const prev = active
    if (el.dataset.omarchyTheme) {
      busy = name
      paint()
      try {
        await api.useOmarchy(name, !tab)
        if (tab) await setTabTheme(tab.id, name)
        busy = ''
        await loadMine()
      } catch {
        busy = ''
        paint()
      }
      return
    }
    active = name
    paint()
    try {
      await pick(name)
    } catch {
      active = prev
      paint()
    }
  })
}
