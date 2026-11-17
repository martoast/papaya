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
const APP_STORE = 'https://apps.apple.com/app/tailscale/id1470499037'
const PLAY_STORE = 'https://play.google.com/store/apps/details?id=com.tailscale.ipn'
const REPO = 'https://github.com/martoast/papaya'

// Small line icons for the welcome pages (SF Symbols in spirit).
const ICON = {
  machines: '<rect x="3" y="4" width="18" height="12" rx="2.5"/><path d="M8 20h8M12 16v4"/>',
  bell: '<path d="M6 16V11a6 6 0 0112 0v5l1.5 2h-15z"/><path d="M10 20a2 2 0 004 0"/>',
  mic: '<rect x="9" y="3" width="6" height="11" rx="3"/><path d="M5.5 11a6.5 6.5 0 0013 0M12 17.5V21"/>',
  home: '<path d="M4 11l8-7 8 7v8.5a1.5 1.5 0 01-1.5 1.5H5.5A1.5 1.5 0 014 19.5z"/><path d="M10 21v-6h4v6"/>',
  key: '<circle cx="8" cy="15" r="4"/><path d="M11 12l8-8M16 7l2.5 2.5M14 9l2 2"/>',
  shield: '<path d="M12 3l7.5 3v5.5c0 4.6-3.2 8.3-7.5 9.5-4.3-1.2-7.5-4.9-7.5-9.5V6z"/><path d="M8.8 12.2l2.2 2.2 4.4-4.6"/>',
  code: '<path d="M8.5 7L3.5 12l5 5M15.5 7l5 5-5 5M13.5 4.5l-3 15"/>',
  phone: '<rect x="6.5" y="2.5" width="11" height="19" rx="3"/><path d="M11 18.5h2"/>',
  laptop: '<rect x="4.5" y="5" width="15" height="10" rx="1.8"/><path d="M2.5 19h19"/>',
}
const ico = (name, size = 22) =>
  `<svg viewBox="0 0 24 24" width="${size}" height="${size}" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${ICON[name]}</svg>`
const isIOSDevice = () =>
  /iPhone|iPad|iPod/.test(navigator.userAgent) || (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1)
const isAndroid = () => /Android/.test(navigator.userAgent)
const POLL_MS = 2500

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
    // First run opens on a short welcome: what Papaya is, why it is private,
    // and how devices reach it. Reopened from the menu, it goes straight to
    // the machines.
    let step = reopen ? 'machines' : 'welcome' // welcome → machines → access → done
    let page = 0 // the welcome's three pages
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
        const next = await api.setup()
        // Re-render only when something changed: rebuilding an unchanged step
        // every few seconds made it flicker.
        if (JSON.stringify(next) === JSON.stringify(state)) return
        state = next
        if (step === 'welcome') return // nothing on these pages changes with it
        autoTest()
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
      ['access', 'Connect them'],
    ]

    function header() {
      const idx = step === 'done' ? stepNames.length : stepNames.findIndex(([k]) => k === step)
      const bars = stepNames
        .map((_, i) => `<i class="su-bar ${i < idx ? 'is-done' : i === idx ? 'is-now' : ''}"></i>`)
        .join('')
      const label =
        step === 'done' || step === 'welcome' ? 'All set' : `Step ${idx + 1} of ${stepNames.length} · ${stepNames[idx][1]}`
      if (step === 'welcome') {
        return `
        <header class="su-head su-head-welcome">
          <div class="su-dots" aria-label="Page ${page + 1} of 3">${[0, 1, 2]
            .map((i) => `<i class="${i === page ? 'is-now' : ''}"></i>`)
            .join('')}</div>
          ${page < 2 ? '<button data-act="skip" class="su-skip">Skip</button>' : ''}
        </header>`
      }
      return `
        <header class="su-head">
          <div class="su-brand">
            ${papaya(22)}<span>Papaya</span>
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

    // The welcome: three short pages, one idea each.
    function feature(icon, title, text, tint = '') {
      return `<div class="su-feat">
          <span class="su-feat-ico ${tint}">${ico(icon)}</span>
          <span><b>${title}</b><span>${text}</span></span>
        </div>`
    }

    function viewWelcome() {
      const self = esc(state.self || 'this machine')
      const pages = [
        // 1. What it is.
        `<div class="su-hero"><span class="su-mark">${papaya(76)}</span>
            <h1>Welcome to Papaya</h1>
            <p class="su-lede">Your terminals and coding agents, on every machine you own, in your pocket.</p></div>
          <div class="su-feats">
            ${feature('machines', 'Every machine, one tap', 'Open a terminal on your laptop, server or desktop from your phone. Your work keeps running when you close it.')}
            ${feature('bell', 'Know when an agent needs you', 'Each tab shows if Claude Code or Codex is working, done, or waiting on you.', 'is-amber')}
            ${feature('mic', 'Talk instead of typing', 'Tap the mic and say what you want. It lands in the terminal.', 'is-papaya')}
          </div>`,
        // 2. Why it is private.
        `<div class="su-hero su-hero-sm"><span class="su-glyph is-green">${ico('shield', 44)}</span>
            <h1>Yours, and only yours</h1>
            <p class="su-lede">There is no Papaya cloud. Nothing you do here is sent to us.</p></div>
          <div class="su-feats">
            ${feature('home', 'It runs on your machine', `Papaya lives on <b>${self}</b>. Your terminals go straight from your devices to your machines, never through anyone else.`, 'is-green')}
            ${feature('key', 'Your keys stay home', 'The key Papaya logs in with is made here and never leaves this machine. Same for your voice key.', 'is-green')}
            ${feature('shield', 'A private network', 'Only devices signed in to your own Tailscale account can open Papaya. It is not on the public internet.', 'is-green')}
            ${feature('code', 'Open source', `Every line is public, so anyone can check. <a href="${REPO}" target="_blank" rel="noopener">See the code ↗</a>`, 'is-green')}
          </div>`,
        // 3. How devices reach it.
        `<div class="su-hero su-hero-sm"><span class="su-glyph is-blue">${tailscaleMark()}</span>
            <h1>Your devices, connected</h1>
            <p class="su-lede">Papaya uses <b>Tailscale</b>, a free private network for your own devices.
              You already have it on this one; that is how this page opened.</p></div>
          <div class="su-net" aria-hidden="true">
            <span class="su-node">${ico('phone', 20)}<small>Phone</small></span><i></i>
            <span class="su-node is-papaya">${papaya(22)}<small>Papaya</small></span><i></i>
            <span class="su-node">${ico('laptop', 20)}<small>Computers</small></span>
          </div>
          <div class="su-card su-tsadd">
            <p class="su-tsadd-title">To use Papaya on another device</p>
            <p class="su-tsadd-sub">Install Tailscale there and sign in with the same account${state.owner ? ` (<b>${esc(state.owner)}</b>)` : ''}. Then open this same address.</p>
            <div class="su-stores">
              ${isAndroid() ? '' : `<a class="su-store" href="${APP_STORE}" target="_blank" rel="noopener">${ico('phone', 18)}<span><small>Get it for</small>iPhone &amp; iPad</span></a>`}
              ${isIOSDevice() ? '' : `<a class="su-store" href="${PLAY_STORE}" target="_blank" rel="noopener">${ico('phone', 18)}<span><small>Get it for</small>Android</span></a>`}
              <a class="su-store" href="${DOWNLOAD}" target="_blank" rel="noopener">${ico('laptop', 18)}<span><small>Get it for</small>Mac, Windows, Linux</span></a>
            </div>
            <p class="su-tsadd-sub su-mt">Machines you want to reach need Tailscale too. Add them any time; they appear here by themselves.</p>
          </div>`,
      ]
      return {
        body: `<div class="su-page" data-page="${page}">${pages[page]}</div>`,
        foot:
          page < 2
            ? `${page > 0 ? '<button data-act="prev" class="su-btn su-btn-ghost">Back</button>' : ''}
               <button data-act="continue" class="su-btn su-btn-primary ${page > 0 ? 'su-grow' : 'su-wide'}">Continue</button>`
            : `<button data-act="prev" class="su-btn su-btn-ghost">Back</button>
               <button data-act="continue" class="su-btn su-btn-primary su-grow">Choose your machines</button>`,
      }
    }

    function tailscaleMark() {
      // Tailscale's nine dots, drawn plainly.
      const d = [[6, 6, 0.35], [12, 6, 0.35], [18, 6, 0.35], [6, 12, 1], [12, 12, 1], [18, 12, 1], [6, 18, 0.35], [12, 18, 1], [18, 18, 0.35]]
      return `<svg viewBox="0 0 24 24" width="44" height="44" aria-hidden="true">${d
        .map(([x, y, o]) => `<circle cx="${x}" cy="${y}" r="2.4" fill="currentColor" opacity="${o}"/>`)
        .join('')}</svg>`
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
        : `<h1>Choose your machines</h1>
            <p class="su-lede">These are the devices on your Tailscale network. Pick the ones you want to open terminals on.</p>`
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

    // Machines the person chose to set up by hand (username, method), instead
    // of with the one-line join.
    const manual = new Set()
    // The join each machine was last tested for, so a fresh join tests again.
    const testedJoin = new Map()

    // The line to paste. On plain http over the tailnet it uses Papaya's short
    // MagicDNS name ("papaya-dev"), which every machine on the tailnet resolves
    // and a person can actually type; https keeps the full name its certificate
    // is for.
    const joinCommand = () => {
      const h = location.hostname
      if (location.protocol === 'http:' && /\.ts\.net$/i.test(h)) {
        const short = h.split('.')[0] + (location.port ? `:${location.port}` : '')
        return `curl -fsSL ${short}/join | sh`
      }
      return `curl -fsSL ${location.origin}/join | sh`
    }

    function resultBody(m, r) {
      if (!r || r.status === 'idle') return ''
      if (r.status === 'testing') return `<p class="su-wait"><i></i>Connecting to ${esc(m.name)}…</p>`
      if (r.status === 'ok') return ''
      if (r.status === 'trust') {
        return `<div class="su-callout su-callout-warn">
            <p>${esc(r.message)}</p>
            <p class="su-sub-p">Its fingerprint (${esc(r.keyType || 'host key')}):</p>
            <code data-fingerprint class="su-fp">${esc(r.fingerprint)}</code>
            <button data-trust="${esc(m.name)}" class="su-btn su-btn-primary su-wide">Trust and connect</button>
          </div>`
      }
      return `<div data-test-error class="su-callout su-callout-err">
          <p>${esc(r.message)}</p>
          ${r.url ? `<a target="_blank" rel="noopener" href="${esc(r.url)}">Open the link it asked for ↗</a>` : ''}
          <button data-retest="${esc(m.name)}" class="su-btn su-btn-ghost">Try again</button>
        </div>`
    }

    // manualBody is the old way in: type the account, pick how Papaya logs in.
    function manualBody(m) {
      const p = picks.get(m.name)
      const method = p.method || m.method
      const user = p.user
      const policy = method === 'tailscale'
        ? `<div data-policy class="su-callout">
            <p><b>One-time Tailscale permission.</b> In Access controls, change <code>"check"</code> to
            <code>"accept"</code> in the <code>"ssh"</code> section, or paste this snippet there.</p>
            ${copyBox(state.policySnippet || '', 'Copy snippet')}
            <a class="su-btn su-btn-ghost" target="_blank" rel="noopener" href="${ADMIN_ACLS}">Open Access controls ↗</a>
          </div>`
        : ''
      const how = method === 'tailscale'
        ? m.tailscaleSSH
          ? `<p class="su-good">✓ Tailscale SSH is on for ${esc(m.name)}.</p>`
          : `<p class="su-step-p">On <b class="su-mono">${esc(m.name)}</b>, run:</p>${copyBox('sudo tailscale set --ssh')}`
        : `<p class="su-step-p">On <b class="su-mono">${esc(m.name)}</b>${user ? `, as <b>${esc(user)}</b>` : ''}, run:</p>
           ${copyBox(keyCommand(state.publicKey || ''))}
           ${isMac(m) ? '<p class="su-note">On a Mac, also turn on <b>Remote Login</b>: System Settings → General → Sharing.</p>' : ''}`
      return `
        <label class="su-label" for="user-${esc(m.name)}">Your username on ${esc(m.name)}</label>
        <input id="user-${esc(m.name)}" data-user="${esc(m.name)}" value="${esc(user)}"
          placeholder="e.g. alex" autocomplete="off" autocapitalize="off" autocorrect="off" spellcheck="false"
          enterkeyhint="done" class="su-input ${user ? '' : 'is-empty'}">
        <p class="su-label">Connect with</p>
        <div class="su-seg" role="tablist">
          <button data-method="tailscale" data-for="${esc(m.name)}" role="tab" aria-selected="${method === 'tailscale'}"
            class="${method === 'tailscale' ? 'is-on' : ''}">Tailscale SSH<small>${isMac(m) ? 'Linux only' : 'no keys'}</small></button>
          <button data-method="key" data-for="${esc(m.name)}" role="tab" aria-selected="${method === 'key'}"
            class="${method === 'key' ? 'is-on' : ''}">Papaya key<small>any SSH server</small></button>
        </div>
        ${how}
        ${policy}
        <button data-retest="${esc(m.name)}" class="su-btn su-btn-primary su-wide su-mt" ${user ? '' : 'disabled'}>Test the connection</button>
        <button data-auto="${esc(m.name)}" class="su-linkbtn">Use the one-line way instead</button>`
    }

    // viewAccess is "Connect your machines": one line to paste on each, and
    // the card turns green by itself.
    function viewAccess() {
      const cards = chosen()
        .map((m) => {
          const r = results.get(m.name)
          const p = picks.get(m.name)
          let pill = '<span class="su-pill"><i class="su-dot"></i>Waiting</span>'
          let body = ''
          if (r?.status === 'ok') {
            pill = '<span class="su-pill su-pill-ok">✓ Ready</span>'
            body = `<p class="su-good">Connected as ${esc(p.user)}. You can open it from Papaya now.</p>`
          } else if (manual.has(m.name)) {
            pill = r?.status === 'testing' ? '<span class="su-pill"><i class="su-dot"></i>Testing</span>' : ''
            body = manualBody(m) + resultBody(m, r)
          } else if (m.joined && !m.sshd && r?.status !== 'testing' && r?.status !== 'trust') {
            pill = '<span class="su-pill su-pill-warn">One more step</span>'
            body = isMac(m) || /darwin|mac/i.test(m.os)
              ? `<p class="su-step-p"><b>Almost there.</b> Turn on <b>Remote Login</b> on ${esc(m.name)}:
                 System Settings → General → Sharing → Remote Login. It just opened for you.</p>
                 <button data-retest="${esc(m.name)}" class="su-btn su-btn-primary su-wide">I turned it on</button>`
              : `<p class="su-step-p"><b>Almost there.</b> ${esc(m.name)} needs an SSH server. On Ubuntu or Debian:</p>
                 ${copyBox('sudo apt install -y openssh-server')}
                 <button data-retest="${esc(m.name)}" class="su-btn su-btn-primary su-wide su-mt">Check again</button>`
            body += resultBody(m, r?.status === 'error' ? r : null)
          } else if (m.joined) {
            pill = '<span class="su-pill"><i class="su-dot"></i>Connecting</span>'
            body = resultBody(m, r) || `<p class="su-wait"><i></i>Connecting to ${esc(m.name)}…</p>`
          } else {
            const mac = isMac(m) || /darwin|mac/i.test(m.os)
            body = `
              <p class="su-step-p">${
                mac
                  ? `On <b>${esc(m.name)}</b>, open <b>Terminal</b> (⌘ Space, type “Terminal”) and paste:`
                  : `On <b>${esc(m.name)}</b>, open a terminal and paste:`
              }</p>
              <div class="su-oneline">
                <code>${esc(joinCommand())}</code>
                <button data-copy="${esc(joinCommand())}" class="su-btn su-btn-primary su-wide">Copy</button>
              </div>
              <p class="su-wait" data-wait="${esc(m.name)}"><i></i>Waiting for ${esc(m.name)}… this turns green by itself.</p>
              <button data-manual="${esc(m.name)}" class="su-linkbtn">Set up another way</button>`
          }
          return `<section data-access="${esc(m.name)}" data-joined="${m.joined ? 'true' : 'false'}" data-status="${r?.status || 'idle'}" class="su-card">
              <div class="su-cardhead">
                <span class="su-os" aria-hidden="true">${osIcon(m.os)}</span>
                <span class="su-grow"><span class="su-name">${esc(m.name)}</span>
                  ${m.online ? '' : '<span class="su-sub"><span class="su-chip su-chip-warn">offline</span></span>'}</span>
                ${pill}
              </div>
              ${body}
            </section>`
        })
        .join('')

      const ready = chosen().filter((m) => results.get(m.name)?.status === 'ok')
      return {
        body: `
          <h1>Connect your machines</h1>
          <p class="su-lede">Paste one line on each machine. That's it.</p>
          <div class="su-list">${cards}</div>
          <p data-error class="su-err hidden"></p>`,
        foot: `<p data-hint class="su-hint ${ready.length ? 'hidden' : ''}">${ready.length ? '' : 'Waiting for a machine to connect'}</p>
          <button data-act="back" class="su-btn su-btn-ghost">Back</button>
          <button data-act="finish" ${ready.length ? '' : 'disabled'} class="su-btn su-btn-primary su-grow">Done</button>`,
      }
    }

    // autoTest tests every machine whose one-line join has come in and not
    // been tested yet, so the person never has to press anything.
    function autoTest() {
      if (step !== 'access') return
      for (const m of machines()) {
        if (m.selected && m.ready && !results.has(m.name)) results.set(m.name, { status: 'ok' })
        if (!m.joined) continue
        if (!picks.has(m.name)) picks.set(m.name, { user: m.user, method: m.method })
        const p = picks.get(m.name)
        if (m.user) p.user = m.user
        p.method = m.method || 'key'
        manual.delete(m.name)
        const key = m.joinedAt || 'joined'
        if (!m.sshd || testedJoin.get(m.name) === key) continue
        if (results.get(m.name)?.status === 'testing') continue
        testedJoin.set(m.name, key)
        test(m.name)
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
            <p class="su-lede">Your machines are one tap away, from any device on your Tailscale network.
            Add more any time from <b>•••</b> → Machines.</p>
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

      const views = { welcome: viewWelcome, machines: viewMachines, access: viewAccess, test: viewTest, done: viewDone }
      const v = views[step]()
      const ready = (state.https || {}).on && location.protocol === 'http:' && step !== 'done' ? httpsNote() : ''
      const at = step === 'welcome' ? `welcome-${page}` : step
      const entering = root.dataset.at !== at
      root.dataset.at = at
      root.dataset.step = step
      root.classList.toggle('su-enter', entering)
      root.innerHTML = `${header()}
        <main class="su-main">${ready}${v.body}</main>
        <footer class="su-foot"><div class="su-foot-in">${v.foot}</div></footer>`
      if (keepFocus) root.scrollTop = scroll

      // Arriving at the accounts step, put the cursor where it is needed.
      if (entering && step === 'access') {
        const empty = [...root.querySelectorAll('[data-user]')].find((i) => !i.value)
        if (empty) setTimeout(() => empty.focus({ preventScroll: false }), 350)
      }
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
      if (t.dataset.manual) {
        manual.add(t.dataset.manual)
        return render()
      }
      if (t.dataset.auto) {
        manual.delete(t.dataset.auto)
        return render()
      }
      if (t.dataset.retest) {
        // A manual setup is saved first, so the test uses what was typed.
        if (manual.has(t.dataset.retest)) {
          try {
            await save()
          } catch (err) {
            const el = root.querySelector('[data-error]')
            if (el) {
              el.textContent = err.message
              el.classList.remove('hidden')
            }
            return
          }
        }
        return test(t.dataset.retest)
      }
      if (t.dataset.trust) {
        const r = results.get(t.dataset.trust)
        return test(t.dataset.trust, r?.fingerprint || '')
      }

      switch (t.dataset.act) {
        case 'close':
          return finish(null)
        case 'continue':
          if (page < 2) page++
          else step = 'machines'
          root.scrollTop = 0
          return render()
        case 'prev':
          if (page > 0) page--
          return render()
        case 'skip':
          step = 'machines'
          return render()
        case 'back':
          step = step === 'test' ? 'access' : 'machines'
          return render()
        case 'next':
          if (step === 'machines') {
            step = 'access'
            try { await save() } catch { /* saved again before testing */ }
            autoTest()
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
      e.target.classList.toggle('is-empty', !e.target.value.trim())
      // Only the footer depends on the typed account; re-rendering the whole
      // step on each keystroke would be wasteful and jumpy.
      const btn = root.querySelector(`[data-retest="${CSS.escape(name)}"].su-btn-primary`)
      if (btn) btn.disabled = !e.target.value.trim()
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
