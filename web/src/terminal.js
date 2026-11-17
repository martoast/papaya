import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebLinksAddon } from '@xterm/addon-web-links'
import { SearchAddon } from '@xterm/addon-search'
import { Unicode11Addon } from '@xterm/addon-unicode11'
import { WebglAddon } from '@xterm/addon-webgl'

import { xtermTheme, monoFont } from './theme.js'
import { sceneEvent, themeState, colorsForTab, setTabTheme } from './themes.js'
import { api } from './api.js'
import { reachability } from './connectivity.js'

// How long a socket that still claims to be open gets to answer a ping after
// the page comes back from the background before it is presumed dead. A live
// round trip over a relayed tailnet path is well under a second.
const WAKE_PROBE_MS = 2000

const encoder = new TextEncoder()

// A generous cap on an OSC 52 payload. Copying a whole file is legitimate;
// unbounded growth from a runaway program is not.
const MAX_CLIPBOARD_B64 = 2_000_000

// writeClipboard puts text on the system clipboard, by whichever route the
// browser permits.
export async function writeClipboard(text) {
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    // The async API needs a recent user gesture in some browsers, and OSC 52
    // arrives from the network a turn later than the keystroke that caused it.
    // The old synchronous path has no such requirement.
    return legacyCopy(text)
  }
}

function legacyCopy(text) {
  const ta = document.createElement('textarea')
  ta.value = text
  ta.setAttribute('readonly', '')
  ta.style.cssText = 'position:fixed;top:-9999px;opacity:0'
  document.body.appendChild(ta)

  // Selecting the textarea steals focus; the terminal must get it back or the
  // next keystroke goes nowhere.
  const focused = document.activeElement
  ta.select()
  let ok = false
  try {
    ok = document.execCommand('copy')
  } catch {
    ok = false
  }
  ta.remove()
  focused?.focus?.()
  return ok
}

// isCopyKey reports the platform's "copy the selection" chord: Cmd+C on a Mac,
// Ctrl+Shift+C everywhere else.
//
// Plain Ctrl+C is deliberately excluded. It is the interrupt, and a terminal
// that spends it on copying takes away the only key that stops a runaway
// program. Ctrl+Shift+C is what gnome-terminal and VS Code use for the same
// reason.
function isCopyKey(e) {
  if (e.key?.toLowerCase() !== 'c' || e.altKey) return false
  if (e.metaKey) return !e.ctrlKey
  return e.ctrlKey && e.shiftKey
}

// Shortcuts the app handles itself. xterm captures most keys by default, so the
// handler below must decline these so they reach the document listener.
function isAppShortcut(e) {
  // Push-to-talk. Ctrl+Space is NUL in a terminal, which nothing useful sends,
  // so taking it costs nothing and it must reach the document handler.
  if (e.ctrlKey && e.code === 'Space' && !e.metaKey && !e.altKey) return true

  const mod = e.metaKey || e.ctrlKey
  if (!mod) return e.key === 'F2'
  if (e.shiftKey && (e.key === '[' || e.key === ']')) return true
  return ['t', 'w', 'f', 'g', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9'].includes(
    e.key.toLowerCase(),
  )
}

export class TerminalTab {
  constructor(
    session,
    { onStateChange, onRename, onClose, onNotice, onAttention, onActivity, onClipboardPending },
  ) {
    this.session = session
    this.onStateChange = onStateChange
    this.onRename = onRename
    this.onClose = onClose
    this.onNotice = onNotice
    // Raised when text arrived for the clipboard and the browser refused the
    // write for want of a user gesture. The app offers one.
    this.onClipboardPending = onClipboardPending
    // Raised when the session wants a human: the remote rang the terminal bell.
    this.onAttention = onAttention
    // Raised when what the program in this tab is doing changes: working,
    // done (finished while nobody watched: your turn) or idle.
    this.onActivity = onActivity
    this.activity = 'idle'
    this.outBuckets = []
    this.lastInputAt = 0
    this.connectedAt = 0
    this.workStart = 0
    this.lastWorkAt = 0

    this.state = session.state || 'connecting'
    // Set by the server bell event, cleared when the user looks at the session.
    this.attention = Boolean(session.attention)
    this.lastOutputAt = 0
    this.userNamed = Boolean(session.name && session.name !== session.host)
    this.active = false
    this.disposed = false

    this.ws = null
    this.reconnectAttempt = 0
    this.reconnectTimer = null
    // Pending wake() probe; cleared by the server's pong.
    this.probeTimer = null
    // Whether a socket for this tab has ever completed a handshake. A session
    // that has worked once and then dropped is probably a blip; one that has
    // never connected is more likely to be a session the server does not have.
    this.everOpened = false
    this.resizeTimer = 0
    // Last geometry sent, so repeated fits do not re-send an unchanged size.
    this.sentCols = 0
    this.sentRows = 0
    // Set by the grid view: every panel is visible, so each syncs its own size.
    this.syncAlways = false

    this.el = document.createElement('div')
    this.el.className = 'term-host'
    this.el.dataset.active = 'false'
    this.el.dataset.terminalId = session.id

    this.term = new Terminal({
      allowProposedApi: true,
      cursorBlink: true,
      fontFamily: monoFont(),
      fontSize: 13,
      lineHeight: 1.2,
      scrollback: 10000,
      theme: xtermTheme(colorsForTab(session.id)),
      // Lets a theme's scene show through the terminal background.
      allowTransparency: true,
      screenReaderMode: false,
      macOptionIsMeta: true,
      // Full-screen programs — Claude Code, vim, htop — turn on mouse
      // reporting, and from then on a drag belongs to them, not to the
      // browser. There is nothing selected locally, so there is nothing for
      // Cmd+C to copy. Holding Option takes the mouse back for one gesture.
      macOptionClickForcesSelection: true,
    })

    this.fitAddon = new FitAddon()
    this.searchAddon = new SearchAddon()

    this.term.loadAddon(this.fitAddon)
    this.term.loadAddon(new WebLinksAddon())
    this.term.loadAddon(this.searchAddon)

    const unicode11 = new Unicode11Addon()
    this.term.loadAddon(unicode11)
    this.term.unicode.activeVersion = '11'

    this.term.open(this.el)

    // Debugging and test hook. The canvas and WebGL renderers draw no DOM text,
    // so reading the buffer is the only way to inspect terminal contents from
    // outside. Same-origin scripts already have full access, so this exposes
    // nothing new.
    this.el._terminal = this.term

    // WebGL is a large win on heavy output, but it paints an opaque
    // background, so while a theme's scene runs behind the terminals they use
    // xterm's DOM renderer instead, which lets the scene through.
    this.webgl = null
    this.useWebgl(!themeState().backdrop)

    this.installClipboardBridge()
    this.installStatusProtocol()
    this.installTouchGestures()

    this.term.attachCustomKeyEventHandler((e) => {
      if (e.type !== 'keydown') return true
      if (isCopyKey(e) && this.term.hasSelection()) {
        this.copySelection()
        // Suppress the browser's own copy, so exactly one writer runs. Chromium
        // would otherwise fire its copy event as well and race this one to
        // write the same text.
        e.preventDefault()
        return false
      }
      if (isAppShortcut(e)) return false
      return true
    })

    this.term.onData((data) => this.send(data))

    // A theme was applied: recolour this terminal in place, and pick the
    // renderer that suits it.
    this.onTheme = () => {
      this.useWebgl(!themeState().backdrop)
      const tokens = colorsForTab(this.id)
      this.term.options.theme = xtermTheme(tokens)
      // The host's tint behind the text (backdrop on) is this tab's too.
      if (tokens['term-bg']) this.el.style.setProperty('--color-term-bg', tokens['term-bg'])
    }
    this.onTheme()
    window.addEventListener('papaya:theme', this.onTheme)

    // Shells emit OSC 0/2 title sequences; use them to auto-title the tab.
    this.term.onTitleChange((title) => this.handleTitle(title))

    this.connect()
  }

  get id() {
    return this.session.id
  }

  // --- clipboard ---

  // copySelection puts the terminal's own selection on the clipboard.
  //
  // xterm.js does not implement the copy chord. It listens for the browser's
  // `copy` event and fills in the clipboard data when one arrives — and that
  // event only fires if the browser believes something is selected. With the
  // WebGL renderer nothing in the document is: the selection is painted onto a
  // canvas, and xterm's helper textarea is empty. Chromium dispatches `copy` to
  // the focused element regardless, so it works there and every browser test
  // here runs on Chromium. WebKit does not, which is every Safari tab and every
  // installed PWA on a Mac: select, press Cmd+C, and nothing happens at all —
  // no copy, no error, nothing to tell you why.
  //
  // Handling the chord ourselves makes the engines agree, and puts the write on
  // a keystroke, which is the user gesture Safari requires before it will let
  // anything near the clipboard.
  copySelection() {
    const text = this.term.getSelection()
    if (!text) return false
    writeClipboard(text).then((ok) => {
      // Success needs no announcement: the user selected the text and pressed
      // the key. Only a refusal is worth a line in the status bar.
      if (!ok) this.onNotice?.('the browser refused the clipboard write', 'error')
    })
    return true
  }

  // OSC 52 is how a program on the far side says "put this on the clipboard".
  // tmux forwards it outward by default, and it is what makes copying work over
  // SSH at all — the remote host cannot otherwise reach your Mac's clipboard.
  //
  // xterm.js does not handle the sequence on its own, so without this it
  // arrives and is dropped: the copy appears to succeed on the remote and
  // nothing lands locally.
  installClipboardBridge() {
    this.term.parser.registerOscHandler(52, (payload) => {
      const sep = payload.indexOf(';')
      if (sep < 0) return true
      const data = payload.slice(sep + 1)

      // "?" is a read request. Answering it would hand the remote host
      // whatever is on the local clipboard, for the cost of one escape
      // sequence in any file that gets cat'd. Never answered.
      if (data === '?') return true

      if (data.length > MAX_CLIPBOARD_B64) {
        this.onNotice?.('clipboard write ignored: too large')
        return true
      }

      let text
      try {
        text = new TextDecoder().decode(Uint8Array.from(atob(data), (c) => c.charCodeAt(0)))
      } catch {
        return true // malformed base64; not ours to repair
      }
      if (!text) return true

      writeClipboard(text).then((ok) => {
        if (ok) {
          this.onNotice?.(`copied ${text.length} character${text.length === 1 ? '' : 's'}`)
          return
        }
        // Safari will not let anything touch the clipboard without a recent
        // user gesture, and this text arrived from the network — a turn later
        // than whatever keystroke caused it, and often with no keystroke at all
        // behind it. There is no way to complete the write here, so the text is
        // held for a gesture that can: copying in tmux copy-mode used to look
        // like it worked and then paste nothing.
        //
        // Falling back to the notice matters: a refusal nobody is listening for
        // must still say so, rather than becoming the silence this replaces.
        if (this.onClipboardPending) this.onClipboardPending(text)
        else this.onNotice?.('the browser refused the clipboard write', 'error')
      })
      return true
    })
  }

  // installTouchGestures gives the terminal the two touch gestures a phone has
  // no other way to reach: swipe to scroll, and touch-and-hold to paste.
  //
  // xterm has no touch support at all. A SWIPE does nothing on its own, so the
  // scrollback — and a full-screen app's own scroll, like Claude Code's
  // conversation — is unreachable; a vertical drag is turned into wheel events
  // on xterm's own element, which lets xterm do the mode-right thing without us
  // knowing the mode (scroll the local scrollback on the normal buffer, forward
  // the wheel to the far side on the alternate buffer — mouse-wheel sequences
  // when the program turned on mouse reporting like Claude Code, cursor keys
  // when it did not). Content follows the finger: dragging DOWN reveals older
  // lines above.
  //
  // A TOUCH-AND-HOLD pastes the system clipboard. The terminal is a canvas, so
  // iOS shows no native paste callout on a long press — there is no input field
  // under the finger. So we detect the hold ourselves and read the clipboard on
  // release (a genuine touch gesture, which is what Safari requires before it
  // will hand over the clipboard), then paste through xterm so bracketed-paste
  // mode is honoured — a multi-line paste arrives as one paste, not a burst of
  // Enters that would fire a half-finished prompt into Claude Code.
  installTouchGestures() {
    const MOVE = 10 // px of travel before a hold is reclassified as a swipe
    const HOLD_MS = 500 // touch-and-hold this long, then release, to paste

    let tracking = false
    let moved = false
    let heldLongEnough = false
    let startX = 0
    let startY = 0
    let lastY = 0
    let holdTimer = 0

    // The element xterm listens on for wheel is an ancestor of the screen, so
    // dispatching on the screen bubbles up to it. Resolved per-gesture because
    // the renderer (WebGL vs canvas) decides which elements exist.
    const wheelTarget = () =>
      this.el.querySelector('.xterm-screen') || this.el.querySelector('.xterm') || this.el

    const clearHold = () => {
      clearTimeout(holdTimer)
      holdTimer = 0
    }

    this.el.addEventListener(
      'touchstart',
      (e) => {
        // One finger only: two-finger gestures are the browser's (pinch-zoom).
        if (e.touches.length !== 1) {
          tracking = false
          heldLongEnough = false
          clearHold()
          return
        }
        tracking = true
        moved = false
        heldLongEnough = false
        const t = e.touches[0]
        startX = t.clientX
        startY = t.clientY
        lastY = t.clientY
        clearHold()
        holdTimer = setTimeout(() => {
          // The finger has stayed put long enough to be a hold, not a tap or a
          // swipe. Arm the paste and nudge the phone so the hold is felt (iOS
          // ignores vibrate; Android honours it).
          if (!moved && tracking) {
            heldLongEnough = true
            navigator.vibrate?.(10)
          }
        }, HOLD_MS)
      },
      { passive: true },
    )

    this.el.addEventListener(
      'touchmove',
      (e) => {
        if (!tracking || e.touches.length !== 1) return
        const t = e.touches[0]

        // Past the dead zone this is a swipe: cancel the pending paste and
        // start scrolling. The small zone keeps a hold from scrolling a hair.
        if (!moved && Math.hypot(t.clientX - startX, t.clientY - startY) > MOVE) {
          moved = true
          heldLongEnough = false
          clearHold()
        }
        if (!moved) return

        const dy = lastY - t.clientY
        lastY = t.clientY
        if (!dy) return

        wheelTarget().dispatchEvent(
          new WheelEvent('wheel', { deltaY: dy, deltaMode: 0, bubbles: true, cancelable: true }),
        )
        // Stop the page itself from scrolling or rubber-banding under the app.
        if (e.cancelable) e.preventDefault()
      },
      { passive: false },
    )

    this.el.addEventListener(
      'touchend',
      (e) => {
        clearHold()
        if (heldLongEnough && !moved) {
          // Suppress the tap-to-focus that would otherwise fire; this gesture
          // is a paste, not a click into the terminal.
          if (e.cancelable) e.preventDefault()
          this.pasteFromClipboard()
        }
        tracking = false
        heldLongEnough = false
      },
      { passive: false },
    )

    this.el.addEventListener(
      'touchcancel',
      () => {
        clearHold()
        tracking = false
        heldLongEnough = false
      },
      { passive: true },
    )
  }

  // pasteFromClipboard reads the system clipboard and pastes it into the
  // terminal. Called from the touch-and-hold gesture; on iOS Safari shows its
  // own "Paste" confirmation, which is the same affordance a real input field
  // gets. term.paste (not send) so bracketed-paste mode is applied when the far
  // side asked for it.
  async pasteFromClipboard() {
    if (this.disposed) return
    if (!navigator.clipboard?.readText) {
      this.onNotice?.('this browser will not share the clipboard', 'error')
      return
    }
    let text
    try {
      text = await navigator.clipboard.readText()
    } catch {
      // The usual cause on iOS is the read being denied the clipboard — no
      // recent gesture, or the user dismissed the Paste prompt.
      this.onNotice?.('could not read the clipboard', 'error')
      return
    }
    if (!text) {
      this.onNotice?.('the clipboard is empty')
      return
    }
    // term.paste, not send: it applies bracketed-paste mode when the far side
    // asked for it, so a multi-line paste is one paste, not a run of Enters.
    this.term.paste(text)
    this.term.focus()
    // Say it landed — a paste that silently did nothing is the whole complaint,
    // and this also tells apart "the read failed" from "it pasted but the app
    // did not show it".
    this.onNotice?.(`pasted ${text.length} character${text.length === 1 ? '' : 's'}`)
  }

  // --- sizing ---

  // fitIfMeasurable is the guard against the hidden-terminal trap. A terminal
  // that is not measurable yields nonsense dimensions (commonly 0, 1, or NaN),
  // and propagating those to the remote PTY destroys its geometry.
  fitIfMeasurable() {
    if (this.disposed) return false
    let dims
    try {
      dims = this.fitAddon.proposeDimensions()
    } catch {
      return false
    }
    if (!dims) return false
    const { cols, rows } = dims
    if (!Number.isFinite(cols) || !Number.isFinite(rows)) return false
    if (cols < 2 || rows < 2) return false

    if (cols !== this.term.cols || rows !== this.term.rows) {
      this.fitAddon.fit()
    }
    return true
  }

  sendResize() {
    if (this.disposed) return
    const { cols, rows } = this.term
    if (!Number.isFinite(cols) || !Number.isFinite(rows) || cols < 2 || rows < 2) return
    if (cols === this.sentCols && rows === this.sentRows) return

    this.sentCols = cols
    this.sentRows = rows
    this.sendJSON({ type: 'resize', cols, rows })
  }

  // sendResizeSoon coalesces resizes. Dragging a grid gutter changes geometry
  // every frame; sending each one would flood the remote PTY with window-change
  // requests and make full-screen programs redraw continuously.
  sendResizeSoon() {
    clearTimeout(this.resizeTimer)
    this.resizeTimer = setTimeout(() => this.sendResize(), 80)
  }

  // onLayoutChange fits locally. In tab mode only the active tab's geometry is
  // authoritative, since hidden tabs are not the ones the PTY should track. In
  // grid mode every panel is visible at its own size, so each one syncs.
  onLayoutChange() {
    const measured = this.fitIfMeasurable()
    if (measured && (this.active || this.syncAlways)) this.sendResizeSoon()
  }

  // --- what the program is doing ---
  //
  // Worked out from the output stream alone, so it works for any agent or
  // program: sustained output is "working" (an agent's spinner counts), the
  // echo of typing does not, nor does the scrollback replayed on connect. A
  // working stretch of 3 s or more that ends while nobody is looking at this
  // tab leaves it "done": the agent finished, or stopped to ask something,
  // and it is your turn.

  noteOutput(now) {
    if (now - this.connectedAt < 2000) return // scrollback replay
    if (now - this.lastInputAt < 800) return // echo of what was typed
    const b = Math.floor(now / 400)
    if (this.outBuckets[this.outBuckets.length - 1] !== b) this.outBuckets.push(b)
    if (this.outBuckets.length > 12) this.outBuckets.shift()
    sceneEvent({ type: 'activity', id: this.id, tab: this.session.name || this.session.host })
  }

  // tick is called twice a second by the tab manager.
  tick(now, watched) {
    const recent = this.outBuckets.filter((b) => b >= Math.floor((now - 2400) / 400))
    this.outBuckets = recent
    if (recent.length >= 3) {
      if (this.activity !== 'working') {
        this.workStart = now
        this.setActivity('working')
      }
      this.lastWorkAt = now
      return
    }
    if (this.activity === 'working' && !this.explicitWorking && now - this.lastOutputAt > 2500) {
      const long = this.lastWorkAt - this.workStart >= 3000
      if (long && !watched) {
        this.setActivity('done')
        sceneEvent({ type: 'done', id: this.id, tab: this.session.name || this.session.host })
      } else this.setActivity('idle')
    }
  }

  // applyAgentStatus takes what the program says about itself (papaya-status,
  // or OSC 7501 when there is no tmux in between). It outranks the guess from
  // the output stream: an explicit "working" holds through quiet stretches
  // until the program says otherwise.
  applyAgentStatus(state, msg) {
    this.agentMsg = msg || ''
    if (state === 'working') {
      this.explicitWorking = true
      this.workStart = this.workStart || Date.now()
      this.setActivity('working')
    } else if (state === 'done') {
      this.explicitWorking = false
      const watched = this.active && document.visibilityState === 'visible'
      this.setActivity(watched ? 'idle' : 'done')
      if (!watched) sceneEvent({ type: 'done', id: this.id, tab: this.session.name || this.session.host })
    } else if (state === 'needs-you') {
      this.explicitWorking = false
      this.setActivity('idle')
      if (!this.focused || document.visibilityState !== 'visible') {
        this.attention = true
        this.onAttention?.(this, true)
      }
      sceneEvent({ type: 'bell', id: this.id, tab: this.session.name || this.session.host })
    } else {
      this.explicitWorking = false
      this.setActivity('idle')
    }
  }

  // OSC 7501, the open Program Status Protocol herdr published:
  //   ESC ] 7501 ; state=working:app=claude ESC \
  // tmux drops unknown OSC sequences, so this only arrives for tabs without
  // tmux; papaya-status is the route that works through it.
  installStatusProtocol() {
    this.term.parser?.registerOscHandler?.(7501, (data) => {
      const kv = {}
      for (const part of String(data).split(/[:;]/)) {
        const [k, v] = part.split('=')
        if (k) kv[k.trim()] = (v || '').trim()
      }
      const map = { working: 'working', busy: 'working', done: 'done', finished: 'done', blocked: 'needs-you', waiting: 'needs-you', input: 'needs-you', idle: 'idle' }
      const state = map[kv.state]
      if (state) this.applyAgentStatus(state, kv.msg || kv.app || '')
      return true
    })
  }

  setActivity(state) {
    if (this.activity === state) return
    this.activity = state
    this.onActivity?.(this, state)
  }

  // isBusy reports recent output, used for the "still working" indicator.
  get busy() {
    return Date.now() - this.lastOutputAt < 1500
  }

  get focused() {
    return this.active
  }

  // clearAttention is called when the user looks at this session.
  clearAttention() {
    if (!this.attention) return
    this.attention = false
    this.onAttention?.(this, false)
    fetch(`/api/sessions/${this.id}/seen`, { method: 'POST' }).catch(() => {
      // Best effort: the badge is already cleared locally.
    })
  }

  activate() {
    this.active = true
    this.el.dataset.active = 'true'
    this.clearAttention()
    if (this.activity === 'done') this.setActivity('idle')
    // Now that the element is on-screen, measurement is valid again; re-fit and
    // sync the remote to whatever the window did while this tab was hidden.
    this.fitIfMeasurable()
    this.sendResize()
    this.term.focus()
  }

  deactivate() {
    this.active = false
    this.el.dataset.active = 'false'
  }

  // --- connection ---

  connect() {
    if (this.disposed) return

    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
    const url = `${proto}//${location.host}/ws?session=${encodeURIComponent(this.id)}`

    let ws
    try {
      ws = new WebSocket(url)
    } catch {
      this.scheduleReconnect()
      return
    }

    ws.binaryType = 'arraybuffer'
    this.ws = ws

    ws.onopen = () => {
      this.connectedAt = Date.now()
      this.reconnectAttempt = 0
      this.everOpened = true
      reachability.set(true)
      this.setState('connecting')
      // A new session on the far side knows nothing about our geometry, so the
      // "already sent" guard must not suppress the first resize.
      this.sentCols = 0
      this.sentRows = 0
      // The server needs our real geometry before it starts streaming.
      this.fitIfMeasurable()
      this.sendResize()
    }

    ws.onmessage = (ev) => {
      if (typeof ev.data === 'string') {
        this.handleEvent(ev.data)
        return
      }
      // Binary frames are raw PTY output; write them straight through.
      this.lastOutputAt = Date.now()
      this.noteOutput(this.lastOutputAt)
      this.term.write(new Uint8Array(ev.data))
    }

    ws.onclose = () => {
      this.ws = null
      if (this.disposed) return
      if (this.state !== 'closed') {
        this.setState('detached')
        this.handleDisconnect()
      }
    }

    ws.onerror = () => {
      // onclose always follows; reconnect is handled there.
    }
  }

  // handleDisconnect works out *why* the socket closed before deciding whether
  // reconnecting can ever work.
  //
  // The browser will not tell us: a handshake rejected with 404 and a server
  // that cannot be reached at all both surface as a bare close event. So ask
  // the server directly, and pick the response that matches the cause:
  //
  //   unreachable          the tailnet is down, usually Tailscale switched off.
  //                        Keep retrying — it comes back — while the banner
  //                        explains what to do about it.
  //   reachable, no session the server has forgotten this session; its list is
  //                        held in memory and does not survive a restart.
  //                        Retrying can never succeed, so stop and say so.
  //
  // Without this the tab retried forever on a 10s ceiling, showing nothing but
  // "detached" — indistinguishable from a hung terminal, and the reason a
  // stale tab once sat there for days making a request every 11 seconds.
  async handleDisconnect() {
    if (this.disposed || this.state === 'closed') return

    // A session that was working a moment ago gets one immediate retry before
    // any diagnosis: a dropped socket is usually a blip, and a round trip to
    // /healthz would only delay the reconnect that fixes it.
    if (this.everOpened && this.reconnectAttempt === 0) {
      this.scheduleReconnect()
      return
    }

    const up = await reachability.check()
    if (this.disposed || this.state === 'closed') return

    if (!up || (await this.stillExists())) {
      this.scheduleReconnect()
      return
    }

    if (this.disposed || this.state === 'closed') return
    this.setState('closed', 'session ended — Papaya restarted and does not have it any more')
    this.onNotice?.('That session is gone. Press ⌘T to start a new one.', 'error')
  }

  // stillExists asks whether the server still knows this session.
  async stillExists() {
    try {
      const sessions = await api.listSessions()
      return sessions.some((s) => s.id === this.id)
    } catch {
      // Reachable a moment ago but not answering now. Treat that as transient
      // rather than declaring a live session dead on one bad response.
      return true
    }
  }

  // reconnectNow abandons the backoff and tries at once.
  //
  // The backoff climbs to a ten second ceiling, which is right for a socket
  // that dropped on its own but wrong the moment something tells us the world
  // changed — a laptop waking, the tailnet returning. Waiting out the rest of
  // an interval then looks like the app is broken.
  reconnectNow() {
    if (this.disposed || this.state === 'closed') return

    clearTimeout(this.reconnectTimer)
    this.reconnectTimer = null
    this.reconnectAttempt = 0

    const live = this.ws?.readyState
    if (live === WebSocket.OPEN || live === WebSocket.CONNECTING) return
    this.connect()
  }

  // wake is called when the page comes back from the background.
  //
  // A socket that is not open is reconnected straight away. One that still
  // reports OPEN is the trap: a phone that suspended the app usually leaves the
  // socket looking open while the far side has long since dropped it, and the
  // browser only finds out when TCP gives up, which can take most of a minute.
  // Until then the tab shows a stale screen and swallows keystrokes. So ask the
  // server: a pong inside WAKE_PROBE_MS means the socket is fine and nothing
  // happens; silence means it is dead, and it is replaced at once.
  wake() {
    if (this.disposed || this.state === 'closed') return
    const ws = this.ws
    if (!ws || ws.readyState === WebSocket.CLOSED || ws.readyState === WebSocket.CLOSING) {
      this.reconnectNow()
      return
    }
    if (ws.readyState !== WebSocket.OPEN || this.probeTimer) return

    try {
      ws.send(JSON.stringify({ type: 'ping' }))
    } catch {
      this.replaceSocket()
      return
    }
    this.probeTimer = setTimeout(() => {
      this.probeTimer = null
      if (this.ws === ws) this.replaceSocket()
    }, WAKE_PROBE_MS)
  }

  // replaceSocket drops the current socket without waiting for it to close
  // (a dead one may take a long time to say so) and opens a new one. The
  // server replays the session's recent output on attach, so nothing is lost.
  replaceSocket() {
    if (this.disposed || this.state === 'closed') return
    clearTimeout(this.probeTimer)
    this.probeTimer = null
    const old = this.ws
    if (old) {
      old.onopen = old.onmessage = old.onclose = old.onerror = null
      try {
        old.close()
      } catch {
        // already gone
      }
    }
    this.ws = null
    clearTimeout(this.reconnectTimer)
    this.reconnectTimer = null
    this.reconnectAttempt = 0
    this.connect()
  }

  scheduleReconnect() {
    if (this.disposed || this.state === 'closed') return
    clearTimeout(this.reconnectTimer)

    const delay = Math.min(500 * 2 ** this.reconnectAttempt, 10000)
    this.reconnectAttempt += 1
    this.reconnectTimer = setTimeout(() => this.connect(), delay)
  }

  handleEvent(raw) {
    let ev
    try {
      ev = JSON.parse(raw)
    } catch {
      return
    }

    switch (ev.type) {
      case 'pong':
        // The answer to wake()'s probe: the socket is alive.
        clearTimeout(this.probeTimer)
        this.probeTimer = null
        break

      case 'status':
        // The workspace is only chosen once the shell starts, so this is where
        // a tab first learns the durable handle on its own work.
        if (ev.workspace && this.session.workspace !== ev.workspace) {
          this.session.workspace = ev.workspace
        }
        this.setState(ev.state, ev.msg)
        break

      case 'rename':
        if (ev.name && ev.name !== this.session.name) {
          this.session.name = ev.name
          this.onRename?.(this, ev.name)
        }
        break

      case 'agent':
        this.applyAgentStatus(ev.state, ev.msg)
        break

      case 'theme':
        // This tab's theme was changed, here or on another device.
        this.session.theme = ev.theme || ''
        setTabTheme(this.id, this.session.theme, { remote: false })
        break

      case 'bell':
        sceneEvent({ type: 'bell', id: this.id, tab: this.session.name || this.session.host })
        // With several panels streaming, this is what tells you which one
        // finished or is waiting on an answer.
        if (!this.focused) {
          this.attention = true
          this.onAttention?.(this, true)
        }
        break

      case 'upload':
        this.onNotice?.(`uploaded ${ev.name}`)
        break

      case 'error':
        this.onNotice?.(ev.msg, 'error')
        if (ev.fatal) this.setState('closed', ev.msg)
        break
    }
  }

  setState(state, msg) {
    if (!state || this.state === state) return
    this.state = state
    this.session.state = state
    this.onStateChange?.(this, state, msg)

    if (state === 'closed') {
      clearTimeout(this.reconnectTimer)
      // The reason matters when the tab closed on its own rather than because
      // the user asked: "session closed" alone reads like a bug.
      this.term.write(`\r\n\x1b[31m[${msg || 'session closed'}]\x1b[0m\r\n`)
    }
  }

  send(data) {
    if (this.ws?.readyState !== WebSocket.OPEN) return
    // Typing answers whatever the tab was waiting for, and its echo is not the
    // program working.
    this.lastInputAt = Date.now()
    if (this.activity === 'done') this.setActivity('idle')
    // Raw keystrokes as a binary frame: no encoding, no parsing server-side.
    this.ws.send(encoder.encode(data))
  }

  sendJSON(obj) {
    if (this.ws?.readyState !== WebSocket.OPEN) return
    this.ws.send(JSON.stringify(obj))
  }

  // --- naming ---

  handleTitle(title) {
    // A name the user typed must never be overridden by a shell title.
    if (this.userNamed || this.disposed) return

    const clean = String(title || '')
      .replace(/^\w+@\S+:\s*/, '')
      .trim()
    if (!clean || clean === this.session.name) return

    this.session.name = clean
    this.onRename?.(this, clean)
    api.patchSession(this.id, { name: clean, auto: true }).catch(() => {
      // Losing an auto-title is not worth surfacing to the user.
    })
  }

  // useWebgl loads or drops the WebGL renderer. The context-loss handler is
  // required: some browsers throw on loss, and without disposing we would
  // render nothing instead of falling back to the DOM renderer.
  useWebgl(on) {
    if (on && !this.webgl) {
      try {
        const webgl = new WebglAddon()
        webgl.onContextLoss(() => {
          webgl.dispose()
          if (this.webgl === webgl) this.webgl = null
          this.onNotice?.('WebGL context lost; using the DOM renderer')
        })
        this.term.loadAddon(webgl)
        this.webgl = webgl
      } catch {
        // No WebGL available. The DOM renderer is fine.
      }
    } else if (!on && this.webgl) {
      this.webgl.dispose()
      this.webgl = null
    }
  }

  markUserNamed(name) {
    this.userNamed = true
    this.session.name = name
  }

  // --- search ---

  findNext(q) {
    this.searchAddon.findNext(q)
  }

  findPrevious(q) {
    this.searchAddon.findPrevious(q)
  }

  clearSearch() {
    this.searchAddon.clearDecorations?.()
  }

  focus() {
    this.term.focus()
  }

  dispose() {
    this.disposed = true
    window.removeEventListener('papaya:theme', this.onTheme)
    clearTimeout(this.reconnectTimer)
    clearTimeout(this.resizeTimer)
    clearTimeout(this.probeTimer)
    if (this.ws) {
      this.ws.onclose = null
      this.ws.close()
      this.ws = null
    }
    this.term.dispose()
    this.el.remove()
  }
}
