// Themes, from the ••• menu: tap one and the whole app changes at once. The
// sheet stays open so the next one is a tap away. Each card is a tiny
// terminal in that theme's own colours, so you see what you will get.
//
// Agents can still build and switch themes with papaya-theme; this is the
// way for a person with a phone in one hand.

import { api } from './api.js'

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

export async function showThemePicker() {
  if (document.getElementById('theme-picker')) return
  const root = document.createElement('div')
  root.id = 'theme-picker'
  root.className = 'lg-scrim tp-scrim'
  root.setAttribute('role', 'dialog')
  root.setAttribute('aria-label', 'Themes')
  root.innerHTML = `
    <div class="lg-sheet tp-sheet">
      <div class="tp-head">
        <h2 class="lg-sheet-title">Themes</h2>
        <button class="tp-done lg-pill-btn" data-act="done">Done</button>
      </div>
      <p class="tp-sub">Tap to try one. Ask your agent to make you a new one any time.</p>
      <div class="tp-grid" data-grid><div class="tp-loading">Loading…</div></div>
    </div>`
  const close = () => root.remove()
  root.addEventListener('click', (e) => {
    if (e.target === root || e.target.closest('[data-act="done"]')) close()
  })
  root.addEventListener('keydown', (e) => e.key === 'Escape' && close())
  document.body.appendChild(root)
  root.querySelector('[data-act="done"]').focus()

  const grid = root.querySelector('[data-grid]')
  let list
  try {
    list = await api.themes()
  } catch (err) {
    grid.innerHTML = `<p class="tp-loading">Couldn't load themes: ${esc(err.message)}</p>`
    return
  }
  const details = await Promise.all(
    list.themes.map((t) => api.theme(t.name).then((d) => ({ ...t, colors: d.colors })).catch(() => t)),
  )
  const all = [DEFAULT, ...details]
  let active = list.active || 'default'

  const paint = () => {
    grid.innerHTML = all
      .map(
        (t) => `<button class="tp-card ${t.name === active ? 'is-on' : ''}" data-theme="${esc(t.name)}"
            aria-pressed="${t.name === active}" title="${esc(t.description || '')}">
          ${preview(t.colors)}
          <span class="tp-name">${esc(pretty(t.name))}${t.hasScene ? '<span class="tp-badge">animated</span>' : ''}</span>
          <span class="tp-check" aria-hidden="true">✓</span>
        </button>`,
      )
      .join('')
  }
  paint()

  grid.addEventListener('click', async (e) => {
    const card = e.target.closest('[data-theme]')
    if (!card || card.dataset.theme === active) return
    const prev = active
    active = card.dataset.theme
    paint()
    try {
      await api.useTheme(active)
    } catch {
      active = prev
      paint()
    }
  })
}
