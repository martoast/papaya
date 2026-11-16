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

const CHEV = (d) =>
  `<svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="${d}"/></svg>`

// One row, in the order a thumb reaches for them.
const KEYS = [
  { label: 'esc', seq: '\x1b', text: 'esc' },
  // Tab: completion in every shell and the way through agent menus; the soft
  // keyboard has none.
  { label: 'tab', seq: '\t', text: 'tab' },
  { label: '←', seq: '\x1b[D', repeat: true, html: CHEV('M15 6l-6 6 6 6') },
  { label: '↑', seq: '\x1b[A', repeat: true, html: CHEV('M6 15l6-6 6 6') },
  { label: '↓', seq: '\x1b[B', repeat: true, html: CHEV('M6 9l6 6 6-6') },
  { label: '→', seq: '\x1b[C', repeat: true, html: CHEV('M9 6l6 6-6 6') },
]

// Keys are plain inside the dock's glass; pressing one lights a lens under it.
const BTN = 'lg-dkey group-data-[open=false]:hidden'

const PASTE_ICON =
  '<svg viewBox="0 0 24 24" width="19" height="19" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="5" y="4.5" width="14" height="17" rx="2.5"/><path d="M9 4.5V4a1 1 0 011-1h4a1 1 0 011 1v.5M9 11h6M9 15h4"/></svg>'
const KEYBOARD_ICON =
  '<svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" aria-hidden="true"><rect x="2.5" y="6" width="19" height="12" rx="2.5"/><path d="M6 10h.01M9.5 10h.01M13 10h.01M16.5 10h.01M7.5 14h9"/></svg>'

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
    'lg-dkey lg-dkey-toggle'
  toggle.dataset.key = 'toggle'

  const setOpen = (on) => {
    open = on
    pad.dataset.open = String(on)
    toggle.innerHTML = on ? CHEV('M7 10l5 5 5-5') : KEYBOARD_ICON
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
    b.className = key.text ? `${BTN} lg-dkey-word` : BTN
    if (key.html) b.innerHTML = key.html
    else b.textContent = key.text
    b.setAttribute('aria-label', key.label === 'esc' ? 'Escape' : key.label === 'tab' ? 'Tab' : key.label)
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
    'lg-dkey lg-dkey-word group-data-[open=false]:hidden'
  paste.dataset.key = 'paste'
  paste.innerHTML = PASTE_ICON
  paste.setAttribute('aria-label', 'Paste')
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

    document.documentElement.classList.toggle('kb-open', hidden > 0 && !zoomed)
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

    const bottom = hidden > 0 ? `${hidden + 8}px` : 'calc(88px + env(safe-area-inset-bottom))'
    for (const f of floats) f.style.bottom = bottom
  }

  const schedule = () => {
    if (!raf) raf = requestAnimationFrame(apply)
  }
  vv.addEventListener('resize', schedule)
  vv.addEventListener('scroll', schedule)
  apply()
}
