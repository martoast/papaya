// Themes, from the ••• menu: tap one and the tab in front changes at once
// (the new look sweeps up from the bottom of the screen). The sheet stays open
// so the next one is a tap away, and only covers the bottom of the screen, so
// the change is visible above it.
//
// Every card is a small phone screen running the theme's own scene, live
// (/theme/scene/{name}?preview=1), with a little glass terminal in the theme's
// colours on top. Cards sit in rows that
// scroll sideways, a row per kind, so nothing can overflow the screen.
//
// Agents still build themes with papaya-theme; a new one appears here.

import { api } from './api.js'
import { tabThemeName, setTabTheme, tabIds, prefetch } from './themes.js'

const DEFAULT = {
  name: 'default',
  title: 'Papaya',
  description: "Papaya's own look: glass over a warm glow.",
  colors: { 'term-bg': '#0b0c10', 'term-fg': '#ebebf0', accent: '#ff9447', 'ansi-green': '#3fb950', 'ansi-blue': '#58a6ff', 'ansi-yellow': '#d29922' },
}

const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c])
const pretty = (n) => n.replace(/[-_]+/g, ' ').replace(/\b\w/g, (c) => c.toUpperCase())

function card(t, on) {
  const c = t.colors || {}
  const bg = c['term-bg'] || '#0b0c10'
  const fg = c['term-fg'] || '#ebebf0'
  const acc = c.accent || '#ff9447'
  const visual = t.hasScene
    ? `<iframe class="tp-poster" data-live="/theme/scene/${encodeURIComponent(t.name)}?v=${encodeURIComponent(t.version || '')}&preview=1"
         sandbox="allow-scripts" tabindex="-1" aria-hidden="true" inert referrerpolicy="no-referrer"></iframe>`
    : `<span class="tp-plain" style="background:radial-gradient(110% 55% at 50% -12%, ${esc(acc)}88, ${esc(acc)}22 45%, transparent 72%), radial-gradient(90% 45% at 50% 115%, #7a5cff44, transparent 70%), ${esc(bg)}"></span>`
  return `<button class="tp-card ${on ? 'is-on' : ''}" data-theme="${esc(t.name)}" aria-pressed="${on}" title="${esc(t.description || '')}">
      <span class="tp-screen">
        ${visual}
        <span class="tp-mini" style="--mini-bg:${esc(bg)};color:${esc(fg)}">
          <span><b style="color:${esc(c['ansi-green'] || '#7ee2a8')}">❯</b> claude</span>
          <span style="color:${esc(c['ansi-yellow'] || '#ffd37a')}">✓ done</span>
          <span><b style="color:${esc(c['ansi-green'] || '#7ee2a8')}">❯</b> <i style="background:${esc(acc)}"></i></span>
        </span>
        <span class="tp-check" aria-hidden="true">✓</span>
      </span>
      <span class="tp-name">${esc(t.title || pretty(t.name))}</span>
    </button>`
}

// tab: { id, name } of the tab in front; its theme is what the picker sets.
// Without one (no tabs open) it sets the theme every tab follows.
export async function showThemePicker({ tab = null } = {}) {
  if (document.getElementById('theme-picker')) return
  const root = document.createElement('div')
  root.id = 'theme-picker'
  root.className = 'tp-scrim'
  root.setAttribute('role', 'dialog')
  root.setAttribute('aria-label', 'Themes')
  root.innerHTML = `
    <div class="lg-sheet tp-sheet">
      <span class="tp-grab" aria-hidden="true"></span>
      <div class="tp-head">
        <div>
          <h2 class="lg-sheet-title">${tab ? `Theme for <span class="tp-tab">${esc(tab.name)}</span>` : 'Themes'}</h2>
          <p class="tp-sub">${tab ? 'Each tab can have its own.' : 'Pick a look.'} Ask your agent to make you a new one any time.</p>
        </div>
        <button class="lg-pill-btn" data-act="done">Done</button>
      </div>
      ${tab ? '<button class="tp-all" data-act="all">Use this theme on all tabs</button>' : ''}
      <div class="tp-body" data-grid><div class="tp-loading">Loading…</div></div>
    </div>`
  // Live previews, only while a card is on screen: each scene starts as its
  // card scrolls into view and is unloaded once it leaves, so a few run at a
  // time (light: standard resolution, 30 fps), never all of them.
  const live = 'IntersectionObserver' in window
    ? new IntersectionObserver((entries) => {
        for (const e of entries) {
          const f = e.target
          if (e.isIntersecting) { if (f.getAttribute('src') !== f.dataset.live) f.src = f.dataset.live }
          else if (f.getAttribute('src')) f.removeAttribute('src')
        }
      }, { rootMargin: '0px 40px' })
    : null
  const watch = () => body.querySelectorAll('iframe[data-live]').forEach((f) => (live ? live.observe(f) : (f.src = f.dataset.live)))
  const close = () => {
    live?.disconnect()
    root.remove()
  }
  root.addEventListener('click', (e) => {
    if (e.target === root || e.target.closest('[data-act="done"]')) close()
  })
  root.addEventListener('keydown', (e) => e.key === 'Escape' && close())
  document.body.appendChild(root)
  root.querySelector('[data-act="done"]').focus()

  const body = root.querySelector('[data-grid]')

  // A mouse wheel scrolls up and down; over a row that scrolls sideways (a
  // narrow window), turn it into sideways scrolling until the row's end, then
  // let the sheet scroll as usual.
  body.addEventListener('wheel', (e) => {
    const row = e.target.closest('.tp-row')
    if (!row || Math.abs(e.deltaX) > Math.abs(e.deltaY)) return
    const max = row.scrollWidth - row.clientWidth
    if (max <= 1) return
    const next = row.scrollLeft + e.deltaY
    if ((e.deltaY < 0 && row.scrollLeft <= 0) || (e.deltaY > 0 && row.scrollLeft >= max - 1)) return
    row.scrollLeft = Math.max(0, Math.min(max, next))
    e.preventDefault()
  }, { passive: false })
  let all = [DEFAULT]
  let active = 'default'

  // Living scenes first: they are what people pick.
  // Koi Pond first: it is the one people love. Then the other mosaic scenes.
  const ORDER = ['koi-pond', 'honu-lagoon', 'night-reef', 'snow-pines', 'teletext-clouds', 'aurora', 'ember', 'dither-dunes', 'departure-board', 'night-sky', 'orchard', 'synthwave']
  const rank = (t) => { const i = ORDER.indexOf(t.name); return i < 0 ? 99 : i }
  const kind = (t) => (t.name === 'default' ? 'colours' : t.hasImage ? 'wallpaper' : t.hasScene ? 'scene' : 'colours')
  const row = (title, list) =>
    list.length
      ? `<section class="tp-group"><h3>${title}</h3><div class="tp-row">${list.map((t) => card(t, t.name === active)).join('')}</div></section>`
      : ''
  const paint = () => {
    const keep = [...body.querySelectorAll('.tp-row')].map((r) => r.scrollLeft)
    body.innerHTML =
      row('Living scenes', all.filter((t) => kind(t) === 'scene').sort((a, b) => rank(a) - rank(b))) +
      row('Wallpapers', all.filter((t) => kind(t) === 'wallpaper')) +
      row('Colours', all.filter((t) => kind(t) === 'colours'))
    body.querySelectorAll('.tp-row').forEach((r, i) => { r.scrollLeft = keep[i] || 0 })
    watch()
  }
  // Selecting only moves the ring: no repaint, so posters don't reload.
  const mark = () => {
    body.querySelectorAll('.tp-card').forEach((el) => {
      const on = el.dataset.theme === active
      el.classList.toggle('is-on', on)
      el.setAttribute('aria-pressed', String(on))
    })
  }

  try {
    const list = await api.themes()
    const details = await Promise.all(
      list.themes.map((t) => api.theme(t.name).then((d) => ({ ...t, colors: d.colors })).catch(() => t)),
    )
    all = [DEFAULT, ...details]
    active = tab ? tabThemeName(tab.id) : list.active || 'default'
    paint()
    // Ready before the tap: switching then applies with nothing to fetch.
    prefetch(details.map((t) => t.name))
    // Start each row where the current theme is (sideways only: the sheet
    // itself stays at the top).
    const on = body.querySelector('.tp-card.is-on')
    if (on) {
      const r = on.closest('.tp-row')
      r.scrollLeft = Math.max(0, on.offsetLeft - r.clientWidth / 2 + on.offsetWidth / 2)
    }
  } catch (err) {
    body.innerHTML = `<p class="tp-loading">Couldn't load themes: ${esc(err.message)}</p>`
    return
  }

  root.addEventListener('click', async (e) => {
    const allBtn = e.target.closest('[data-act="all"]')
    if (allBtn) {
      allBtn.disabled = true
      try {
        await api.useTheme(active)
        await Promise.all(tabIds().map((id) => setTabTheme(id, '')))
        allBtn.textContent = '✓ On all tabs'
      } catch {
        allBtn.disabled = false
      }
      return
    }
    const el = e.target.closest('[data-theme]')
    if (!el || el.dataset.theme === active) return
    const prev = active
    active = el.dataset.theme
    mark()
    try {
      await (tab ? setTabTheme(tab.id, active) : api.useTheme(active))
    } catch {
      active = prev
      mark()
    }
  })
}
