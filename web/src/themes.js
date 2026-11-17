// Themes: colours on the page, and an optional animated scene behind it.
//
// An agent pushes a theme with papaya-theme; the server announces it on
// /api/events and every open browser applies it here, live.
//
// The scene is someone else's JavaScript, so it never runs in this page. It
// runs in a sandboxed frame (no allow-same-origin: an opaque origin with no
// access to this DOM, cookies or storage) whose own policy forbids network
// access. The frame is inert and ignores the pointer, so it cannot take focus
// or keystrokes. What crosses into it is event names and tab names, never
// terminal content; what comes back is "ready", "running at N fps" or an error,
// which is forwarded to the server so the agent that wrote the scene can read
// it with `papaya-theme status`.

const TOKENS = [
  'surface-0', 'surface-1', 'surface-2', 'surface-3', 'text-hi', 'text-lo', 'accent',
  'state-ok', 'state-warn', 'state-err', 'term-bg', 'term-fg', 'term-cursor', 'term-sel',
  'ansi-black', 'ansi-red', 'ansi-green', 'ansi-yellow', 'ansi-blue', 'ansi-magenta', 'ansi-cyan', 'ansi-white',
  'ansi-bright-black', 'ansi-bright-red', 'ansi-bright-green', 'ansi-bright-yellow',
  'ansi-bright-blue', 'ansi-bright-magenta', 'ansi-bright-cyan', 'ansi-bright-white',
]

// Each tab can have its own theme. The page (chrome, accents, the backdrop)
// wears the theme of the tab in front; each terminal wears its own, so grid
// panels can differ. A tab with no theme of its own follows the owner's.
let current = null // the view the page is wearing
let globalView = null // the owner's theme
let activeId = null
const tabThemes = new Map() // tab id -> theme name ('' follows the owner's)
const views = new Map() // theme name -> view (colours, scene url, version)
let base = null // the stylesheet's own tokens, before any theme
let frame = null
let frameReady = false
let queue = []
const tabs = new Map() // id -> { id, name, host }
const lastActivity = new Map()

// The scene's view of a tab: names only.
const tabInfo = (t) => ({ id: t.id, name: t.name, host: t.host })

export function themeState() {
  return {
    backdrop: document.documentElement.dataset.backdrop === 'on',
    opacity: current?.opacity ?? 1,
  }
}

// sceneEvent is called by the tab code for things a scene may draw.
export function sceneEvent(ev) {
  switch (ev.type) {
    case 'tab-open':
      tabs.set(ev.id, { id: ev.id, name: ev.tab, host: ev.host })
      break
    case 'tab-close':
      tabs.delete(ev.id)
      lastActivity.delete(ev.id)
      break
    case 'tab-rename':
      if (tabs.has(ev.id)) tabs.get(ev.id).name = ev.tab
      break
    case 'activity': {
      // Output arrives in bursts of many frames a second; a scene needs a pulse.
      const now = Date.now()
      if (now - (lastActivity.get(ev.id) || 0) < 1500) return
      lastActivity.set(ev.id, now)
      break
    }
  }
  toFrame({ papaya: 'host', type: 'event', event: ev })
}

function toFrame(msg) {
  if (!frame) return
  if (!frameReady) {
    if (queue.length < 50) queue.push(msg)
    return
  }
  // The frame's origin is opaque, so there is no origin to name; '*' is the
  // only target that reaches it. Nothing sensitive is ever in these messages.
  frame.contentWindow?.postMessage(msg, '*')
}

function readBase() {
  if (base) return
  const cs = getComputedStyle(document.documentElement)
  base = {}
  for (const t of TOKENS) base[t] = cs.getPropertyValue(`--color-${t}`).trim()
}

async function ensureView(name) {
  if (!name || name === 'default') return { name: 'default' }
  if (views.has(name)) return views.get(name)
  try {
    const res = await fetch(`/api/theme?name=${encodeURIComponent(name)}`)
    if (!res.ok) return globalView || { name: 'default' }
    const v = await res.json()
    views.set(name, v)
    return v
  } catch {
    return globalView || { name: 'default' }
  }
}

const effectiveName = (id) => (id && tabThemes.get(id)) || globalView?.name || 'default'

// tabThemeName is what the picker marks as on for a tab.
export function tabThemeName(id) {
  return effectiveName(id)
}

export function activeTabId() {
  return activeId
}

// colorsForTab is every colour token for one tab's terminal: the stylesheet's
// own, overlaid with the tab's theme.
export function colorsForTab(id) {
  readBase()
  const v = views.get(effectiveName(id)) || (effectiveName(id) === 'default' ? null : globalView)
  return { ...base, ...(v?.colors || {}) }
}

// registerTab records a tab's own theme as the server has it.
export function registerTab(id, name) {
  tabThemes.set(id, name || '')
  if (name) ensureView(name).then(() => window.dispatchEvent(new Event('papaya:theme')))
}

export function forgetTab(id) {
  tabThemes.delete(id)
}

export function setActiveTab(id) {
  if (activeId === id) return
  activeId = id
  applyActive()
}

// setTabTheme gives one tab its own theme ('' to follow the owner's again).
// remote: false when the change came from the server already.
export async function setTabTheme(id, name, { remote = true } = {}) {
  tabThemes.set(id, name || '')
  // Wear it first, save it alongside: a tap should feel instant.
  const saved = remote
    ? fetch(`/api/sessions/${encodeURIComponent(id)}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ theme: name || '' }),
      })
    : null
  await ensureView(name)
  if (id === activeId) await applyActive()
  window.dispatchEvent(new Event('papaya:theme'))
  if (saved) await saved
}

// prefetch loads themes' views ahead of a tap (the picker calls it on open).
export function prefetch(names) {
  return Promise.all(names.map((n) => ensureView(n)))
}

// tabIds lists the tabs this page knows, for "use on all tabs".
export function tabIds() {
  return [...tabThemes.keys()]
}

async function applyActive() {
  const view = await ensureView(effectiveName(activeId))
  if (current && view.name === current.name && view.version === current.version) return
  wear(view)
}

const reduced = () => window.matchMedia?.('(prefers-reduced-motion: reduce)').matches
let wearing = 0

async function wear(view) {
  const first = current === null && !document.documentElement.dataset.theme
  current = view
  const token = ++wearing
  // Load the new backdrop first, hidden, so the reveal shows the real scene,
  // not just its colours with the scene popping in later. Never wait long.
  let fresh = null
  if (view.sceneUrl && (!frame || frame.dataset.version !== view.version)) {
    fresh = mountScene(view.sceneUrl, view.version)
    if (!first) await Promise.race([fresh.ready, new Promise((r) => setTimeout(r, 700))])
    if (token !== wearing) return // another theme was picked meanwhile
  }
  const swap = () => {
    applyColors(view.colors)
    if (view.opacity) document.documentElement.style.setProperty('--papaya-term-pct', `${Math.round(view.opacity * 100)}%`)
    else document.documentElement.style.removeProperty('--papaya-term-pct')
    if (view.sceneUrl) {
      if (fresh) showNow(fresh)
    } else unmountScene({ now: true })
    document.documentElement.dataset.theme = view.name
    document.documentElement.dataset.tone = isLight(view.colors?.['surface-0'] || view.colors?.['term-bg']) ? 'light' : 'dark'
    window.dispatchEvent(new Event('papaya:theme'))
  }
  // The new theme takes over in a circle that grows from the bottom of the
  // screen (glass.css, View Transitions in Safari and Chrome). The first
  // paint does not animate.
  if (!first && document.startViewTransition && !reduced()) document.startViewTransition(swap)
  else swap()
}

// layoutScene keeps the backdrop clear of the controls along the bottom (see
// #scene in glass.css). Not while the keyboard is up: the scene would jump.
function layoutScene() {
  const surface = document.getElementById('surface')
  if (!surface || document.documentElement.classList.contains('kb-open')) return
  const gap = Math.max(0, Math.round(window.innerHeight - surface.getBoundingClientRect().bottom))
  document.documentElement.style.setProperty('--scene-bottom', `${gap}px`)
}
window.addEventListener('resize', () => requestAnimationFrame(layoutScene))
window.addEventListener('load', layoutScene)
if ('ResizeObserver' in window) {
  queueMicrotask(() => {
    const el = document.getElementById('surface')
    if (el) new ResizeObserver(() => layoutScene()).observe(el)
  })
}

// isLight says whether a colour is a pale one (relative luminance), so the
// glass chrome can switch to its light-theme look.
function isLight(c) {
  const m = /^#?([0-9a-f]{6})$/i.exec(String(c || '').trim())
  if (!m) return false
  const n = parseInt(m[1], 16)
  const lin = (v) => { v /= 255; return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4 }
  const L = 0.2126 * lin((n >> 16) & 255) + 0.7152 * lin((n >> 8) & 255) + 0.0722 * lin(n & 255)
  return L > 0.45
}

function applyColors(colors) {
  const root = document.documentElement.style
  for (const t of TOKENS) {
    const v = colors?.[t]
    if (v) root.setProperty(`--color-${t}`, v)
    else root.removeProperty(`--color-${t}`)
  }
}

// The scene being replaced, kept up until the new one is shown.
let leaving = null

function retire(f) {
  if (!f) return
  f.classList.remove('is-in')
  setTimeout(() => f.remove(), 450)
}

// mountScene starts loading a scene behind the current one and returns its
// frame; frame.ready resolves once it has drawn.
function mountScene(url, version) {
  if (leaving) leaving.remove()
  leaving = frame
  frameReady = false
  queue = []
  const f = document.createElement('iframe')
  f.id = 'scene'
  f.title = 'Theme backdrop'
  f.setAttribute('sandbox', 'allow-scripts')
  f.setAttribute('aria-hidden', 'true')
  f.setAttribute('tabindex', '-1')
  f.setAttribute('inert', '')
  f.setAttribute('referrerpolicy', 'no-referrer')
  f.dataset.version = version
  f.ready = new Promise((resolve) => { f.onReady = resolve })
  f.src = url
  layoutScene()
  document.body.prepend(f)
  frame = f
  // If it never says it is ready, show it anyway rather than hide it forever.
  setTimeout(() => f.isConnected && f === frame && showNow(f), 2500)
  return f
}

// showNow puts a loaded scene up at once (inside the reveal, which animates
// it) and drops the one it replaces.
function showNow(f) {
  if (!f.isConnected) return
  f.style.transition = 'none'
  f.classList.add('is-in')
  void f.offsetWidth
  f.style.transition = ''
  document.documentElement.dataset.backdrop = 'on'
  if (leaving && leaving !== f) {
    leaving.remove()
    leaving = null
  }
}

function unmountScene({ now = false } = {}) {
  if (now && frame) frame.remove()
  else retire(frame)
  if (leaving) leaving.remove()
  leaving = null
  frame = null
  frameReady = false
  queue = []
  delete document.documentElement.dataset.backdrop
}

function report(body) {
  if (!current || current.name === 'default') return
  fetch(`/api/themes/${encodeURIComponent(current.name)}/status`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  }).catch(() => {})
}

window.addEventListener('message', (e) => {
  if (!frame || e.source !== frame.contentWindow) return
  const m = e.data
  if (!m || m.papaya !== 'scene' || typeof m.type !== 'string') return
  if (m.version !== current?.version) return
  if (m.type === 'ready') {
    frameReady = true
    frame.onReady?.()
    frame.contentWindow.postMessage({ papaya: 'host', type: 'init', tabs: [...tabs.values()].map(tabInfo) }, '*')
    const pending = queue
    queue = []
    pending.forEach((msg) => frame.contentWindow.postMessage(msg, '*'))
  } else if (m.type === 'running') {
    report({ version: current.version, state: 'running', fps: Number(m.fps) || 0 })
  } else if (m.type === 'error') {
    report({ version: current.version, state: 'error', error: String(m.message || '').slice(0, 2000) })
    console.warn(`papaya: theme "${current.name}" scene stopped:`, m.message)
    // A broken scene must not leave a blank page behind the terminals.
    unmountScene()
    window.dispatchEvent(new Event('papaya:theme'))
  }
})

async function load() {
  readBase()
  let view
  try {
    const res = await fetch('/api/theme')
    if (!res.ok) return
    view = await res.json()
  } catch {
    return
  }
  // A theme may have been edited: forget every cached view, then fetch what
  // the open tabs wear before repainting, so each terminal gets its own.
  views.clear()
  globalView = view
  if (view.name !== 'default') views.set(view.name, view)
  const names = new Set([...tabThemes.values()].filter(Boolean))
  await Promise.all([...names].map(ensureView))
  current = null
  await applyActive()
  window.dispatchEvent(new Event('papaya:theme'))
}

function listen() {
  if (!('EventSource' in window)) return
  const es = new EventSource('/api/events')
  es.onmessage = (e) => {
    let ev
    try {
      ev = JSON.parse(e.data)
    } catch {
      return
    }
    if (ev.type === 'theme') load()
    else if (ev.type === 'cue') toFrame({ papaya: 'host', type: 'event', event: { type: 'cue', name: String(ev.name || '') } })
    else if (ev.type === 'say') toFrame({ papaya: 'host', type: 'event', event: { type: 'say', from: String(ev.from || ''), to: String(ev.to || '') } })
  }
  // EventSource reconnects by itself. After a gap, catch up on a theme that
  // may have changed while it was away.
  es.onopen = () => load()
}

export function initThemes() {
  load()
  listen()
}
