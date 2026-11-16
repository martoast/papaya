import { api } from './api.js'
import { writeClipboard } from './terminal.js'

// The first-run setup screen: pick machines, let Papaya in, test, done.
//
// It is the first thing a new user sees after signing in to Tailscale, and the
// step most likely to make them give up, so every screen asks for one thing,
// every command has a Copy button, and every failure says what to do next.

const ADMIN_ACLS = 'https://login.tailscale.com/admin/acls/file'
const ADMIN_DNS = 'https://login.tailscale.com/admin/dns'
const DOWNLOAD = 'https://tailscale.com/download'
const POLL_MS = 4000

const esc = (s) =>
  String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c])

function osIcon(os) {
  const o = (os || '').toLowerCase()
  if (o.includes('mac')) return ''.length === 1 ? '🍎' : '🍎'
  if (o.includes('linux')) return '🐧'
  if (o.includes('windows')) return '🪟'
  if (o.includes('ios') || o.includes('android')) return '📱'
  return '🖥️'
}

function osLabel(os) {
  const o = (os || '').toLowerCase()
  if (o === 'macos' || o === 'darwin') return 'Mac'
  if (o === 'linux') return 'Linux'
  if (o === 'windows') return 'Windows'
  if (o === 'ios') return 'iPhone / iPad'
  if (o === 'android') return 'Android'
  return os || 'Device'
}

// papaya draws the logo: a papaya cut in half, seeds and all. Same mark as
// site/logo.svg, with flat colours and no ids so it can appear twice on a page.
// There is no papaya emoji, and a mango is not one.
const papaya = (size = 20) => `<svg width="${size}" height="${size}" viewBox="0 0 64 64" aria-hidden="true">
  
  <g transform="rotate(-24 32 32)">
    <path d="M32 3.5c6.5 0 10 5.5 12 12.5 1.6 5.6 6.5 9.5 7.6 17.5C53.4 47 44.5 60.5 32 60.5S10.6 47 12.4 33.5C13.5 25.5 18.4 21.6 20 16c2-7 5.5-12.5 12-12.5z" fill="#2f8a47"/>
    <path d="M32 6.5c5 0 8.2 4.6 9.9 10.9 1.7 6 6.2 9.7 7.2 16.4C50.6 45.3 42.7 57.5 32 57.5S13.4 45.3 14.9 33.8c1-6.7 5.5-10.4 7.2-16.4C23.8 11.1 27 6.5 32 6.5z" fill="#f3d36a"/>
    <path d="M32 8.5c4.2 0 7 4.1 8.5 9.6 1.7 6.1 5.9 9.6 6.8 15.9C48.6 44.4 41.6 55.5 32 55.5S15.4 44.4 16.7 34c.9-6.3 5.1-9.8 6.8-15.9C25 12.6 27.8 8.5 32 8.5z" fill="#ff8f4f"/>
    <path d="M32 22c4.6 0 7.5 6.5 7.5 14.5S36.2 50 32 50s-7.5-5.5-7.5-13.5S27.4 22 32 22z" fill="#e2582a"/>
    <g fill="#1c120d">
      <circle cx="30" cy="27" r="2"/><circle cx="34" cy="28.5" r="2"/><circle cx="29" cy="32" r="2.1"/><circle cx="33.4" cy="33.4" r="2.1"/>
      <circle cx="28.8" cy="37.4" r="2.1"/><circle cx="33.6" cy="38.6" r="2.1"/><circle cx="30.4" cy="42.4" r="2"/><circle cx="34.2" cy="43.6" r="1.8"/><circle cx="31.6" cy="46.6" r="1.6"/>
    </g>
    <g fill="#fff" opacity=".45">
      <circle cx="29.4" cy="26.4" r=".6"/><circle cx="33.4" cy="27.9" r=".6"/><circle cx="28.4" cy="31.4" r=".6"/><circle cx="32.8" cy="32.8" r=".6"/><circle cx="28.2" cy="36.8" r=".6"/><circle cx="33" cy="38" r=".6"/>
    </g>
  </g>
</svg>`

const isMac = (m) => (m.os || '').toLowerCase().startsWith('mac')

// keyCommand is what a person pastes on a machine to let Papaya's key in.
const keyCommand = (pub) => `mkdir -p ~/.ssh && echo '${pub}' >> ~/.ssh/authorized_keys`

// runSetup shows the setup screen and resolves with the first ready machine
// ({host, user}) when the person finishes, or null if they close it.
//
// reopen: true opens it from the settings button, over a running app, with a
// close button; a first run has none, since there is nothing behind it yet.
export function runSetup(overlaysEl, initial, { reopen = false } = {}) {
  return new Promise((resolve) => {
    const root = document.createElement('div')
    root.dataset.modal = 'setup'
    root.className = 'fixed inset-0 z-40 overflow-y-auto bg-surface-0 text-text-hi font-mono'
    overlaysEl.appendChild(root)

    let state = initial
    let step = 'machines' // machines → access → test → done
    // What the person has chosen, by machine name. Kept across refreshes.
    const picks = new Map()
    for (const m of state.machines || []) {
      if (m.selected) picks.set(m.name, { user: m.user || '', method: m.method })
    }
    // Test results, by machine name.
    const results = new Map()
    let timer = null
    let closed = false

    const finish = (value) => {
      if (closed) return
      closed = true
      clearInterval(timer)
      document.removeEventListener('keydown', onKey, true)
      root.remove()
      resolve(value)
    }
    const onKey = (e) => {
      // Keep keys away from the terminal underneath while this is open.
      e.stopPropagation()
      if (e.key === 'Escape' && reopen) finish(null)
    }
    document.addEventListener('keydown', onKey, true)

    const machines = () => state.machines || []
    const machine = (name) => machines().find((m) => m.name === name)
    const chosen = () => machines().filter((m) => picks.has(m.name))
    const lastUser = () =>
      [...picks.values()].map((p) => p.user).filter(Boolean).pop() || state.lastUser || ''

    // Machines come and go while the screen is open — a laptop wakes up, a
    // machine has Tailscale SSH switched on — so the list keeps itself fresh.
    timer = setInterval(async () => {
      if (step === 'test' || step === 'done') return
      try {
        state = await api.setup()
        render({ keepFocus: true })
      } catch {
        // A missed refresh is harmless; the next one will do.
      }
    }, POLL_MS)

    // --- rendering ---

    const stepNames = [
      ['machines', 'Machines'],
      ['access', 'Let Papaya in'],
      ['test', 'Test'],
    ]

    function header() {
      const idx = stepNames.findIndex(([k]) => k === step)
      const pills = stepNames
        .map(([k, label], i) => {
          const cls =
            i < idx || step === 'done'
              ? 'text-state-ok'
              : i === idx
                ? 'text-text-hi'
                : 'text-text-lo opacity-60'
          const mark = i < idx || step === 'done' ? '✓' : String(i + 1)
          return `<li class="flex items-center gap-1.5 ${cls}">
              <span class="grid size-5 place-items-center rounded-full border border-current text-[11px]">${mark}</span>
              <span class="hidden sm:inline">${label}</span></li>`
        })
        .join('<li class="text-text-lo opacity-40" aria-hidden="true">—</li>')
      return `
        <div class="flex items-center justify-between gap-4 mb-6">
          <div class="flex items-center gap-2">
            ${papaya(22)}
            <span class="text-base font-semibold">Papaya</span>
          </div>
          <ol class="flex items-center gap-2 text-xs" aria-label="Setup steps">${pills}</ol>
          ${
            reopen
              ? '<button data-act="close" class="px-2 py-1 text-text-lo hover:text-text-hi text-lg leading-none" aria-label="Close">×</button>'
              : '<span class="w-6"></span>'
          }
        </div>`
    }

    function httpsNote() {
      const h = state.https || {}
      if (location.protocol === 'http:' && h.on && h.url && h.url.startsWith('https:')) {
        return `<div data-note="https-ready" class="mb-5 rounded-lg border border-state-ok/40 bg-state-ok/10 p-3 text-sm">
            Papaya's secure address is ready.
            <a class="underline text-state-ok" href="${esc(h.url + location.pathname)}">Switch to ${esc(h.url)}</a>
          </div>`
      }
      if (!h.on && !h.available) {
        return `<details data-note="https-off" class="mb-5 rounded-lg border border-surface-3 bg-surface-1 p-3 text-sm">
            <summary class="cursor-pointer text-text-lo">Optional: turn on HTTPS for a secure address and voice input</summary>
            <p class="mt-2 text-text-lo">In your Tailscale admin, open <a class="underline text-accent" target="_blank" rel="noopener" href="${ADMIN_DNS}">DNS settings</a>
            and turn on <b class="text-text-hi">HTTPS Certificates</b>. Papaya notices within a minute and moves to https on its own.</p>
          </details>`
      }
      return ''
    }

    function copyBox(text, label = 'Copy') {
      return `<div class="mt-2 flex items-stretch gap-2">
          <code class="min-w-0 flex-1 overflow-x-auto whitespace-pre rounded bg-surface-2 border border-surface-3 px-2.5 py-2 text-xs text-text-hi">${esc(text)}</code>
          <button data-copy="${esc(text)}" class="shrink-0 rounded border border-surface-3 px-3 text-xs text-text-lo hover:text-text-hi hover:bg-surface-2">${label}</button>
        </div>`
    }

    function viewMachines() {
      const ms = machines()
      const list = ms.length
        ? ms
            .map((m) => {
              const on = picks.has(m.name)
              return `<label data-machine="${esc(m.name)}" class="flex cursor-pointer items-center gap-3 rounded-lg border ${
                on ? 'border-accent bg-accent/5' : 'border-surface-3 bg-surface-1'
              } p-3 hover:border-accent/60">
                <input type="checkbox" data-pick="${esc(m.name)}" ${on ? 'checked' : ''} class="size-4 accent-[var(--color-accent)]">
                <span class="text-xl" aria-hidden="true">${osIcon(m.os)}</span>
                <span class="min-w-0 flex-1">
                  <span class="block truncate text-sm font-medium">${esc(m.name)}</span>
                  <span class="block text-xs text-text-lo">${esc(osLabel(m.os))} ·
                    <span class="${m.online ? 'text-state-ok' : 'text-text-lo'}">${m.online ? 'online' : 'offline'}</span>
                  </span>
                </span>
                ${
                  m.tailscaleSSH
                    ? '<span class="shrink-0 rounded-full border border-state-ok/40 bg-state-ok/10 px-2 py-0.5 text-[11px] text-state-ok">Tailscale SSH on</span>'
                    : ''
                }
                ${m.new && reopen ? '<span class="shrink-0 rounded-full bg-accent/15 px-2 py-0.5 text-[11px] text-accent">new</span>' : ''}
              </label>`
            })
            .join('')
        : `<div data-empty class="rounded-lg border border-dashed border-surface-3 p-6 text-center text-sm text-text-lo">
            <div class="text-2xl mb-2" aria-hidden="true">📡</div>
            No other machines on your tailnet yet.<br>
            <a class="underline text-accent" target="_blank" rel="noopener" href="${DOWNLOAD}">Install Tailscale</a>
            on a computer you want to reach and sign in with the same account. It shows up here by itself.
          </div>`

      return `
        <h1 class="text-lg font-semibold">Which machines do you want to reach?</h1>
        <p class="mt-1 mb-4 text-sm text-text-lo">These are the devices on your tailnet${
          state.owner ? ` (signed in as ${esc(state.owner)})` : ''
        }. Tick the ones you want; you can change this any time.</p>
        <div class="flex flex-col gap-2">${list}</div>
        <div class="mt-6 flex items-center justify-end gap-3">
          <button data-act="next" ${picks.size ? '' : 'disabled'}
            class="rounded bg-accent px-4 py-2 text-sm font-medium text-surface-0 hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-30">
            Next${picks.size ? ` (${picks.size})` : ''}</button>
        </div>`
    }

    function viewAccess() {
      const anyTailscale = chosen().some((m) => (picks.get(m.name).method || m.method) === 'tailscale')
      const policy = anyTailscale
        ? `<div data-policy class="mb-4 rounded-lg border border-surface-3 bg-surface-1 p-4">
            <div class="text-sm font-medium">Once for your whole tailnet</div>
            <p class="mt-1 text-xs text-text-lo">Tailscale's default rule asks for a browser re-login every 12 hours, which an always-on app can't do.
              Open <a class="underline text-accent" target="_blank" rel="noopener" href="${ADMIN_ACLS}">Access controls</a>,
              find the <code>"ssh"</code> section, and replace it with this (or just change <code>"check"</code> to <code>"accept"</code>):</p>
            ${copyBox(state.policySnippet || '')}
          </div>`
        : ''

      const cards = chosen()
        .map((m) => {
          const p = picks.get(m.name)
          const method = p.method || m.method
          const user = p.user
          const tailscaleBody = m.tailscaleSSH
            ? `<p class="mt-3 text-sm text-state-ok">✓ Tailscale SSH is on for ${esc(m.name)}. Nothing to run there.</p>`
            : `<p class="mt-3 text-sm">On <b>${esc(m.name)}</b>, run:</p>
               ${copyBox('sudo tailscale set --ssh')}
               <p class="mt-2 text-xs text-text-lo" data-wait="${esc(m.name)}">
                 <span class="inline-block size-2 animate-pulse rounded-full bg-state-warn"></span>
                 Waiting for ${esc(m.name)}… this turns green by itself.</p>`
          const keyBody = `<p class="mt-3 text-sm">On <b>${esc(m.name)}</b>${
            user ? `, logged in as <b>${esc(user)}</b>` : ''
          }, run:</p>
            ${copyBox(keyCommand(state.publicKey || ''))}
            ${
              isMac(m)
                ? '<p class="mt-2 text-xs text-text-lo">On a Mac, also turn on <b class="text-text-hi">Remote Login</b>: System Settings → General → Sharing.</p>'
                : ''
            }`
          return `<section data-access="${esc(m.name)}" class="rounded-lg border border-surface-3 bg-surface-1 p-4">
              <div class="flex items-center gap-2">
                <span class="text-lg" aria-hidden="true">${osIcon(m.os)}</span>
                <span class="text-sm font-medium">${esc(m.name)}</span>
                ${m.online ? '' : '<span class="text-xs text-state-warn">offline</span>'}
              </div>

              <label class="mt-3 block text-xs text-text-lo" for="user-${esc(m.name)}">Your account on ${esc(m.name)}</label>
              <input id="user-${esc(m.name)}" data-user="${esc(m.name)}" value="${esc(user)}"
                placeholder="the username you log in with" autocomplete="off" autocapitalize="off" spellcheck="false"
                class="mt-1 w-full max-w-xs rounded bg-surface-2 border border-surface-3 px-2.5 py-2 text-sm outline-none focus:ring-1 focus:ring-accent">

              <div class="mt-4 inline-flex rounded-lg border border-surface-3 p-0.5 text-xs" role="tablist">
                <button data-method="tailscale" data-for="${esc(m.name)}" role="tab" aria-selected="${method === 'tailscale'}"
                  class="rounded-md px-3 py-1.5 ${method === 'tailscale' ? 'bg-surface-3 text-text-hi' : 'text-text-lo hover:text-text-hi'}">
                  Tailscale SSH${isMac(m) ? '' : ' · recommended'}</button>
                <button data-method="key" data-for="${esc(m.name)}" role="tab" aria-selected="${method === 'key'}"
                  class="rounded-md px-3 py-1.5 ${method === 'key' ? 'bg-surface-3 text-text-hi' : 'text-text-lo hover:text-text-hi'}">
                  Papaya key${isMac(m) ? ' · for Macs' : ''}</button>
              </div>
              ${
                isMac(m) && method === 'tailscale'
                  ? '<p class="mt-2 text-xs text-state-warn">Tailscale SSH only runs on Linux, so a Mac usually needs Papaya key.</p>'
                  : ''
              }
              ${method === 'tailscale' ? tailscaleBody : keyBody}
            </section>`
        })
        .join('')

      const missing = chosen().filter((m) => !picks.get(m.name).user)
      return `
        <h1 class="text-lg font-semibold">Let Papaya in</h1>
        <p class="mt-1 mb-4 text-sm text-text-lo">For each machine: the account to log in as, and one command to run on it.</p>
        ${policy}
        <div class="flex flex-col gap-3">${cards}</div>
        <p data-error class="mt-4 hidden text-sm text-state-err"></p>
        <div class="mt-6 flex items-center justify-between gap-3">
          <button data-act="back" class="rounded px-3 py-2 text-sm text-text-lo hover:bg-surface-2">Back</button>
          <button data-act="next" ${missing.length ? 'disabled' : ''}
            class="rounded bg-accent px-4 py-2 text-sm font-medium text-surface-0 hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-30">
            ${missing.length ? `Type your account on ${esc(missing[0].name)}` : 'Next: test'}</button>
        </div>`
    }

    function viewTest() {
      const rows = chosen()
        .map((m) => {
          const r = results.get(m.name) || { status: 'idle' }
          const p = picks.get(m.name)
          let body = ''
          if (r.status === 'testing') {
            body = '<span class="text-xs text-text-lo"><span class="inline-block size-2 animate-pulse rounded-full bg-accent"></span> Connecting…</span>'
          } else if (r.status === 'ok') {
            body = '<span class="text-sm text-state-ok">✓ Ready</span>'
          } else if (r.status === 'trust') {
            body = `<div class="mt-2 rounded border border-state-warn/40 bg-state-warn/10 p-3 text-xs">
                <p class="text-sm text-text-hi">${esc(r.message)}</p>
                <p class="mt-2 text-text-lo">Its fingerprint (${esc(r.keyType || 'host key')}):</p>
                <code data-fingerprint class="mt-1 block break-all rounded bg-surface-2 px-2 py-1.5 text-text-hi">${esc(r.fingerprint)}</code>
                <p class="mt-2 text-text-lo">To compare, run <code>ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub</code> on ${esc(m.name)}.</p>
                <button data-trust="${esc(m.name)}" class="mt-3 rounded bg-accent px-3 py-1.5 text-sm font-medium text-surface-0 hover:opacity-90">Trust and connect</button>
              </div>`
          } else if (r.status === 'error') {
            body = `<div data-test-error class="mt-2 rounded border border-state-err/40 bg-state-err/10 p-3 text-sm">
                <p>${esc(r.message)}</p>
                ${r.url ? `<a class="mt-1 inline-block underline text-accent text-xs" target="_blank" rel="noopener" href="${esc(r.url)}">Open the link it asked for</a>` : ''}
                <div class="mt-2 flex gap-2">
                  <button data-act="back" class="rounded px-2 py-1 text-xs text-text-lo hover:bg-surface-2">See the instructions</button>
                </div>
              </div>`
          }
          return `<section data-test="${esc(m.name)}" data-status="${r.status}" class="rounded-lg border border-surface-3 bg-surface-1 p-4">
              <div class="flex items-center gap-2">
                <span class="text-lg" aria-hidden="true">${osIcon(m.os)}</span>
                <span class="min-w-0 flex-1">
                  <span class="block text-sm font-medium">${esc(m.name)}</span>
                  <span class="block text-xs text-text-lo">as ${esc(p.user)} · ${(p.method || m.method) === 'key' ? 'Papaya key' : 'Tailscale SSH'}</span>
                </span>
                ${
                  r.status === 'testing'
                    ? ''
                    : `<button data-retest="${esc(m.name)}" class="shrink-0 rounded border border-surface-3 px-3 py-1.5 text-xs text-text-lo hover:text-text-hi hover:bg-surface-2">${r.status === 'idle' ? 'Test' : 'Test again'}</button>`
                }
              </div>
              ${body}
            </section>`
        })
        .join('')
      const ready = chosen().filter((m) => results.get(m.name)?.status === 'ok')
      return `
        <h1 class="text-lg font-semibold">Test the connection</h1>
        <p class="mt-1 mb-4 text-sm text-text-lo">Papaya logs in to each machine once to check everything works.</p>
        <div class="flex flex-col gap-3">${rows}</div>
        <div class="mt-6 flex items-center justify-between gap-3">
          <button data-act="back" class="rounded px-3 py-2 text-sm text-text-lo hover:bg-surface-2">Back</button>
          <button data-act="finish" ${ready.length ? '' : 'disabled'}
            class="rounded bg-accent px-4 py-2 text-sm font-medium text-surface-0 hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-30">
            ${ready.length ? 'Finish' : 'Waiting for a machine to pass'}</button>
        </div>`
    }

    function viewDone() {
      const first = chosen().find((m) => results.get(m.name)?.status === 'ok')
      return `
        <div class="py-10 text-center">
          <div class="inline-block">${papaya(64)}</div>
          <h1 class="mt-4 text-xl font-semibold">You're all set</h1>
          <p class="mt-2 text-sm text-text-lo">Your machines are one tap away, from any device on your tailnet.</p>
          <button data-act="open"
            class="mt-6 rounded bg-accent px-5 py-2.5 text-sm font-medium text-surface-0 hover:opacity-90">
            Open a terminal on ${esc(first?.name || 'your machine')}</button>
          <p class="mt-6 text-xs text-text-lo">Add more machines later from the ⚙ button. On a phone, "Add to Home Screen" makes Papaya an app.</p>
        </div>`
    }

    function render({ keepFocus = false } = {}) {
      // Rebuilding the DOM on every refresh would throw away what is being
      // typed, so remember the focused field and put the caret back.
      const active = keepFocus ? document.activeElement : null
      const focusKey = active?.dataset?.user
      const caret = focusKey ? active.selectionStart : null

      const views = { machines: viewMachines, access: viewAccess, test: viewTest, done: viewDone }
      root.dataset.step = step
      root.innerHTML = `<div class="mx-auto max-w-2xl px-4 py-6 sm:py-10">${header()}${step === 'done' ? '' : httpsNote()}${views[step]()}</div>`

      if (focusKey) {
        const again = root.querySelector(`[data-user="${CSS.escape(focusKey)}"]`)
        if (again) {
          again.focus()
          again.setSelectionRange(caret, caret)
        }
      }
    }

    // --- actions ---

    async function save() {
      const body = chosen().map((m) => {
        const p = picks.get(m.name)
        return { name: m.name, fqdn: m.fqdn, user: p.user, method: p.method || m.method }
      })
      state = await api.saveMachines(body)
    }

    async function test(name, trust = '') {
      const m = machine(name)
      const p = picks.get(name)
      if (!m || !p) return
      results.set(name, { status: 'testing' })
      render()
      try {
        const r = await api.testMachine({ host: name, user: p.user, method: p.method || m.method, trust })
        results.set(name, r)
      } catch (err) {
        results.set(name, { status: 'error', message: err.message })
      }
      if (!closed) render()
    }

    root.addEventListener('click', async (e) => {
      const t = e.target.closest('button, input[type=checkbox]')
      if (!t) return

      if (t.dataset.copy !== undefined) {
        const ok = await writeClipboard(t.dataset.copy)
        const before = t.textContent
        t.textContent = ok ? 'Copied' : 'Copy failed'
        setTimeout(() => (t.textContent = before), 1500)
        return
      }
      if (t.dataset.pick !== undefined) {
        const name = t.dataset.pick
        if (t.checked) {
          const m = machine(name)
          picks.set(name, { user: m?.user || lastUser(), method: m?.method || 'tailscale' })
        } else {
          picks.delete(name)
        }
        render()
        return
      }
      if (t.dataset.method) {
        const p = picks.get(t.dataset.for)
        if (p) p.method = t.dataset.method
        render()
        return
      }
      if (t.dataset.retest) return test(t.dataset.retest)
      if (t.dataset.trust) {
        const r = results.get(t.dataset.trust)
        return test(t.dataset.trust, r?.fingerprint || '')
      }

      switch (t.dataset.act) {
        case 'close':
          return finish(null)
        case 'back':
          step = step === 'test' ? 'access' : 'machines'
          return render()
        case 'next':
          if (step === 'machines') {
            step = 'access'
            return render()
          }
          if (step === 'access') {
            try {
              await save()
            } catch (err) {
              const el = root.querySelector('[data-error]')
              el.textContent = err.message
              el.classList.remove('hidden')
              return
            }
            step = 'test'
            render()
            // Test everything straight away: most machines just pass.
            for (const m of chosen()) {
              if (results.get(m.name)?.status !== 'ok') test(m.name)
            }
          }
          return
        case 'finish':
          try {
            state = await api.completeSetup(true)
          } catch {
            // Saving the flag failing only means the screen shows again next
            // time; the machines themselves are already saved.
          }
          step = 'done'
          return render()
        case 'open': {
          const first = chosen().find((m) => results.get(m.name)?.status === 'ok')
          return finish(first ? { host: first.name, user: picks.get(first.name).user } : null)
        }
      }
    })

    root.addEventListener('input', (e) => {
      const name = e.target.dataset.user
      if (name === undefined) return
      const p = picks.get(name)
      if (p) p.user = e.target.value.trim()
      // Only the footer depends on the typed account; re-rendering the whole
      // step on each keystroke would be wasteful and jumpy.
      const next = root.querySelector('[data-act="next"]')
      const missing = chosen().filter((m) => !picks.get(m.name).user)
      if (next) {
        next.disabled = missing.length > 0
        next.textContent = missing.length ? `Type your account on ${missing[0].name}` : 'Next: test'
      }
    })

    render()
  })
}

// watchNewMachines lights the settings button's dot while a machine has joined
// the tailnet that the owner has not been shown yet.
export function watchNewMachines(dotEl, intervalMs = 60_000) {
  const check = async () => {
    try {
      const st = await api.setup()
      const n = st.newMachines || 0
      dotEl.classList.toggle('hidden', n === 0)
      dotEl.parentElement?.setAttribute(
        'title',
        n ? `${n} new machine${n === 1 ? '' : 's'} on your tailnet` : 'Machines and setup',
      )
    } catch {
      // Offline or not set up; the dot simply stays as it was.
    }
  }
  check()
  return setInterval(check, intervalMs)
}
