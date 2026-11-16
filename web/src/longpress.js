// Press-and-hold, for actions that otherwise only have a double-click.
//
// Renaming a tab was reachable two ways, and neither existed on a phone: F2,
// which a Mac keyboard puts behind Fn and which nothing on screen mentions, and
// a double-click, which touch does not really have. So on the device most
// likely to be used one-handed, a tab could not be renamed at all.
//
// Pointer events rather than touch events: one path covers finger, pen and
// mouse, and the browser's own click still fires normally when the press is
// short, so this can be layered onto an element that already does something.

const HOLD_MS = 500

// Enough slack for a resting finger, tight enough that a scroll or a drag is
// not mistaken for a hold.
const MOVE_TOLERANCE_PX = 10

export function onLongPress(el, handler) {
  let timer = null
  let origin = null
  let fired = false

  const cancel = () => {
    clearTimeout(timer)
    timer = null
    origin = null
  }

  el.addEventListener('pointerdown', (e) => {
    // Secondary buttons have their own menus; leave them alone.
    if (e.pointerType === 'mouse' && e.button !== 0) return

    fired = false
    origin = { x: e.clientX, y: e.clientY }
    timer = setTimeout(() => {
      fired = true
      cancel()
      handler(e)
    }, HOLD_MS)
  })

  el.addEventListener('pointermove', (e) => {
    if (!origin) return
    if (
      Math.abs(e.clientX - origin.x) > MOVE_TOLERANCE_PX ||
      Math.abs(e.clientY - origin.y) > MOVE_TOLERANCE_PX
    ) {
      cancel()
    }
  })

  for (const ev of ['pointerup', 'pointercancel', 'pointerleave']) {
    el.addEventListener(ev, cancel)
  }

  // The click that follows a completed hold would otherwise also activate the
  // element, so the rename input would open and immediately lose focus.
  el.addEventListener(
    'click',
    (e) => {
      if (!fired) return
      fired = false
      e.preventDefault()
      e.stopPropagation()
    },
    true,
  )

  // A hold on touch otherwise raises the selection callout over the label.
  el.addEventListener('contextmenu', (e) => {
    if (e.pointerType === 'mouse') return
    e.preventDefault()
  })
}
