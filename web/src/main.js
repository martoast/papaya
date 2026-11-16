import './style.css'

import { api } from './api.js'
import { TabManager, MAX_TABS } from './tabs.js'
import { writeClipboard } from './terminal.js'
import { wireUploads } from './upload.js'
import { newSessionModal, confirmModal, searchOverlay } from './modal.js'
import { VoiceRecorder, transcribe, micAvailable, micUnavailableReason } from './voice.js'
import { WaveformMeter } from './waveform.js'
import { GridView } from './grid.js'
import { wireKeyPad, wireViewport } from './keypad.js'
import { saveLayout, loadLayout, rehydrate } from './layout.js'
import { reachability } from './connectivity.js'
import { rememberTabs, recallTabs } from './remember.js'
import { runSetup, watchNewMachines } from './setup.js'
import { initThemes } from './themes.js'
import { initInstall } from './install.js'
import { showVoiceSetup } from './voicesetup.js'

const el = (id) => document.getElementById(id)

const statusLeft = el('status-left')
const statusText = el('status-text')

let noticeTimer = null
function notice(msg, kind = 'info') {
  clearTimeout(noticeTimer)
  statusText.textContent = msg
  statusText.className = kind === 'error' ? 'text-state-err' : 'text-text-lo'
  noticeTimer = setTimeout(() => {
    statusText.textContent = ''
  }, 5000)
}

// The banner outlives any single notice: losing the tailnet is a condition, not
// an event, and it stays true until the user does something about it.
const offlineBanner = el('offline-banner')
initThemes()

reachability.subscribe((up) => {
  offlineBanner.classList.toggle('hidden', up)
  offlineBanner.classList.toggle('flex', !up)
})

// --- clipboard held for a gesture ---
//
// A copy on the remote host reaches us as OSC 52, which arrives from the
// network with no user gesture behind it. Safari refuses a clipboard write in
// that state, so on a Mac — in Safari or, more to the point, in the installed
// app — copying in tmux copy-mode looked like it worked and pasted nothing.
//
// The text is kept and a button offered; pressing it is the gesture that lets
// the write through. It expires, because a Copy button still sitting there ten
// minutes later would hand over something long forgotten.
const clipboardBtn = el('clipboard-pending')
const PENDING_TTL_MS = 60_000
let pending = null
let pendingTimer = null

function showPending(text) {
  clearTimeout(pendingTimer)
  pending = text
  clipboardBtn.classList.remove('hidden')
  clipboardBtn.classList.add('inline-flex')
  notice(`copied on the far side — press Copy to put it on this clipboard`)
  pendingTimer = setTimeout(clearPending, PENDING_TTL_MS)
}

function clearPending() {
  clearTimeout(pendingTimer)
  pending = null
  clipboardBtn.classList.add('hidden')
  clipboardBtn.classList.remove('inline-flex')
}

clipboardBtn.addEventListener('click', async () => {
  const text = pending
  if (!text) return clearPending()
  const ok = await writeClipboard(text)
  clearPending()
  notice(
    ok
      ? `copied ${text.length} character${text.length === 1 ? '' : 's'}`
      : 'the browser refused the clipboard write',
    ok ? 'info' : 'error',
  )
})

const tabs = new TabManager({
  tabsEl: el('tabs'),
  stackEl: el('stack'),
  newTabBtn: el('new-tab'),
  emptyStateEl: el('empty-state'),
  onNotice: notice,
  onActiveChange: updateStatusLeft,
  onSessionUpdate: (id, patch) => gridView.updateSession(id, patch),
  onClipboardPending: showPending,
})

// The grid is arranged by session id, but ids die with the papaya process that
// issued them. Pinning each panel to its workspace as well is what lets an
// arrangement survive a restart instead of collapsing to a default tiling.
const workspaceFor = (id) => tabs.get(id)?.session?.workspace || ''

function updateStatusLeft() {
  const tab = tabs.active
  if (!tab) {
    statusLeft.textContent = `0/${MAX_TABS} sessions`
    return
  }
  const { host, user } = tab.session
  const where = view === 'grid' ? ' · grid' : ''
  statusLeft.textContent =
    `${user}@${host} · ${tab.state} · ${tabs.size}/${MAX_TABS} sessions${where}`
}

// --- session lifecycle ---

async function newSession() {
  if (tabs.atLimit) {
    notice(`maximum ${MAX_TABS} sessions`, 'error')
    return
  }

  const choice = await newSessionModal(el('overlays'))
  if (!choice) return

  // Size the PTY correctly from the start rather than opening at 80x24 and
  // immediately resizing.
  const active = tabs.active
  const cols = active?.term.cols ?? 80
  const rows = active?.term.rows ?? 24

  try {
    adoptSession(await api.createSession({ ...choice, cols, rows }))
  } catch (err) {
    notice(err.status === 429 ? `maximum ${MAX_TABS} sessions` : err.message, 'error')
  }
}

// adoptSession puts a session the server just returned on screen, wherever the
// user is currently looking.
function adoptSession(session, { activate = true } = {}) {
  tabs.add(session, { activate: activate && view !== 'grid' })
  if (view === 'grid') {
    gridView.addSession(session.id)
    gridView.focus(session.id)
    saveLayout(gridView.layout, workspaceFor)
  }
  updateStatusLeft()
}

// --- resuming work that outlived papaya ---

// Remote shells keep running when papaya stops, but nothing here remembers
// them: without this the only way back in is to recreate a tab with exactly the
// original name, which nobody memorises — least of all the "-2" a second
// unnamed tab was silently given.
async function refreshResumable() {
  const panel = el('resumable')
  const list = el('resumable-list')
  if (!panel || !list) return

  let items = []
  try {
    items = await api.listResumable()
  } catch {
    // A host being unreachable is not worth an error here; there is simply
    // nothing to offer.
  }

  panel.classList.toggle('hidden', items.length === 0)
  list.replaceChildren()

  for (const w of items) {
    const row = document.createElement('button')
    row.className = 'lg-row flex items-center justify-between gap-4 text-sm'
    row.dataset.workspace = w.name

    const left = document.createElement('span')
    left.className = 'font-mono text-text-hi'
    left.textContent = w.label

    const right = document.createElement('span')
    right.className = 'opacity-60'
    right.textContent = `${w.host} · ${w.windows} window${w.windows === 1 ? '' : 's'}`

    row.append(left, right)
    row.addEventListener('click', () => resume(w))
    list.append(row)
  }
}

async function resume(w) {
  const active = tabs.active
  const cols = active?.term.cols ?? 80
  const rows = active?.term.rows ?? 24

  try {
    adoptSession(
      await api.resumeSession({
        host: w.host,
        user: w.user,
        name: w.label,
        workspace: w.name,
        cols,
        rows,
      }),
    )
    await refreshResumable()
  } catch (err) {
    notice(err.status === 429 ? `maximum ${MAX_TABS} sessions` : err.message, 'error')
  }
}

// adoptLiveSessions shows any session this device is not yet displaying that the
// server already has, so every device converges on the one shared session list.
//
// The list at /api/sessions is keyed by tailnet identity, not by browser, so a
// session opened on the laptop is already in the phone's list — it simply had no
// tab for it. This makes the two match. activate:false so catching up on
// background work never yanks the user off the tab they are on, and tabs.add is
// a no-op for a session already shown, so this is safe to run on every resync.
function adoptLiveSessions(sessions) {
  const shown = new Set(tabs.all().map((t) => t.id))
  for (const s of sessions) {
    if (shown.has(s.id)) continue
    tabs.add(s, { activate: false })
    if (view === 'grid') {
      gridView.addSession(s.id)
      saveLayout(gridView.layout, workspaceFor)
    }
  }
}

async function closeSession(id) {
  await tabs.close(id)
  gridView.removeSession(id)
  saveLayout(gridView.layout, workspaceFor)
  updateStatusLeft()

  // Closing the last tab brings the empty state back, and with it whatever is
  // still running elsewhere.
  if (tabs.size === 0) refreshResumable()
}

async function closeActive() {
  const tab = tabs.active
  if (!tab) return

  if (tabs.size === 1) {
    const ok = await confirmModal(el('overlays'), 'Close the last session?')
    if (!ok) return
  }

  await closeSession(tab.id)
}

// --- keyboard shortcuts ---

function isEditable(target) {
  if (!target) return false
  if (target.dataset?.renameInput === 'true') return true
  const tag = target.tagName
  return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || target.isContentEditable
}

document.addEventListener('keydown', (e) => {
  // A rename input or modal field owns its keystrokes.
  if (isEditable(e.target) && e.target.dataset?.renameInput === 'true') return

  if (e.key === 'F2') {
    e.preventDefault()
    tabs.renameActive()
    return
  }

  const mod = e.metaKey || e.ctrlKey
  if (!mod) return

  // xterm's textarea is where terminal keystrokes land; a modal input is not.
  if (isEditable(e.target) && !e.target.classList.contains('xterm-helper-textarea')) return

  if (e.shiftKey && (e.key === '[' || e.key === ']')) {
    e.preventDefault()
    tabs.cycle(e.key === '[' ? -1 : 1)
    return
  }

  switch (e.key.toLowerCase()) {
    case 't':
      e.preventDefault()
      newSession()
      break

    case 'w':
      e.preventDefault()
      closeActive()
      break

    case 'g':
      e.preventDefault()
      setView(view === 'grid' ? 'tabs' : 'grid')
      break

    case 'f': {
      e.preventDefault()
      const tab = tabs.active
      if (tab) searchOverlay(el('overlays'), tab)
      break
    }

    case '0':
      e.preventDefault()
      tabs.activateLast()
      break

    default: {
      const n = Number(e.key)
      if (Number.isInteger(n) && n >= 1 && n <= 9) {
        e.preventDefault()
        tabs.activateIndex(n - 1)
      }
    }
  }
})

el('new-tab').addEventListener('click', newSession)

// --- uploads ---

const uploads = wireUploads({
  overlayEl: el('drop-overlay'),
  progressEl: el('upload-progress'),
  barEl: el('upload-bar'),
  getActiveTab: () => tabs.active,
  getTabAt: (node) => {
    const panel = node?.closest?.('[data-panel-id]')
    return panel ? tabs.get(panel.dataset.panelId) : null
  },
  onNotice: notice,
})

// The attach button opens the native file picker — on a phone that is Photo
// Library / Take Photo / Files, which is how a screenshot gets in without a
// keyboard, a clipboard round-trip, or a drag nobody can do on a touchscreen.
// Drag-and-drop stays for the desktop.
{
  const fileInput = el('file-input')
  el('attach').addEventListener('click', () => fileInput.click())
  fileInput.addEventListener('change', () => {
    const files = Array.from(fileInput.files || [])
    if (files.length) uploads.sendAll(files)
    // Clear it, or picking the same screenshot twice in a row fires nothing.
    fileInput.value = ''
  })
}

// --- views ---

// Two ways to look at the same sessions: one terminal at a time behind tabs, or
// every terminal at once as resizable panels. The grid is the control centre —
// several agent sessions streaming side by side, all live.
const viewToggle = el('view-toggle')
const viewLabel = el('view-label')
const viewIcon = el('view-icon')
const balanceBtn = el('balance')

const gridView = new GridView({
  root: el('grid'),
  getTab: (id) => tabs.get(id),
  onClose: (id) => closeSession(id),
  onFocus: (id) => {
    tabs.setActiveId(id)
    updateStatusLeft()
  },
  onNotice: notice,
})

// Persist the arrangement so a reload comes back to the same control centre.
gridView.onLayoutSettled = () => saveLayout(gridView.layout, workspaceFor)

// Renaming from a grid panel opens the input in the tab bar, which is visible
// in both views, rather than duplicating the edit-in-place machinery per panel.
gridView.onRename = (id) => tabs.beginRename(id)

const VIEW_KEY = 'papaya.view.v1'
let view = 'tabs'

function setView(next) {
  if (next === view) return
  view = next

  if (view === 'grid') {
    tabs.suspend() // stop the tab stack from hiding or re-parenting terminals
    gridView.show(tabs.ids(), rehydrate(loadLayout(), tabs.all().map((t) => t.session)))
    if (tabs.activeId) gridView.focus(tabs.activeId)
  } else {
    saveLayout(gridView.layout, workspaceFor)
    gridView.hide()
    tabs.resume() // reclaim the terminals into the stack
  }

  el('stack').classList.toggle('hidden', view === 'grid')
  balanceBtn.classList.toggle('hidden', view !== 'grid')
  // The Grid toggle button was removed from the bar; grid still exists behind
  // ⌘G for anyone who wants it, so these keep the button in sync only if present.
  if (viewToggle) {
    viewToggle.dataset.view = view
    viewLabel.textContent = view === 'grid' ? 'Tabs' : 'Grid'
    viewIcon.textContent = view === 'grid' ? '▤' : '▦'
    viewToggle.title =
      view === 'grid' ? 'Switch to tab view (⌘G)' : 'Switch to grid view (⌘G)'
  }

  try {
    localStorage.setItem(VIEW_KEY, view)
  } catch {
    // Not persisting the view choice is harmless.
  }

  updateStatusLeft()
}

viewToggle?.addEventListener('click', () => setView(view === 'grid' ? 'tabs' : 'grid'))
balanceBtn.addEventListener('click', () => {
  gridView.balance()
  saveLayout(gridView.layout, workspaceFor)
})

// --- voice input ---

const micBtn = el('mic')
const micDot = el('mic-dot')
const micLabel = el('mic-label')

const pill = el('recording-pill')
const pillTime = el('recording-time')
const pillHint = el('recording-hint')
const pillDot = el('recording-dot')

let voiceEnabled = false
let elapsedTimer = null
let silenceWarned = false

const meter = new WaveformMeter(el('recording-wave'), {
  onLevel: (_level, peak) => {
    // Say something once it is clear the microphone is producing nothing, while
    // the user can still fix it, rather than after an empty transcript.
    if (silenceWarned || peak > 0.012) return
    if (Date.now() - recordingStartedAt > 2000) {
      silenceWarned = true
      pillHint.textContent = 'no audio detected — check your microphone'
      pillHint.classList.add('text-state-err')
    }
  },
})

let recordingStartedAt = 0

const voice = new VoiceRecorder({
  onNotice: notice,
  onStateChange: (state) => setMicState(state),
  onStream: (stream) => meter.attach(stream),
})

function formatElapsed(ms) {
  const s = Math.floor(ms / 1000)
  return `${String(Math.floor(s / 60)).padStart(2, '0')}:${String(s % 60).padStart(2, '0')}`
}

function setMicState(state) {
  micBtn.dataset.state = state
  micDot.dataset.on = String(state === 'recording')
  pill.dataset.state = state

  clearInterval(elapsedTimer)

  if (state === 'recording') {
    recordingStartedAt = Date.now()
    silenceWarned = false

    pillHint.textContent = 'release to send · Esc cancels'
    pillHint.classList.remove('text-state-err')
    pillDot.classList.add('animate-pulse')
    // The canvas has no size until the pill is displayed.
    requestAnimationFrame(() => meter.resize())

    const tick = () => {
      const text = formatElapsed(Date.now() - recordingStartedAt)
      pillTime.textContent = text
      micLabel.textContent = text
    }
    tick()
    elapsedTimer = setInterval(tick, 250)
    return
  }

  meter.detach()

  if (state === 'transcribing') {
    micLabel.textContent = '…'
    pillTime.textContent = formatElapsed(Date.now() - recordingStartedAt)
    pillHint.textContent = 'transcribing…'
    pillHint.classList.remove('text-state-err')
    pillDot.classList.remove('animate-pulse')
    return
  }

  micLabel.textContent = 'Voice'
}

async function startVoice() {
  if (!voiceEnabled || voice.isRecording) return
  if (!tabs.active) {
    notice('open a session before recording', 'error')
    return
  }
  await voice.start()
}

async function stopVoiceAndTranscribe() {
  if (!voice.isBusy) return

  const tab = tabs.active
  const audio = await voice.stop()
  if (!audio || !tab) {
    setMicState('idle')
    return
  }

  setMicState('transcribing')
  try {
    const res = await transcribe(tab.id, audio.blob, audio.filename)
    if (!res.text) {
      notice('nothing was heard')
    } else if (res.injected === false) {
      notice(`transcribed but not inserted: ${res.text}`, 'error')
    } else {
      notice(`"${res.text}"`)
      tab.focus()
    }
  } catch (err) {
    notice(err.message || 'transcription failed', 'error')
  } finally {
    setMicState('idle')
  }
}

// Click toggles; holding the hotkey is push-to-talk.
micBtn.addEventListener('click', () => {
  if (!voiceEnabled) {
    if (micBtn.dataset.setup === 'true') showVoiceSetup({ onEnabled: initVoice })
    return
  }
  // isBusy, not isRecording: a press during the acquire window must stop rather
  // than queue a second recording.
  if (voice.isBusy) stopVoiceAndTranscribe()
  else startVoice()
})

// Ctrl+Space is push-to-talk: held down records, releasing transcribes. It is
// not a terminal binding, and xterm is told to ignore it in terminal.js.
let pttHeld = false
document.addEventListener('keydown', (e) => {
  if (e.code !== 'Space' || !e.ctrlKey || e.metaKey || e.altKey) return
  if (isEditable(e.target) && !e.target.classList.contains('xterm-helper-textarea')) return

  e.preventDefault()
  if (!pttHeld) {
    pttHeld = true
    startVoice()
  }
})

document.addEventListener('keyup', (e) => {
  if (!pttHeld) return
  // Releasing either Control or Space ends the utterance.
  if (e.code === 'Space' || e.key === 'Control') {
    pttHeld = false
    stopVoiceAndTranscribe()
  }
})

// Escape abandons a recording without sending it.
document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape' && voice.isBusy) {
    pttHeld = false
    voice.cancel()
    notice('recording discarded')
  }
})

async function initVoice() {
  let caps
  try {
    caps = await api.capabilities()
  } catch {
    return // older server, or not reachable; leave the button hidden
  }

  if (!caps.transcription) {
    // Not on yet, but it can be turned on from here: the mic stays, and
    // tapping it asks for the key.
    if (caps.voiceSettable) {
      micBtn.dataset.setup = 'true'
      micBtn.classList.remove('hidden')
      micBtn.classList.add('flex')
      micBtn.title = 'Turn on voice input'
      setMicState('idle')
    }
    return
  }
  delete micBtn.dataset.setup

  if (!micAvailable()) {
    // The server supports it but this page cannot use it. Show why rather than
    // silently omitting the feature.
    micBtn.classList.remove('hidden')
    micBtn.classList.add('flex')
    micBtn.disabled = true
    micBtn.title = micUnavailableReason()
    micLabel.textContent = 'Voice'
    return
  }

  voiceEnabled = true
  micBtn.classList.remove('hidden')
  micBtn.classList.add('flex')
  setMicState('idle')
}

// --- waking up ---

// Closing the lid suspends everything. On wake the sockets are dead, and if
// papaya restarted in the meantime its session list is empty — so the ids these
// tabs hold refer to nothing, and no amount of reconnecting can revive them.
// That is what "the tabs go stale and there is no way to refresh" is: an
// installed app has no address bar, so there was no reload to fall back on.
//
// The Reconnect button runs exactly this, deliberately: the manual path and the
// automatic one must not be able to drift apart.
let resyncing = false

async function resync() {
  if (resyncing) return
  resyncing = true

  try {
    if (!(await reachability.check())) return // banner is up; polling recovers

    let sessions
    try {
      sessions = await api.listSessions()
    } catch {
      return
    }
    const live = new Set(sessions.map((s) => s.id))

    // Captured before anything is closed: closing a tab drops it from the
    // remembered set on the next sweep, and that set is how its workspace is
    // found again.
    const remembered = recallTabs()

    // Sessions the server still has only need their socket back — and need it
    // now, not at the end of a backoff interval that may be most of a minute.
    for (const tab of tabs.all()) {
      if (live.has(tab.id)) tab.reconnectNow()
    }

    // Sessions it has forgotten cannot be reconnected at all; the id is gone.
    // Dropped locally, never closed: close() sends a delete, and a delete kills
    // the remote workspace. If this list were ever wrong about a session being
    // gone — a truncated response, a proxy hiccup — closing would destroy the
    // very work this is trying to recover. Forgetting costs a reattach.
    for (const tab of tabs.all()) {
      if (!live.has(tab.id)) tabs.forget(tab.id)
    }

    // Sessions opened on another device show up in the shared list; show any
    // this device is not yet displaying, so refocusing the phone catches up with
    // work started on the laptop.
    adoptLiveSessions(sessions)

    await reattachRemembered(sessions, remembered)
    updateStatusLeft()
    refreshResumable()
  } finally {
    resyncing = false
  }
}

// A lid opening surfaces as one of these, depending on the browser and on
// whether the page was frozen or merely put in the background.
//
// Every tab is woken first, before resync asks the server anything. resync
// starts with a reachability check and a session list, two round trips that on
// a phone reopening over a relayed tailnet path cost real time, and sockets
// used to wait for both before reconnecting. A tab whose session is still on
// the server (the normal case) needs neither: it reconnects, or proves its
// socket alive, in parallel with resync's checks. resync still handles the
// rare case of sessions the server has forgotten.
function wake() {
  for (const tab of tabs.all()) tab.wake()
  resync()
}

document.addEventListener('visibilitychange', () => {
  if (document.visibilityState === 'visible') wake()
})
window.addEventListener('online', wake)
window.addEventListener('focus', wake)
window.addEventListener('pageshow', (e) => {
  if (e.persisted) wake()
})

// The manual escape hatch, shown only when something is actually wrong so it
// does not sit there implying the app needs nursing.
const reconnectBtn = el('reconnect')
reconnectBtn?.addEventListener('click', () => {
  notice('reconnecting…')
  resync()
})

function refreshReconnectButton() {
  if (!reconnectBtn) return
  const stuck = tabs.all().some((t) => t.state === 'detached' || t.state === 'closed')
  const show = !reachability.reachable || stuck
  reconnectBtn.classList.toggle('hidden', !show)
  reconnectBtn.classList.toggle('inline-flex', show)
}

// The banner is the biggest thing on screen when the tailnet drops, so it is
// also the biggest target for getting back.
offlineBanner?.addEventListener('click', resync)

// --- full screen ---

// A browser tab always looks like a browser tab, and the shortcut for this is
// reserved by the browser itself, so there is no way to reach it from the app
// without a button. In full screen the tab bar becomes the whole chrome, which
// is the closest this gets to being an application window.
function wireFullscreen() {
  const btn = el('fullscreen')
  if (!btn) return

  // Prefixed names for Safari, which only unprefixed these recently. iPhone
  // Safari has no Fullscreen API at all, and reports neither.
  const enabled = document.fullscreenEnabled || document.webkitFullscreenEnabled
  if (!enabled) return

  btn.classList.remove('hidden')
  btn.classList.add('flex')

  const active = () => Boolean(document.fullscreenElement || document.webkitFullscreenElement)

  const paint = () => {
    const on = active()
    btn.dataset.on = String(on)
    // The label carries the state, not the icon: there is no unicode pair for
    // enter/exit that reads clearly at this size.
    el('fullscreen-label').textContent = on ? 'Exit' : 'Full'
    btn.title = on ? 'Leave full screen (Esc)' : 'Full screen'
    btn.setAttribute('aria-label', btn.title)
  }

  btn.addEventListener('click', async () => {
    try {
      if (active()) {
        await (document.exitFullscreen?.() ?? document.webkitExitFullscreen?.())
      } else {
        const root = document.documentElement
        await (root.requestFullscreen?.() ?? root.webkitRequestFullscreen?.())
      }
    } catch {
      // Refused — a browser may require a gesture it did not see as one, or
      // the window manager may simply decline. Nothing to recover.
      notice('the browser refused full screen', 'error')
    }
  })

  // Nothing here re-measures the terminals: both views already watch their own
  // container with a ResizeObserver, and entering full screen resizes those
  // containers like any other window change.
  for (const ev of ['fullscreenchange', 'webkitfullscreenchange']) {
    document.addEventListener(ev, paint)
  }

  paint()
}

wireFullscreen()

// --- touch keys ---

wireKeyPad(el('keypad'), () => tabs.active)
wireViewport(el('app'))

// Test hook: deliver a bell the way the server does. Same-origin script already
// has full access to the page, so this exposes nothing new.
window.__papaya_test_bell = (id) => {
  const tab = tabs.get(id)
  if (tab) tab.handleEvent(JSON.stringify({ type: 'bell' }))
}

// Test hook: hand a test the tab object for a session id, so it can watch what
// the key pad writes to the socket. Same-origin access, nothing new exposed.
window.__papaya_tab_for = (id) => tabs.get(id)

// --- boot ---

// reattachRemembered puts back tabs this browser had open that the server no
// longer knows about.
//
// The server's session list is held in memory, so restarting papaya empties it
// while every remote shell carries on running in tmux. Without this the tab bar
// comes back empty and the only way back in is to remember the workspace name
// and type it exactly.
//
// A remembered entry is a hint, not an instruction: nothing is reattached
// unless the workspace is confirmed to be still running out there. Resuming a
// workspace that has since died would open an empty shell wearing the name of
// work that is gone, which is worse than an empty tab bar.
async function reattachRemembered(live, remembered = recallTabs()) {
  const key = (host, user, workspace) => `${host}\u0000${user}\u0000${workspace}`

  if (!remembered.length) return

  const alive = new Set(live.map((s) => key(s.host, s.user, s.workspace)))
  const missing = remembered.filter((e) => !alive.has(key(e.host, e.user, e.workspace)))
  if (!missing.length) return

  let running = []
  try {
    running = await api.listResumable()
  } catch {
    // A host that cannot be reached has nothing to offer; leave the entries in
    // storage so a later load can still find them.
    return
  }
  const runningKeys = new Set(running.map((w) => key(w.host, w.user, w.name)))

  const active = tabs.active
  const cols = active?.term.cols ?? 80
  const rows = active?.term.rows ?? 24

  let restored = 0
  for (const e of missing) {
    if (tabs.atLimit) break
    if (!runningKeys.has(key(e.host, e.user, e.workspace))) continue

    try {
      const resumed = await api.resumeSession({
        host: e.host,
        user: e.user,
        name: e.name || '',
        workspace: e.workspace,
        cols,
        rows,
      })
      // Never yank the user off a tab they are already looking at.
      adoptSession(resumed, { activate: !tabs.active })
      restored += 1
    } catch {
      // One workspace failing must not stop the rest coming back.
    }
  }

  if (restored) {
    notice(`reconnected ${restored} session${restored === 1 ? '' : 's'}`)
  }
}

async function restore() {
  try {
    const sessions = await api.listSessions()
    for (const s of sessions) {
      tabs.add(s, { activate: false })
    }

    // Show what the server has straight away. Reattaching work it forgot can
    // mean scanning every host over SSH, which takes as long as the slowest
    // one (8 s when a machine is asleep), and the screen used to stay empty
    // for all of it on any launch where one remembered tab was missing —
    // typically on the phone, after a tab was closed on the laptop.
    if (tabs.size) tabs.activateIndex(0)

    // Then the work the server forgot but tmux kept. A reattached tab is
    // only brought to the front if nothing else was on screen.
    await reattachRemembered(sessions)

    let saved = null
    try {
      saved = localStorage.getItem(VIEW_KEY)
    } catch {
      // no stored preference
    }
    // tabs.size, not sessions.length: reattached work counts too, and dropping
    // into the tab view because the server's list happened to be empty would
    // undo the grid the user left set.
    if (saved === 'grid' && tabs.size) setView('grid')

    updateStatusLeft()
  } catch (err) {
    notice(`could not load sessions: ${err.message}`, 'error')
    // The usual cause is that the tailnet went away between serving this page
    // and its first request. Find out, so the banner can say so instead of
    // leaving a bare error that scrolls away after five seconds.
    reachability.check()
  }

  // Ask what is still running out there. Deliberately after the tabs are on
  // screen: it costs an SSH round trip per host, and the sessions that already
  // exist must not wait on a host that may be unreachable.
  refreshResumable()

  el('resume-all')?.addEventListener('click', async () => {
    let items = []
    try {
      items = await api.listResumable()
    } catch {
      return
    }
    for (const w of items) await resume(w)
  })
}

// Keep the status line current as connection states change, and keep the
// remembered tab set with it.
//
// Swept rather than hooked into every mutation: tabs are added, closed,
// renamed, reordered and auto-titled from half a dozen places, and a sweep
// cannot be forgotten by the next one added. It writes only when the set has
// actually changed, so the cost is a small JSON encode per second.
setInterval(() => {
  updateStatusLeft()
  rememberTabs(tabs.all())
  refreshReconnectButton()
}, 1000)

window.addEventListener('resize', () => {
  if (voice.isRecording) meter.resize()
})

window.addEventListener('beforeunload', () => {
  // Sessions deliberately survive a reload; only the sockets go away.
})

// A fresh install opens on the setup screen; everything else starts as before.
// Asking first costs one request, and a failure to ask (an older server, a
// blip) falls through to the normal app rather than blocking it.
async function start() {
  let setupState = null
  try {
    setupState = await api.setup()
  } catch {
    // Not available: carry on as configured.
  }

  if (setupState && !setupState.complete) {
    const first = await runSetup(el('overlays'), setupState)
    await restore()
    if (first && tabs.size === 0) openOn(first)
  } else {
    restore()
  }
  watchNewMachines(el('settings-dot'))
  // After setup, so the install guide never covers the first-run screen.
  initInstall(el('install-app'))
}

// openOn opens the first terminal on a machine setup has just made ready.
async function openOn({ host, user }) {
  try {
    adoptSession(await api.createSession({ host, user, cols: 80, rows: 24 }))
  } catch (err) {
    notice(err.message, 'error')
  }
}

el('settings').addEventListener('click', async () => {
  let st
  try {
    st = await api.setup()
  } catch (err) {
    notice(`could not open setup: ${err.message}`, 'error')
    return
  }
  const first = await runSetup(el('overlays'), st, { reopen: true })
  api.seenMachines().catch(() => {})
  el('settings-dot').classList.add('hidden')
  if (first && tabs.size === 0) openOn(first)
})

start()
initVoice()

// The worker only supplies an offline fallback page (see public/sw.js). It is
// registered last and never awaited: the app must behave identically whether or
// not it installs, and a browser that refuses it loses nothing but the
// explanation on a cold load with the tailnet down.
if ('serviceWorker' in navigator) {
  window.addEventListener('load', () => {
    navigator.serviceWorker.register('/sw.js').catch(() => {
      // Not supported, or blocked by policy. Nothing else depends on it.
    })
  })
}

// The empty state's big + does what the bar's + does.
el('empty-new')?.addEventListener('click', () => el('new-tab').click())
