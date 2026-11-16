// Floating touch key pad.
//
// A soft keyboard has no Esc and no arrows, and those five keys are most of
// what a terminal needs beyond text — Claude Code lives on Esc and ↑/↓. The
// key row this replaces offered fourteen keys the iPhone keyboard already
// types, sat flush with the bottom edge exactly where iOS listens for the
// keyboard gesture, and ended every press by focusing the terminal — and
// programmatic focus inside a tap gesture is permission for iOS to raise the
// keyboard. Reaching for an arrow kept opening the keyboard instead.
//
// This pad floats at the right edge, never takes or gives focus, and can be
// put away behind a small chip when it is in the way.

const PAD_STATE = 'papaya.keypad.v1'

const KEYS = [
  { label: 'esc', seq: '\x1b', cell: 'col-start-1 row-start-1' },
  { label: '↑', seq: '\x1b[A', repeat: true, cell: 'col-start-2 row-start-1' },
  { label: '←', seq: '\x1b[D', repeat: true, cell: 'col-start-1 row-start-2' },
  { label: '↓', seq: '\x1b[B', repeat: true, cell: 'col-start-2 row-start-2' },
  { label: '→', seq: '\x1b[C', repeat: true, cell: 'col-start-3 row-start-2' },
]

const BTN =
  'grid place-items-center size-11 rounded-lg border border-surface-3 bg-surface-2/85 ' +
  'backdrop-blur-sm text-sm text-text-hi active:bg-surface-3 ' +
  'group-data-[open=false]:hidden'

export function wireKeyPad(pad, getActiveTab) {
  let open = true
  try {
    open = localStorage.getItem(PAD_STATE) !== 'hidden'
  } catch {
    // Storage unavailable; the pad still toggles for this page.
  }

  // The way to put the pad away and the way back. Small and dim on purpose:
  // when the pad is hidden this chip is all that remains on screen.
  const toggle = document.createElement('button')
  toggle.className =
    'col-start-3 row-start-1 self-center justify-self-end grid place-items-center size-7 ' +
    'rounded-md border border-surface-3 bg-surface-1/70 backdrop-blur-sm text-xs ' +
    'text-text-lo active:bg-surface-3'
  toggle.dataset.key = 'toggle'

  const setOpen = (on) => {
    open = on
    pad.dataset.open = String(on)
    toggle.textContent = on ? '×' : '⌨'
    toggle.setAttribute('aria-label', on ? 'Hide terminal keys' : 'Show terminal keys')
    try {
      localStorage.setItem(PAD_STATE, on ? 'shown' : 'hidden')
    } catch {
      // Not persisted; still hidden or shown for this page.
    }
  }

  toggle.addEventListener('pointerdown', (e) => {
    e.preventDefault()
    setOpen(!open)
  })
  pad.appendChild(toggle)

  let holdTimer = 0
  let repeatTimer = 0
  const stop = () => {
    clearTimeout(holdTimer)
    clearInterval(repeatTimer)
  }

  for (const key of KEYS) {
    const b = document.createElement('button')
    b.className = `${BTN} ${key.cell}`
    b.textContent = key.label
    b.dataset.key = key.label

    // pointerdown, not click: it fires before focus can move. preventDefault
    // keeps focus exactly where it is — stealing it would close an open
    // keyboard, and explicitly focusing the terminal here would open one.
    // Nothing about sending bytes down the socket needs focus anyway.
    b.addEventListener('pointerdown', (e) => {
      e.preventDefault()
      getActiveTab()?.send(key.seq)

      // Holding an arrow repeats it, the way a hardware key would. Scrolling
      // history one tap at a time is misery.
      if (key.repeat) {
        stop()
        holdTimer = setTimeout(() => {
          repeatTimer = setInterval(() => getActiveTab()?.send(key.seq), 60)
        }, 350)
      }
    })
    for (const ev of ['pointerup', 'pointercancel', 'pointerleave']) {
      b.addEventListener(ev, stop)
    }

    pad.appendChild(b)
  }

  // Paste. The terminal is a canvas, so a long-press over it gets no native
  // paste callout; this is the reliable way in. A plain click, not pointerdown:
  // Safari hands over the clipboard most readily from a click, and unlike the
  // arrow keys a paste WANTS the focus it brings, so the soft keyboard's Return
  // is there to send the pasted prompt.
  const paste = document.createElement('button')
  paste.className =
    'col-start-1 col-span-3 row-start-3 h-9 w-full grid place-items-center rounded-lg ' +
    'border border-surface-3 bg-surface-2/85 backdrop-blur-sm text-xs text-text-hi ' +
    'active:bg-surface-3 group-data-[open=false]:hidden'
  paste.dataset.key = 'paste'
  paste.textContent = 'Paste'
  paste.addEventListener('click', () => {
    getActiveTab()?.pasteFromClipboard()
  })
  pad.appendChild(paste)

  setOpen(open)
}

// wireViewport pins the app to the VISIBLE viewport.
//
// Opening the iOS keyboard does not shrink the layout viewport — the keyboard
// covers the bottom of it, cursor included, and Safari "helps" by scrolling
// the page toward the focused element. Sizing the app to window.visualViewport
// makes the container genuinely smaller instead: the ResizeObservers fire,
// xterm refits, the PTY reflows, and the line being typed stays above the
// keyboard.
//
// Every floating touch control (the key pad, the attach button) rides just
// above the keys while the keyboard is open and sits well clear of the bottom
// edge when it is closed — the bottom edge is the keyboard trigger.
export function wireViewport(app) {
  const vv = window.visualViewport
  if (!vv) return

  const floats = document.querySelectorAll('.kb-float')

  let raf = 0
  const apply = () => {
    raf = 0

    // How much of the layout viewport's bottom is not actually visible — the
    // keyboard, for all practical purposes.
    const hidden = Math.max(0, Math.round(window.innerHeight - vv.height - vv.offsetTop))

    // A pinch zoom also shrinks the visual viewport, and reflowing every
    // remote PTY because someone zoomed in would be wrong twice over.
    const zoomed = Math.abs((vv.scale || 1) - 1) > 0.01

    if (hidden > 0 && !zoomed) {
      app.style.height = `${Math.round(vv.height)}px`
      app.style.transform = `translateY(${Math.round(vv.offsetTop)}px)`
      // Undo Safari's own scroll toward the focused textarea; the app is
      // already pinned inside the visible area.
      window.scrollTo(0, 0)
    } else {
      app.style.height = ''
      app.style.transform = ''
    }

    const bottom = `${hidden + (hidden > 0 ? 8 : 88)}px`
    for (const f of floats) f.style.bottom = bottom
  }

  const schedule = () => {
    if (!raf) raf = requestAnimationFrame(apply)
  }
  vv.addEventListener('resize', schedule)
  vv.addEventListener('scroll', schedule)
  apply()
}
