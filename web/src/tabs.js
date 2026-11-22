import { api } from './api.js'
import { TerminalTab } from './terminal.js'
import { sceneEvent, registerTab, forgetTab, setActiveTab } from './themes.js'

import { onLongPress } from './longpress.js'

export const MAX_TABS = 10

// Tabs are capsules inside the glass bar; the active one is a lens of
// brighter glass (glass.css .lg-tab). Layout rules stay here.
const TAB_CLASS =
  'lg-tab group relative flex items-center gap-2 min-w-0 flex-1 max-w-52 px-3 ' +
  'cursor-pointer text-text-lo ' +
  // On a phone the bar has room for one title. The active tab keeps its name
  // and takes whatever width the chips leave; every other tab collapses to a
  // centred status dot, still one tap to switch to.
  'max-sm:data-[active=false]:max-w-9 max-sm:data-[active=false]:px-0 ' +
  'max-sm:data-[active=false]:justify-center max-sm:data-[active=true]:max-w-none'

const DOT_CLASS =
  'size-1.5 shrink-0 rounded-full transition-colors ' +
  'data-[state=connected]:bg-state-ok ' +
  'data-[state=connecting]:bg-state-warn data-[state=connecting]:animate-pulse ' +
  'data-[state=detached]:bg-state-warn ' +
  'data-[state=closed]:bg-state-err'

export class TabManager {
  constructor({
    tabsEl,
    stackEl,
    newTabBtn,
    emptyStateEl,
    onNotice,
    onActiveChange,
    onSessionUpdate,
    onClipboardPending,
  }) {
    this.tabsEl = tabsEl
    this.stackEl = stackEl
    this.newTabBtn = newTabBtn
    this.emptyStateEl = emptyStateEl
    this.onNotice = onNotice
    this.onActiveChange = onActiveChange
    this.onSessionUpdate = onSessionUpdate
    this.onClipboardPending = onClipboardPending

    this.tabs = new Map() // id -> { tab, el, nameEl, dotEl }
    this.activeId = null
    // True while another view (the grid) holds the terminal elements.
    this.suspended = false

    // One observer for the whole stack. It fires for every terminal on a window
    // resize; each fits locally and only the active one syncs the remote PTY.
    this.resizeObserver = new ResizeObserver(() => {
      for (const { tab } of this.tabs.values()) tab.onLayoutChange()
    })
    this.resizeObserver.observe(this.stackEl)
  }

  get size() {
    return this.tabs.size
  }

  get active() {
    return this.activeId ? this.tabs.get(this.activeId)?.tab ?? null : null
  }

  // get returns one tab by session id.
  get(id) {
    return this.tabs.get(id)?.tab ?? null
  }

  // ids lists sessions in tab order.
  ids() {
    return [...this.tabs.keys()]
  }

  // all returns every open tab, for callers that need to inspect the set rather
  // than a single member.
  all() {
    return [...this.tabs.values()].map(({ tab }) => tab)
  }

  // setActiveId marks a session current without touching the DOM, for when
  // another view owns the terminals and has changed focus.
  setActiveId(id) {
    if (!this.tabs.has(id)) return
    this.activeId = id

    for (const [otherId, other] of this.tabs) {
      const isActive = otherId === id
      other.tab.active = isActive
      other.el.dataset.active = String(isActive)
      other.el.setAttribute('aria-selected', String(isActive))
    }
  }

  // suspend hands the terminals to another view. While suspended the manager
  // stops re-parenting or hiding them, so the grid can hold them without the
  // stack pulling them back.
  suspend() {
    this.suspended = true
    this.resizeObserver.disconnect()
  }

  // resume reclaims the terminals into the stack and restores tab behaviour.
  resume() {
    this.suspended = false
    for (const { tab } of this.tabs.values()) {
      tab.syncAlways = false
      if (!this.suspended) this.stackEl.appendChild(tab.el)
    }
    this.resizeObserver.observe(this.stackEl)

    if (this.activeId) this.activate(this.activeId)
    else {
      const first = [...this.tabs.keys()][0]
      if (first) this.activate(first)
    }
  }

  get atLimit() {
    return this.tabs.size >= MAX_TABS
  }

  // add wires a session into a terminal, a tab button, and the stack.
  add(session, { activate = true } = {}) {
    if (this.tabs.has(session.id)) return this.tabs.get(session.id).tab

    registerTab(session.id, session.theme)
    const tab = new TerminalTab(session, {
      onStateChange: (t, state) => this.updateState(t.id, state),
      onRename: (t, name) => this.updateName(t.id, name),
      onNotice: this.onNotice,
      onAttention: (t, on) => this.setAttention(t.id, on),
      onActivity: (t, state) => this.setActivity(t.id, state),
      onClipboardPending: this.onClipboardPending,
    })

    if (!this.suspended) this.stackEl.appendChild(tab.el)
    sceneEvent({ type: 'tab-open', id: session.id, tab: session.name || session.host, host: session.host })

    const el = document.createElement('div')
    el.className = TAB_CLASS
    el.dataset.tabId = session.id
    el.dataset.active = 'false'
    el.setAttribute('role', 'tab')
    el.setAttribute('aria-selected', 'false')

    const dot = document.createElement('span')
    dot.className = DOT_CLASS + ' tab-dot' 
    dot.dataset.state = session.state || 'connecting'

    const name = document.createElement('span')
    name.className = 'tab-name flex-1 truncate text-sm max-sm:group-data-[active=false]:hidden'
    name.textContent = session.name || session.host

    const close = document.createElement('button')
    close.className =
      'lg-tab-close shrink-0 size-5 rounded-full opacity-0 group-hover:opacity-100 group-data-[active=true]:opacity-60 ' +
      'flex items-center justify-center text-sm transition-opacity ' +
      'max-sm:group-data-[active=false]:hidden'
    close.setAttribute('aria-label', 'Close')
    close.textContent = '×'

    el.append(dot, name, close)
    if (session.owner) el.dataset.owner = session.owner
    el.title = tabTitle(session.name || session.host, where(session))

    el.addEventListener('click', (e) => {
      if (e.target === close) return
      this.activate(session.id)
    })
    name.addEventListener('dblclick', (e) => {
      e.stopPropagation()
      this.beginRename(session.id)
    })
    // Touch has no double-click and no F2, so without this a tab could not be
    // renamed at all from a phone.
    onLongPress(name, () => {
      this.activate(session.id)
      this.beginRename(session.id)
    })
    close.addEventListener('click', (e) => {
      e.stopPropagation()
      this.close(session.id)
    })

    this.tabsEl.appendChild(el)
    el.dataset.activity = 'idle'
    this.tabs.set(session.id, { tab, el, nameEl: name, dotEl: dot })
    this.startTicking()

    this.refreshChrome()
    if (activate) this.activate(session.id)

    // A terminal only measures correctly once it is laid out.
    requestAnimationFrame(() => tab.onLayoutChange())
    return tab
  }

  activate(id) {
    const entry = this.tabs.get(id)
    if (!entry) return
    sceneEvent({ type: 'focus', id, tab: entry.tab.session.name || entry.tab.session.host })
    // The page wears the theme of the tab in front.
    setActiveTab(id)

    // While the grid owns the terminals, only the bookkeeping applies; showing
    // and hiding is the grid's job.
    if (this.suspended) {
      this.setActiveId(id)
      this.onActiveChange?.(entry.tab)
      return
    }

    for (const [otherId, other] of this.tabs) {
      const isActive = otherId === id
      other.tab[isActive ? 'activate' : 'deactivate']()
      other.el.dataset.active = String(isActive)
      other.el.setAttribute('aria-selected', String(isActive))
    }

    this.activeId = id
    this.refreshChrome()
    this.onActiveChange?.(entry.tab)
  }

  activateIndex(i) {
    const ids = [...this.tabs.keys()]
    if (i >= 0 && i < ids.length) this.activate(ids[i])
  }

  activateLast() {
    const ids = [...this.tabs.keys()]
    if (ids.length) this.activate(ids[ids.length - 1])
  }

  cycle(delta) {
    const ids = [...this.tabs.keys()]
    if (!ids.length) return
    const cur = ids.indexOf(this.activeId)
    const next = (cur + delta + ids.length) % ids.length
    this.activate(ids[next])
  }

  async close(id) {
    if (!this.forget(id)) return

    try {
      await api.deleteSession(id)
    } catch {
      // The tab is already gone locally; a failed delete is reaped server-side.
    }
  }

  // forget drops a tab locally and tells the server nothing.
  //
  // This is for a session the server has already lost — after a restart, when
  // the id refers to nothing. Going through close() there would be worse than
  // pointless: the delete it sends kills the remote workspace, so the one time
  // the session list is wrong about a session being gone, the tab's actual work
  // would be destroyed rather than recovered. Removing locally can only ever
  // cost a reattach.
  //
  // Returns whether there was a tab to remove.
  forget(id) {
    const entry = this.tabs.get(id)
    if (!entry) return false

    entry.tab.dispose()
    entry.el.remove()
    this.tabs.delete(id)
    forgetTab(id)
    sceneEvent({ type: 'tab-close', id })
    this.refreshTitle()

    if (this.activeId === id) {
      this.activeId = null
      const next = [...this.tabs.keys()].pop()
      if (next) this.activate(next)
      else setActiveTab(null)
    }
    this.refreshChrome()
    return true
  }

  // startTicking runs the activity clock for every tab: twice a second each
  // tab works out whether its program is working, done or idle.
  startTicking() {
    if (this.ticker) return
    if (this.baseTitle === undefined) this.baseTitle = document.title
    this.ticker = setInterval(() => {
      const now = Date.now()
      const visible = document.visibilityState === 'visible'
      for (const [id, { tab }] of this.tabs) tab.tick(now, visible && (id === this.activeId || this.suspended))
    }, 500)
  }

  setActivity(id, state) {
    const entry = this.tabs.get(id)
    if (!entry) return
    entry.el.dataset.activity = state
    const msg = entry.tab.agentMsg ? ` · ${entry.tab.agentMsg}` : ''
    entry.el.title = tabTitle(entry.tab.session.name || entry.tab.session.host, where(entry.tab.session)) +
      (state === 'working' ? ' · working' : state === 'done' ? ' · finished, your turn' : '') + msg
    this.onSessionUpdate?.(id, { activity: state })
    this.refreshTitle()
  }

  // refreshTitle counts the tabs waiting on a person into the page title, so
  // a background browser tab or an installed app's switcher shows it.
  refreshTitle() {
    if (this.baseTitle === undefined) this.baseTitle = document.title
    let n = 0
    for (const { tab } of this.tabs.values()) if (tab.attention || tab.activity === 'done') n++
    document.title = n ? `(${n}) ${this.baseTitle}` : this.baseTitle
  }

  // setAttention marks a tab as wanting a human. The dot turns solid and pulses
  // so it reads at a glance across a full tab bar.
  setAttention(id, on) {
    const entry = this.tabs.get(id)
    if (!entry) return
    entry.el.dataset.attention = String(on)
    if (on && entry.tab.agentMsg) entry.el.title = `${tabTitle(entry.tab.session.name || entry.tab.session.host, where(entry.tab.session))} · needs you: ${entry.tab.agentMsg}`
    this.onSessionUpdate?.(id, { attention: on })
    this.refreshTitle()
  }

  updateState(id, state) {
    const entry = this.tabs.get(id)
    if (!entry) return
    entry.dotEl.dataset.state = state
    this.onSessionUpdate?.(id, { state })
    this.refreshChrome()
  }

  updateName(id, name) {
    const entry = this.tabs.get(id)
    if (!entry) return
    sceneEvent({ type: 'tab-rename', id, tab: name })
    entry.nameEl.textContent = name
    entry.el.title = tabTitle(name, where(entry.tab.session))
    this.onSessionUpdate?.(id, { name })
  }

  // beginRename swaps the label for an input in place.
  beginRename(id) {
    const entry = this.tabs.get(id)
    if (!entry) return

    const { tab, nameEl } = entry
    if (!nameEl.isConnected) return

    const input = document.createElement('input')
    input.value = tab.session.name || tab.session.host
    input.maxLength = 40
    input.className =
      'flex-1 min-w-0 bg-surface-0 text-text-hi text-sm px-1 rounded outline-none ring-1 ring-accent'
    input.dataset.renameInput = 'true'

    nameEl.replaceWith(input)
    input.focus()
    input.select()

    let done = false
    const commit = async (save) => {
      if (done) return
      done = true

      const previous = tab.session.name || tab.session.host
      const next = save ? input.value.trim() : previous
      input.replaceWith(nameEl)

      if (!save || !next || next === previous) return

      // updateName rather than writing the label here: it is also what tells the
      // grid, whose panel header carries the same name and would otherwise keep
      // showing the old one until something forced a re-render — which is what
      // renaming from a grid panel looked like before.
      this.updateName(id, next) // optimistic
      tab.markUserNamed(next)

      try {
        await api.patchSession(id, { name: next })
      } catch {
        tab.session.name = previous
        this.updateName(id, previous) // rollback
      }
    }

    input.addEventListener('keydown', (e) => {
      if (e.key === 'Enter') {
        e.preventDefault()
        commit(true)
      } else if (e.key === 'Escape') {
        e.preventDefault()
        commit(false)
      }
      // Without this the keystrokes reach the document handler and the
      // terminal, so typing a tab name would type into the shell.
      e.stopPropagation()
    })
    input.addEventListener('keypress', (e) => e.stopPropagation())
    input.addEventListener('keyup', (e) => e.stopPropagation())
    input.addEventListener('blur', () => commit(true))
  }

  renameActive() {
    if (this.activeId) this.beginRename(this.activeId)
  }

  refreshChrome() {
    const atLimit = this.atLimit
    this.newTabBtn.disabled = atLimit
    this.newTabBtn.title = atLimit ? 'Maximum 10 sessions' : 'New session (⌘T)'
    this.emptyStateEl.classList.toggle('hidden', this.tabs.size > 0)
  }

  disposeAll() {
    for (const { tab, el } of this.tabs.values()) {
      tab.dispose()
      el.remove()
    }
    this.tabs.clear()
    this.activeId = null
    this.refreshChrome()
  }
}

// tabTitle is the hover text, and the only place the app says out loud that a
// tab can be renamed. Both gestures were previously undiscoverable: nothing
// mentioned F2, and a double-click is not something you go looking for.
// where names the machine a tab runs on, and, for an admin looking at a
// teammate's tab, whose it is.
function where(session) {
  return session.owner ? `${session.host} · ${session.owner}'s tab` : session.host
}

function tabTitle(name, host) {
  return `${name} — ${host}\nDouble-click or hold to rename (F2)`
}
