import { api } from './api.js'

// The Team panel: the admin adds a teammate by their Tailscale login (or a tag)
// and ticks the machines that teammate may use. A teammate sees only their own
// tabs, on those machines, and none of the settings. The server enforces all of
// it; this panel only edits the list.

const esc = (s) =>
  String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c])

export async function openTeam(overlaysEl) {
  let state
  try {
    state = await api.team()
  } catch (err) {
    return { error: err.message }
  }
  let members = state.members.map((m) => ({ login: m.login, machines: [...m.machines] }))
  const machines = state.machines || []

  return new Promise((resolve) => {
    const root = document.createElement('div')
    root.className = 'lg-scrim fixed inset-0 z-30 grid place-items-center'
    root.dataset.modal = 'team'
    overlaysEl.appendChild(root)

    const finish = (v) => {
      document.removeEventListener('keydown', onKey, true)
      root.remove()
      resolve(v)
    }
    const onKey = (e) => {
      if (e.key === 'Escape') {
        e.stopPropagation()
        finish(null)
      }
    }
    document.addEventListener('keydown', onKey, true)

    const render = (error = '') => {
      const rows = members
        .map(
          (m, i) => `
          <div class="mb-3 rounded-xl border border-white/10 p-3" data-member="${i}">
            <div class="flex items-center gap-2 mb-2">
              <span class="flex-1 truncate text-sm font-semibold">${esc(m.login)}</span>
              <button data-remove="${i}" class="lg-pill-btn text-xs">Remove</button>
            </div>
            ${
              machines.length
                ? machines
                    .map(
                      (name) => `
              <label class="flex items-center gap-2 text-sm py-0.5">
                <input type="checkbox" data-machine="${esc(name)}" data-of="${i}" ${m.machines.includes(name) ? 'checked' : ''}>
                <span>${esc(name)}</span>
              </label>`,
                    )
                    .join('')
                : '<p class="text-xs text-text-lo">No machines are set up yet. Add them under Machines first.</p>'
            }
          </div>`,
        )
        .join('')

      root.innerHTML = `
        <div class="lg-sheet w-[min(26rem,calc(100vw-2rem))] max-h-[85vh] overflow-y-auto p-5">
          <h2 class="lg-sheet-title mb-1">Team</h2>
          <p class="text-xs text-text-lo mb-4">Each teammate can open terminals only on the machines ticked for them, and sees nothing else: not your machines, not your tabs, not the settings. You keep full access to everything, including their tabs.</p>
          ${rows || '<p class="text-sm text-text-lo mb-4">No teammates yet.</p>'}
          <label class="block text-xs text-text-lo mb-1" for="team-login">Add a teammate (their Tailscale login, or a tag:name)</label>
          <div class="flex gap-2 mb-3">
            <input id="team-login" placeholder="ariosto@example.com" maxlength="200"
                   class="lg-field flex-1 text-sm outline-none focus:ring-1 focus:ring-accent">
            <button data-act="add" class="lg-pill-btn">Add</button>
          </div>
          <p id="team-error" class="${error ? '' : 'hidden'} text-xs text-state-err mb-3">${esc(error)}</p>
          <div class="flex justify-end gap-2">
            <button data-act="cancel" class="lg-pill-btn">Cancel</button>
            <button data-act="save" class="lg-pill-btn lg-pill-primary">Save</button>
          </div>
        </div>`
    }

    const collect = () => {
      root.querySelectorAll('input[data-machine]').forEach((box) => {
        const m = members[Number(box.dataset.of)]
        const name = box.dataset.machine
        m.machines = m.machines.filter((x) => x !== name)
        if (box.checked) m.machines.push(name)
      })
    }

    root.addEventListener('click', async (e) => {
      const t = e.target.closest('button')
      if (e.target === root) return finish(null)
      if (!t) return
      if (t.dataset.remove !== undefined) {
        collect()
        members.splice(Number(t.dataset.remove), 1)
        return render()
      }
      switch (t.dataset.act) {
        case 'cancel':
          return finish(null)
        case 'add': {
          collect()
          const login = root.querySelector('#team-login').value.trim()
          if (!login) return
          if (members.some((m) => m.login.toLowerCase() === login.toLowerCase())) return render(`${login} is already on the team`)
          members.push({ login, machines: [] })
          return render()
        }
        case 'save':
          collect()
          try {
            const saved = await api.saveTeam(members)
            members = saved.members
            finish(saved)
          } catch (err) {
            render(err.message)
          }
      }
    })

    render()
  })
}
