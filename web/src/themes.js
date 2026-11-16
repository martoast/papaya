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

let current = null
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
  let view
  try {
    const res = await fetch('/api/theme')
    if (!res.ok) return
    view = await res.json()
  } catch {
    return
  }
  if (current && view.name === current.name && view.version === current.version) return
  current = view
  applyColors(view.colors)
  if (view.opacity) document.documentElement.style.setProperty('--papaya-term-pct', `${Math.round(view.opacity * 100)}%`)
  else document.documentElement.style.removeProperty('--papaya-term-pct')
  if (view.sceneUrl) mountScene(view.sceneUrl, view.version)
  else unmountScene()
  document.documentElement.dataset.theme = view.name
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
