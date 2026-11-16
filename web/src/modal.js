import { api } from './api.js'

// newSessionModal resolves with {host, name} or null if cancelled.
export function newSessionModal(overlaysEl) {
  return new Promise((resolve) => {
    const root = document.createElement('div')
    root.className = 'lg-scrim fixed inset-0 z-30 grid place-items-center'
    root.dataset.modal = 'new-session'

    root.innerHTML = `
      <div class="lg-sheet w-[min(22rem,calc(100vw-2rem))] p-5">
        <h2 class="lg-sheet-title mb-4">New session</h2>

        <label class="block text-xs text-text-lo mb-1" for="modal-host">Host</label>
        <select id="modal-host"
                class="lg-field w-full mb-3
                       text-sm outline-none focus:ring-1 focus:ring-accent">
          <option value="">Loading hosts…</option>
        </select>

        <!-- Which account this host is reached as. Hosts do not have to share a
             username, and a silent mismatch is what made a whole machine's work
             invisible to the resume list. -->
        <p id="modal-as" class="hidden -mt-2 mb-3 text-xs text-text-lo"></p>

        <label class="block text-xs text-text-lo mb-1" for="modal-name">
          Name <span class="opacity-60">(optional)</span>
        </label>
        <input id="modal-name" placeholder="billing-api" maxlength="40"
               class="lg-field w-full mb-4
                      text-sm outline-none focus:ring-1 focus:ring-accent">

        <p id="modal-error" class="hidden text-xs text-state-err mb-3"></p>

        <div class="flex justify-end gap-2">
          <button data-act="cancel"
                  class="lg-pill-btn">Cancel</button>
          <button data-act="connect"
                  class="lg-pill-btn lg-pill-primary">Connect</button>
        </div>
      </div>
    `

    overlaysEl.appendChild(root)

    const hostSel = root.querySelector('#modal-host')
    const nameInput = root.querySelector('#modal-name')
    const errorEl = root.querySelector('#modal-error')
    const asEl = root.querySelector('#modal-as')

    let settled = false
    const finish = (value) => {
      if (settled) return
      settled = true
      document.removeEventListener('keydown', onKey, true)
      root.remove()
      resolve(value)
    }

    const onKey = (e) => {
      // Captured so the terminal never sees these keys while the modal is open.
      e.stopPropagation()
      if (e.key === 'Escape') {
        e.preventDefault()
        finish(null)
      } else if (e.key === 'Enter' && e.target !== hostSel) {
        e.preventDefault()
        submit()
      }
    }
    document.addEventListener('keydown', onKey, true)

    const submit = () => {
      const host = hostSel.value.trim()
      if (!host) {
        errorEl.textContent = 'Choose a host'
        errorEl.classList.remove('hidden')
        return
      }
      finish({ host, name: nameInput.value.trim() })
    }

    root.querySelector('[data-act="connect"]').addEventListener('click', submit)
    root.querySelector('[data-act="cancel"]').addEventListener('click', () => finish(null))
    root.addEventListener('click', (e) => {
      if (e.target === root) finish(null)
    })

    api
      .listHosts()
      .then((hosts) => {
        hostSel.innerHTML = ''
        if (!hosts?.length) {
          hostSel.innerHTML = '<option value="">No hosts found</option>'
          return
        }
        const userByHost = new Map()
        for (const h of hosts) {
          // A host with no known state (configured but not a visible peer) stays
          // selectable; only a peer we know to be offline is disabled.
          const knownOffline = h.known && !h.online
          const opt = document.createElement('option')
          opt.value = h.name
          opt.textContent = knownOffline ? `${h.name} (offline)` : h.name
          opt.disabled = knownOffline
          hostSel.appendChild(opt)
          if (h.user) userByHost.set(h.name, h.user)
        }

        const showUser = () => {
          const u = userByHost.get(hostSel.value)
          asEl.textContent = u ? `connects as ${u}` : ''
          asEl.classList.toggle('hidden', !u)
        }
        hostSel.addEventListener('change', showUser)

        const firstSelectable = hosts.find((h) => h.online || !h.known)
        if (firstSelectable) hostSel.value = firstSelectable.name
        showUser()
        nameInput.focus()
      })
      .catch(() => {
        hostSel.innerHTML = '<option value="">Could not load hosts</option>'
      })
  })
}

// confirmModal resolves true/false.
export function confirmModal(overlaysEl, message) {
  return new Promise((resolve) => {
    const root = document.createElement('div')
    root.className = 'lg-scrim fixed inset-0 z-30 grid place-items-center'
    root.innerHTML = `
      <div class="lg-sheet w-[min(22rem,calc(100vw-2rem))] p-5">
        <p class="text-sm mb-4"></p>
        <div class="flex justify-end gap-2">
          <button data-act="no" class="lg-pill-btn">Cancel</button>
          <button data-act="yes" class="lg-pill-btn lg-pill-primary">Close</button>
        </div>
      </div>
    `
    root.querySelector('p').textContent = message
    overlaysEl.appendChild(root)

    const finish = (v) => {
      document.removeEventListener('keydown', onKey, true)
      root.remove()
      resolve(v)
    }
    const onKey = (e) => {
      e.stopPropagation()
      if (e.key === 'Escape') finish(false)
      if (e.key === 'Enter') finish(true)
    }
    document.addEventListener('keydown', onKey, true)

    root.querySelector('[data-act="yes"]').addEventListener('click', () => finish(true))
    root.querySelector('[data-act="no"]').addEventListener('click', () => finish(false))
  })
}

// searchOverlay wires a find bar over the active terminal.
export function searchOverlay(overlaysEl, tab) {
  const existing = overlaysEl.querySelector('[data-overlay="search"]')
  if (existing) {
    existing.querySelector('input').focus()
    return
  }

  const root = document.createElement('div')
  root.dataset.overlay = 'search'
  root.className =
    'lg-glass fixed top-16 right-4 z-30 flex items-center gap-1 rounded-full px-2 py-1.5'
  root.innerHTML = `
    <input placeholder="Find" spellcheck="false"
           class="lg-field w-48 text-sm">
    <button data-act="prev" class="px-1.5 text-text-lo hover:text-text-hi text-sm" aria-label="Previous">↑</button>
    <button data-act="next" class="px-1.5 text-text-lo hover:text-text-hi text-sm" aria-label="Next">↓</button>
    <button data-act="close" class="px-1.5 text-text-lo hover:text-state-err text-sm" aria-label="Close">×</button>
  `

  overlaysEl.appendChild(root)
  const input = root.querySelector('input')
  input.focus()

  const close = () => {
    tab.clearSearch()
    root.remove()
    tab.focus()
  }

  input.addEventListener('keydown', (e) => {
    e.stopPropagation()
    if (e.key === 'Enter') {
      e.preventDefault()
      if (e.shiftKey) tab.findPrevious(input.value)
      else tab.findNext(input.value)
    } else if (e.key === 'Escape') {
      e.preventDefault()
      close()
    }
  })
  input.addEventListener('keyup', (e) => e.stopPropagation())
  input.addEventListener('input', () => tab.findNext(input.value))

  root.querySelector('[data-act="next"]').addEventListener('click', () => tab.findNext(input.value))
  root.querySelector('[data-act="prev"]').addEventListener('click', () => tab.findPrevious(input.value))
  root.querySelector('[data-act="close"]').addEventListener('click', close)
}
