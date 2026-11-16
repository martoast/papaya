import * as L from './layout.js'
import { onLongPress } from './longpress.js'

// Matches the platform double-click window closely enough that a deliberate
// double-click is caught and two separate clicks are not.
const DOUBLE_CLICK_MS = 400

const GUTTER = 6 // px, matches the CSS

// GridView renders sessions as simultaneously visible, resizable panels.
//
// Every terminal is live at once: nothing is hidden, so several agent sessions
// can be watched streaming side by side. The terminal elements are *moved* here
// from the tab stack rather than recreated — recreating one would drop its
// scrollback and reconnect its socket.
export class GridView {
  constructor({ root, getTab, onFocus, onClose, onNotice }) {
    this.root = root
    this.getTab = getTab
    this.onFocus = onFocus
    this.onClose = onClose
    this.onNotice = onNotice

    this.tree = null
    this.focusedId = null
    this.panels = new Map() // sessionId -> { el, body, header }
    this.active = false

    this.resizeObserver = new ResizeObserver(() => this.scheduleFit())
    this.fitHandle = 0
    this.dragging = null
    // Invalidates a deferred focus that has not run yet. See focus().
    this.focusToken = 0
    // Pairs clicks on a panel name into a rename. See createPanel.
    this.lastNameClick = null
  }

  // --- lifecycle ---

  show(sessionIds, saved) {
    this.active = true
    this.root.classList.remove('hidden')

    this.tree = saved ? L.reconcile(saved, sessionIds) : L.grid(sessionIds)
    if (!this.focusedId || !sessionIds.includes(this.focusedId)) {
      this.focusedId = sessionIds[0] ?? null
    }

    this.render()
    this.resizeObserver.observe(this.root)
  }

  hide() {
    this.active = false
    this.resizeObserver.disconnect()
    this.root.classList.add('hidden')

    // Hand every terminal back before tearing the panels down, or they would be
    // removed from the document along with their panel.
    for (const id of this.panels.keys()) this.releaseTerminal(id)
    this.panels.clear()
    this.root.replaceChildren()
  }

  get layout() {
    return this.tree
  }

  // --- session set changes ---

  addSession(id) {
    this.tree = L.add(this.tree, id, { target: this.focusedId })
    this.focusedId = id
    if (this.active) this.render()
  }

  removeSession(id) {
    this.tree = L.remove(this.tree, id)
    this.panels.delete(id)

    if (this.focusedId === id) {
      this.focusedId = L.ids(this.tree)[0] ?? null
    }
    if (this.active) this.render()
  }

  focus(id) {
    if (!id) return

    const changed = this.focusedId !== id
    this.focusedId = id
    this.paintFocus()

    // Focus on the next frame. This runs from a capture-phase pointerdown, and
    // the browser's default handling of that same event moves focus to the
    // panel (a plain div, so effectively to <body>) afterwards — focusing now
    // would immediately be undone and keystrokes would go nowhere.
    //
    // The token is what lets that be called off. Anything that opens its own
    // input between the pointerdown and the frame — renaming, from the click
    // that follows — would otherwise have focus pulled back to the terminal a
    // frame later, and an input that loses focus commits and closes itself.
    const token = ++this.focusToken
    requestAnimationFrame(() => {
      if (this.focusToken !== token || this.focusedId !== id) return
      const tab = this.getTab(id)
      tab?.focus()
      // Looking at a panel is what dismisses its badge.
      tab?.clearAttention()
    })

    if (changed) this.onFocus?.(id)
  }

  // cancelPendingFocus calls off a deferred terminal focus, for when something
  // else has legitimately taken the keyboard in the meantime.
  cancelPendingFocus() {
    this.focusToken++
  }

  balance() {
    this.tree = L.balance(this.tree)
    this.render()
  }

  // --- rendering ---

  render() {
    if (!this.active) return

    const live = new Set(L.ids(this.tree))

    // Detach terminals from panels that are about to be rebuilt, so moving them
    // does not destroy them.
    for (const id of this.panels.keys()) this.releaseTerminal(id)
    this.panels.clear()

    this.root.replaceChildren()
    if (this.tree) {
      this.root.appendChild(this.renderNode(this.tree, []))
    }

    for (const id of live) this.adoptTerminal(id)

    this.paintFocus()
    this.scheduleFit()
  }

  renderNode(node, path) {
    if (L.isLeaf(node)) return this.renderPanel(node.id)

    const wrap = document.createElement('div')
    wrap.className = 'flex min-h-0 min-w-0 h-full w-full'
    wrap.style.flexDirection = node.dir === 'row' ? 'row' : 'column'

    const a = this.renderNode(node.a, [...path, 'a'])
    const b = this.renderNode(node.b, [...path, 'b'])

    const pct = node.ratio * 100
    a.style.flex = `0 0 calc(${pct}% - ${GUTTER / 2}px)`
    b.style.flex = `1 1 0%`
    a.style.minWidth = '0'
    a.style.minHeight = '0'
    b.style.minWidth = '0'
    b.style.minHeight = '0'

    wrap.append(a, this.renderGutter(node, path), b)
    return wrap
  }

  renderGutter(node, path) {
    const g = document.createElement('div')
    g.dataset.gutter = node.dir
    g.className =
      node.dir === 'row'
        ? 'shrink-0 cursor-col-resize bg-surface-0 hover:bg-accent/60 transition-colors'
        : 'shrink-0 cursor-row-resize bg-surface-0 hover:bg-accent/60 transition-colors'
    g.style[node.dir === 'row' ? 'width' : 'height'] = `${GUTTER}px`

    g.addEventListener('pointerdown', (e) => this.beginGutterDrag(e, node, path))
    return g
  }

  renderPanel(id) {
    const tab = this.getTab(id)

    const panel = document.createElement('div')
    panel.dataset.panelId = id
    panel.className =
      'lg-panel group/panel relative flex min-h-0 min-w-0 flex-col overflow-hidden rounded-2xl ' +
      'border border-surface-3 bg-term-bg transition-[border-color,box-shadow] ' +
      'data-[focused=true]:border-accent data-[focused=true]:shadow-[0_0_0_1px_var(--color-accent)] ' +
      // A panel asking for attention is unmissable across a full grid.
      'data-[attention=true]:border-state-warn ' +
      'data-[attention=true]:shadow-[0_0_0_2px_var(--color-state-warn)]'
    panel.dataset.focused = 'false'
    panel.dataset.attention = String(Boolean(tab?.attention))
    panel.dataset.activity = tab?.activity || 'idle'

    // Header doubles as the drag handle for rearranging.
    const header = document.createElement('div')
    header.className =
      'lg-panel-head flex h-8 shrink-0 items-center gap-2 px-3 ' +
      'cursor-grab active:cursor-grabbing select-none'
    header.draggable = true

    const dot = document.createElement('span')
    dot.className =
      'size-1.5 shrink-0 rounded-full ' +
      'data-[state=connected]:bg-state-ok ' +
      'data-[state=connecting]:bg-state-warn data-[state=connecting]:animate-pulse ' +
      'data-[state=detached]:bg-state-warn ' +
      'data-[state=closed]:bg-state-err'
    dot.dataset.state = tab?.state ?? 'connecting'

    const name = document.createElement('span')
    name.className = 'panel-name flex-1 truncate text-xs text-text-lo'
    name.textContent = tab?.session.name || tab?.session.host || id.slice(0, 8)
    name.title = 'Double-click or hold to rename'
    // The grid had no way to rename at all: the tab bar's gestures are on tab
    // elements, and someone working in the grid never touches those. Renaming
    // opens the input in the tab bar, which stays visible in both views.
    //
    // The pairing is tracked on the view rather than left to the browser's own
    // dblclick, because the first click focuses the panel and a focus change
    // re-renders the tree — replacing this very element, so the second click
    // lands on a fresh node and no dblclick is ever produced. State that lives
    // on the instance survives that.
    name.addEventListener('click', (e) => {
      const now = Date.now()
      const prev = this.lastNameClick
      this.lastNameClick = { id, at: now }
      if (prev && prev.id === id && now - prev.at < DOUBLE_CLICK_MS) {
        this.lastNameClick = null
        e.stopPropagation()
        this.cancelPendingFocus()
        this.onRename?.(id)
      }
    })
    onLongPress(name, () => {
      this.focus(id)
      this.cancelPendingFocus()
      this.onRename?.(id)
    })

    const close = document.createElement('button')
    close.className =
      'shrink-0 size-4 rounded text-xs text-text-lo opacity-0 group-hover/panel:opacity-100 ' +
      'hover:bg-surface-3 hover:text-state-err flex items-center justify-center transition-opacity'
    close.setAttribute('aria-label', 'Close panel')
    close.textContent = '×'
    close.addEventListener('click', (e) => {
      e.stopPropagation()
      this.onClose?.(id)
    })

    header.append(dot, name, close)

    const body = document.createElement('div')
    body.className = 'relative min-h-0 flex-1'

    panel.append(header, body)

    // Clicking anywhere in the panel focuses it, which is what voice input and
    // drag-and-drop then target.
    //
    // Two listeners on purpose: pointerdown (capture) claims focus immediately
    // so the highlight and voice target update on press, and click (bubble)
    // runs after the browser's default focus handling has finished, so the
    // terminal keeps keyboard focus rather than losing it to the panel div.
    panel.addEventListener('pointerdown', () => this.focus(id), true)
    panel.addEventListener('click', () => {
      if (this.focusedId === id) this.getTab(id)?.focus()
    })

    this.wirePanelDrag(panel, header, id)
    this.panels.set(id, { el: panel, body, header, dot, name })
    return panel
  }

  // --- terminal adoption ---

  adoptTerminal(id) {
    const tab = this.getTab(id)
    const panel = this.panels.get(id)
    if (!tab || !panel) return

    // Moving the element preserves the xterm instance, its scrollback, and its
    // WebSocket. In grid mode every terminal is visible, so it must also keep
    // its own geometry in sync rather than only when focused.
    tab.syncAlways = true
    tab.el.dataset.active = 'true'
    panel.body.appendChild(tab.el)
  }

  releaseTerminal(id) {
    const tab = this.getTab(id)
    if (!tab) return
    tab.syncAlways = false
    if (tab.el.parentElement) tab.el.remove()
  }

  paintFocus() {
    for (const [id, panel] of this.panels) {
      panel.el.dataset.focused = String(id === this.focusedId)
    }
  }

  // updateSession refreshes a panel's header without rebuilding the tree.
  updateSession(id, { name, state, attention, activity } = {}) {
    const panel = this.panels.get(id)
    if (!panel) return
    if (name) panel.name.textContent = name
    if (state) panel.dot.dataset.state = state
    if (attention !== undefined) panel.el.dataset.attention = String(attention)
    if (activity !== undefined) panel.el.dataset.activity = activity
  }

  // --- sizing ---

  scheduleFit() {
    cancelAnimationFrame(this.fitHandle)
    this.fitHandle = requestAnimationFrame(() => {
      for (const id of this.panels.keys()) this.getTab(id)?.onLayoutChange()
    })
  }

  // --- gutter dragging ---

  beginGutterDrag(e, node, path) {
    e.preventDefault()

    const gutter = e.currentTarget
    const container = gutter.parentElement
    if (!container) return

    const rect = container.getBoundingClientRect()
    const horizontal = node.dir === 'row'
    const total = horizontal ? rect.width : rect.height
    if (total <= 0) return

    gutter.setPointerCapture(e.pointerId)
    document.body.classList.add(horizontal ? 'cursor-col-resize' : 'cursor-row-resize')

    const onMove = (ev) => {
      const pos = horizontal ? ev.clientX - rect.left : ev.clientY - rect.top
      this.tree = L.setRatio(this.tree, path, pos / total)
      this.applyRatios()
      // Fit locally on every frame; the remote resize is throttled by the
      // terminal itself so a drag does not flood the PTY with WindowChange.
      this.scheduleFit()
    }

    const onUp = () => {
      gutter.releasePointerCapture?.(e.pointerId)
      document.body.classList.remove('cursor-col-resize', 'cursor-row-resize')
      gutter.removeEventListener('pointermove', onMove)
      gutter.removeEventListener('pointerup', onUp)
      gutter.removeEventListener('pointercancel', onUp)
      this.onLayoutSettled?.()
      this.scheduleFit()
    }

    gutter.addEventListener('pointermove', onMove)
    gutter.addEventListener('pointerup', onUp)
    gutter.addEventListener('pointercancel', onUp)
  }

  // applyRatios updates flex bases in place, which is far cheaper than
  // re-rendering the tree on every pointer move.
  applyRatios() {
    const walk = (node, el) => {
      if (!node || L.isLeaf(node)) return
      const [a, , b] = el.children
      if (!a || !b) return
      a.style.flex = `0 0 calc(${node.ratio * 100}% - ${GUTTER / 2}px)`
      walk(node.a, a)
      walk(node.b, b)
    }
    walk(this.tree, this.root.firstElementChild)
  }

  // --- rearranging ---

  wirePanelDrag(panel, header, id) {
    header.addEventListener('dragstart', (e) => {
      e.dataTransfer.effectAllowed = 'move'
      // A payload is required for the drop to fire in some browsers.
      e.dataTransfer.setData('text/plain', id)
      this.dragging = id
      panel.dataset.dragging = 'true'
    })

    header.addEventListener('dragend', () => {
      this.dragging = null
      delete panel.dataset.dragging
      for (const p of this.panels.values()) delete p.el.dataset.dropTarget
    })

    panel.addEventListener('dragover', (e) => {
      if (!this.dragging || this.dragging === id) return
      e.preventDefault()
      e.dataTransfer.dropEffect = 'move'
      panel.dataset.dropTarget = 'true'
    })

    panel.addEventListener('dragleave', (e) => {
      // Ignore crossings into the panel's own children.
      if (panel.contains(e.relatedTarget)) return
      delete panel.dataset.dropTarget
    })

    panel.addEventListener('drop', (e) => {
      if (!this.dragging || this.dragging === id) return
      e.preventDefault()
      e.stopPropagation()

      const from = this.dragging
      this.dragging = null
      delete panel.dataset.dropTarget

      this.tree = L.swap(this.tree, from, id)
      this.render()
      this.focus(from)
      this.onLayoutSettled?.()
    })
  }
}
