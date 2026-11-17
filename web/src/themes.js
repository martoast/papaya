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
  if (remote) {
    await fetch(`/api/sessions/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ theme: name || '' }),
    })
  }
  await ensureView(name)
  if (id === activeId) await applyActive()
  window.dispatchEvent(new Event('papaya:theme'))
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

function wear(view) {
  current = view
  applyColors(view.colors)
  if (view.opacity) document.documentElement.style.setProperty('--papaya-term-pct', `${Math.round(view.opacity * 100)}%`)
  else document.documentElement.style.removeProperty('--papaya-term-pct')
  if (view.sceneUrl) {
    if (!frame || frame.dataset.version !== view.version) mountScene(view.sceneUrl, view.version)
  } else unmountScene()
  document.documentElement.dataset.theme = view.name
  window.dispatchEvent(new Event('papaya:theme'))
}

function applyColors(colors) {
  const root = document.documentElement.style
  for (const t of TOKENS) {
    const v = colors?.[t]
    if (v) root.setProperty(`--color-${t}`, v)
    else root.removeProperty(`--color-${t}`)
  }
}

function mountScene(url, version) {
  unmountScene()
  frame = document.createElement('iframe')
  frame.id = 'scene'
  frame.title = 'Theme backdrop'
  frame.setAttribute('sandbox', 'allow-scripts')
  frame.setAttribute('aria-hidden', 'true')
  frame.setAttribute('tabindex', '-1')
  frame.setAttribute('inert', '')
  frame.setAttribute('referrerpolicy', 'no-referrer')
  frame.dataset.version = version
  frame.src = url
  document.body.prepend(frame)
  document.documentElement.dataset.backdrop = 'on'
}

function unmountScene() {
  if (frame) frame.remove()
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
