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
    root.className = 'setup-ui fixed inset-0 z-40 overflow-y-auto bg-surface-0 text-text-hi'
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
    //
    // Mobile first: most people meet this screen on the phone they just used to
    // sign Papaya in. Prose in a readable sans, monospace only for commands and
    // machine names, a labelled progress bar, and the step's actions in a bar
    // pinned to the bottom, where a thumb is.

    const stepNames = [
      ['machines', 'Choose machines'],
      ['access', 'Let Papaya in'],
      ['test', 'Test'],
    ]

    function header() {
      const idx = step === 'done' ? stepNames.length : stepNames.findIndex(([k]) => k === step)
      const bars = stepNames
        .map((_, i) => `<i class="su-bar ${i < idx ? 'is-done' : i === idx ? 'is-now' : ''}"></i>`)
        .join('')
      const label =
        step === 'done' ? 'All set' : `Step ${idx + 1} of ${stepNames.length} · ${stepNames[idx][1]}`
      return `
        <header class="su-head">
          <div class="su-brand">
            ${step === 'machines' && !reopen ? '' : `${papaya(22)}<span>Papaya</span>`}
            ${reopen ? '<button data-act="close" class="su-x" aria-label="Close">×</button>' : ''}
          </div>
          <div class="su-progress" aria-label="${esc(label)}">${bars}</div>
          <div class="su-steplabel">${esc(label)}</div>
        </header>`
    }

    // httpsNote: the switch to the secure address when it is ready, and on the
    // last step the reason to turn HTTPS on (Safari on iPhone quietly upgrades
    // http to https, and voice input and installing as an app need it).
    function httpsNote() {
      const h = state.https || {}
      if (location.protocol === 'http:' && h.on && h.url && h.url.startsWith('https:')) {
        return `<div data-note="https-ready" class="su-banner su-ok">
            Papaya's secure address is ready.
            <a href="${esc(h.url + location.pathname)}">Switch to ${esc(h.url)}</a>
          </div>`
      }
      if (!h.on && !h.available) {
        return `<details data-note="https-off" class="su-banner">
            <summary>Recommended: turn on HTTPS</summary>
            <p>Safari on iPhone switches addresses to https by itself, and voice input and
            installing Papaya as an app need it. In your Tailscale admin, open
            <a target="_blank" rel="noopener" href="${ADMIN_DNS}">DNS settings</a> and turn on
            <b>HTTPS Certificates</b>. Papaya notices within a minute and moves to https on its own.</p>
          </details>`
      }
      return ''
    }

    function copyBox(text, label = 'Copy') {
      return `<div class="su-copy">
          <code>${esc(text)}</code>
          <button data-copy="${esc(text)}" class="su-btn su-btn-ghost">${label}</button>
        </div>`
    }

    function viewMachines() {
      const ms = machines()
      const list = ms.length
        ? ms
            .map((m) => {
              const on = picks.has(m.name)
              return `<label data-machine="${esc(m.name)}" class="su-pick ${on ? 'is-on' : ''}">
                <input type="checkbox" data-pick="${esc(m.name)}" ${on ? 'checked' : ''} class="su-check">
                <span class="su-os" aria-hidden="true">${osIcon(m.os)}</span>
                <span class="su-grow">
                  <span class="su-name">${esc(m.name)}</span>
                  <span class="su-sub">${esc(osLabel(m.os))} ·
                    <span class="${m.online ? 'su-online' : ''}">${m.online ? 'online' : 'offline'}</span>
                    ${m.tailscaleSSH ? '<span class="su-chip su-chip-ok">Tailscale SSH on</span>' : ''}
                    ${m.new && reopen ? '<span class="su-chip">new</span>' : ''}
                  </span>
                </span>
                <span class="su-tick" aria-hidden="true">✓</span>
              </label>`
            })
            .join('')
        : `<div data-empty class="su-card su-empty">
            <div aria-hidden="true">📡</div>
            <p>No other machines on your tailnet yet.</p>
            <p><a target="_blank" rel="noopener" href="${DOWNLOAD}">Install Tailscale</a> on a computer you
            want to reach and sign in with the same account. It shows up here by itself.</p>
          </div>`

      const hero = reopen
        ? `<h1>Your machines</h1>`
        : `<div class="su-hero"><span class="su-mark">${papaya(76)}</span><h1>Welcome to Papaya</h1>
            <p class="su-lede">Every machine you own, in your pocket. Pick the ones you want to reach.</p></div>`
      return {
        body: `
          ${hero}
          ${reopen ? `<p class="su-lede">Pick the ones you want to reach${state.owner ? ` as <b>${esc(state.owner)}</b>` : ''}.</p>` : ''}
          <p class="su-label">On your tailnet${!reopen && state.owner ? ` · ${esc(state.owner)}` : ''}</p>
          ${ms.length ? `<div class="su-group">${list}</div>` : list}`,
        foot: `<button data-act="next" ${picks.size ? '' : 'disabled'} class="su-btn su-btn-primary su-wide">
            Next${picks.size ? ` (${picks.size})` : ''}</button>`,
      }
    }

    function viewAccess() {
      const anyTailscale = chosen().some((m) => (picks.get(m.name).method || m.method) === 'tailscale')
      const policy = anyTailscale
        ? `<section data-policy class="su-card su-policy">
            <div class="su-policy-head">
              <span class="su-policy-ico" aria-hidden="true">🔐</span>
              <div>
                <div class="su-name-sans">One-time Tailscale permission</div>
                <p class="su-sub-p">Let Papaya log in without asking you every 12 hours. In <b>Access controls</b>,
                  change <code>"check"</code> to <code>"accept"</code> in the <code>"ssh"</code> section, or paste this snippet there.</p>
              </div>
            </div>
            <details class="su-fold"><summary>Show the snippet</summary>${copyBox(state.policySnippet || '', 'Copy snippet')}</details>
            <div class="su-row">
              <button data-copy="${esc(state.policySnippet || '')}" class="su-btn su-btn-ghost">Copy snippet</button>
              <a class="su-btn su-btn-ghost" target="_blank" rel="noopener" href="${ADMIN_ACLS}">Open Access controls ↗</a>
            </div>
          </section>`
        : ''

      const cards = chosen()
        .map((m) => {
          const p = picks.get(m.name)
          const method = p.method || m.method
          const user = p.user
          const tailscaleBody = m.tailscaleSSH
            ? `<p class="su-good">✓ Tailscale SSH is on for ${esc(m.name)}. Nothing to run there.</p>`
            : `<p class="su-step-p">On <b class="su-mono">${esc(m.name)}</b>, run:</p>
               ${copyBox('sudo tailscale set --ssh')}
               <p class="su-wait" data-wait="${esc(m.name)}"><i></i>Waiting for ${esc(m.name)}… this turns green by itself.</p>`
          const keyBody = `<p class="su-step-p">On <b class="su-mono">${esc(m.name)}</b>${
            user ? `, logged in as <b>${esc(user)}</b>` : ''
          }, run:</p>
            ${copyBox(keyCommand(state.publicKey || ''))}
            ${
              isMac(m)
                ? '<p class="su-note">On a Mac, also turn on <b>Remote Login</b>: System Settings → General → Sharing.</p>'
                : ''
            }`
          return `<section data-access="${esc(m.name)}" class="su-card">
              <div class="su-cardhead">
                <span class="su-os" aria-hidden="true">${osIcon(m.os)}</span>
                <span class="su-name">${esc(m.name)}</span>
                ${m.online ? '' : '<span class="su-chip su-chip-warn">offline</span>'}
              </div>

              <div class="su-field">
                <label for="user-${esc(m.name)}">Account</label>
                <input id="user-${esc(m.name)}" data-user="${esc(m.name)}" value="${esc(user)}" aria-label="Your account on ${esc(m.name)}"
                  placeholder="your username" autocomplete="off" autocapitalize="off" autocorrect="off" spellcheck="false"
                  class="su-input">
              </div>

              <p class="su-label">Connect with</p>
              <div class="su-seg" role="tablist">
                <button data-method="tailscale" data-for="${esc(m.name)}" role="tab" aria-selected="${method === 'tailscale'}"
                  class="${method === 'tailscale' ? 'is-on' : ''}">Tailscale SSH<small>${isMac(m) ? 'Linux only' : 'recommended'}</small></button>
                <button data-method="key" data-for="${esc(m.name)}" role="tab" aria-selected="${method === 'key'}"
                  class="${method === 'key' ? 'is-on' : ''}">Papaya key<small>${isMac(m) ? 'for Macs' : 'any SSH server'}</small></button>
              </div>
              ${
                isMac(m) && method === 'tailscale'
                  ? '<p class="su-warn">Tailscale SSH only runs on Linux, so a Mac usually needs Papaya key.</p>'
                  : ''
              }
              ${method === 'tailscale' ? tailscaleBody : keyBody}
            </section>`
        })
        .join('')

      const missing = chosen().filter((m) => !picks.get(m.name).user)
      return {
        body: `
          <h1>Let Papaya in</h1>
          <p class="su-lede">For each machine, the account to log in as and one quick step.</p>
          ${policy}
          <div class="su-list">${cards}</div>
          <p data-error class="su-err hidden"></p>`,
        foot: `<p data-hint class="su-hint ${missing.length ? '' : 'hidden'}">${missing.length ? `Type your account on ${esc(missing[0].name)}` : ''}</p>
          <button data-act="back" class="su-btn su-btn-ghost">Back</button>
          <button data-act="next" ${missing.length ? 'disabled' : ''} class="su-btn su-btn-primary su-grow">Next: test</button>`,
      }
    }

    function viewTest() {
      const rows = chosen()
        .map((m) => {
          const r = results.get(m.name) || { status: 'idle' }
          const p = picks.get(m.name)
          let body = ''
          if (r.status === 'testing') {
            body = '<p class="su-wait"><i></i>Connecting…</p>'
          } else if (r.status === 'ok') {
            body = '<p class="su-good">✓ Ready</p>'
          } else if (r.status === 'trust') {
            body = `<div class="su-callout su-callout-warn">
                <p>${esc(r.message)}</p>
                <p class="su-sub-p">Its fingerprint (${esc(r.keyType || 'host key')}):</p>
                <code data-fingerprint class="su-fp">${esc(r.fingerprint)}</code>
                <p class="su-sub-p">To compare, run <code>ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub</code> on ${esc(m.name)}.</p>
                <button data-trust="${esc(m.name)}" class="su-btn su-btn-primary su-wide">Trust and connect</button>
              </div>`
          } else if (r.status === 'error') {
            body = `<div data-test-error class="su-callout su-callout-err">
                <p>${esc(r.message)}</p>
                ${r.url ? `<a target="_blank" rel="noopener" href="${esc(r.url)}">Open the link it asked for ↗</a>` : ''}
                <button data-act="back" class="su-btn su-btn-ghost">See the instructions</button>
              </div>`
          }
          return `<section data-test="${esc(m.name)}" data-status="${r.status}" class="su-card">
              <div class="su-cardhead">
                <span class="su-os" aria-hidden="true">${osIcon(m.os)}</span>
                <span class="su-grow">
                  <span class="su-name">${esc(m.name)}</span>
                  <span class="su-sub">as ${esc(p.user)} · ${(p.method || m.method) === 'key' ? 'Papaya key' : 'Tailscale SSH'}</span>
                </span>
                ${
                  r.status === 'testing'
                    ? ''
                    : `<button data-retest="${esc(m.name)}" class="su-btn su-btn-ghost su-sm">${r.status === 'idle' ? 'Test' : 'Test again'}</button>`
                }
              </div>
              ${body}
            </section>`
        })
        .join('')
      const ready = chosen().filter((m) => results.get(m.name)?.status === 'ok')
      return {
        body: `
          <h1>Test the connection</h1>
          <p class="su-lede">Papaya logs in to each machine once to check everything works.</p>
          <div class="su-list">${rows}</div>`,
        foot: `<button data-act="back" class="su-btn su-btn-ghost">Back</button>
          <button data-act="finish" ${ready.length ? '' : 'disabled'} class="su-btn su-btn-primary su-grow">
            ${ready.length ? 'Finish' : 'Waiting for a machine to pass'}</button>`,
      }
    }

    function viewDone() {
      const first = chosen().find((m) => results.get(m.name)?.status === 'ok')
      return {
        body: `
          <div class="su-done">
            <span class="su-mark">${papaya(84)}</span>
            <h1>You're all set</h1>
            <p class="su-lede">Your machines are one tap away, from any device on your tailnet.
            Add more later from the ⚙ button.</p>
          </div>
          ${httpsNote()}`,
        foot: `<button data-act="open" class="su-btn su-btn-primary su-wide">
            Open a terminal on ${esc(first?.name || 'your machine')}</button>`,
      }
    }

    function render({ keepFocus = false } = {}) {
      // Rebuilding the DOM on every refresh would throw away what is being
      // typed, so remember the focused field and put the caret back.
      const active = keepFocus ? document.activeElement : null
      const focusKey = active?.dataset?.user
      const caret = focusKey ? active.selectionStart : null
      const scroll = root.scrollTop

      const views = { machines: viewMachines, access: viewAccess, test: viewTest, done: viewDone }
      const v = views[step]()
      const ready = (state.https || {}).on && location.protocol === 'http:' && step !== 'done' ? httpsNote() : ''
      root.dataset.step = step
      root.innerHTML = `${header()}
        <main class="su-main">${ready}${v.body}</main>
        <footer class="su-foot"><div class="su-foot-in">${v.foot}</div></footer>`
      if (keepFocus) root.scrollTop = scroll

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
      const hint = root.querySelector('[data-hint]')
      const missing = chosen().filter((m) => !picks.get(m.name).user)
      if (next) next.disabled = missing.length > 0
      if (hint) {
        hint.textContent = missing.length ? `Type your account on ${missing[0].name}` : ''
        hint.classList.toggle('hidden', !missing.length)
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
