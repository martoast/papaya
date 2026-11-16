// Turning voice input on, from the mic button itself.
//
// Voice needs an OpenAI API key. Rather than send people to a server's
// environment variables, the first tap on the mic asks for it here: get a key,
// paste, done. Papaya checks it with OpenAI and keeps it on the server; it is
// never stored in, or sent back to, the browser.

import { api } from './api.js'

const KEYS_URL = 'https://platform.openai.com/api-keys'

export function showVoiceSetup({ onEnabled } = {}) {
  if (document.getElementById('voice-setup')) return
  const sheet = document.createElement('div')
  sheet.id = 'voice-setup'
  sheet.setAttribute('role', 'dialog')
  sheet.setAttribute('aria-label', 'Turn on voice input')
  sheet.innerHTML = `
    <form class="vs-card" novalidate>
      <button type="button" class="vs-close" aria-label="Not now">×</button>
      <div class="vs-icon" aria-hidden="true">🎙️</div>
      <h2>Talk instead of typing</h2>
      <p class="vs-lede">Tap the mic, speak, and your words land in the terminal. Papaya uses
        OpenAI's speech-to-text, so it needs an OpenAI API key, once.</p>
      <ol class="vs-steps">
        <li><a href="${KEYS_URL}" target="_blank" rel="noopener">Create a key on OpenAI ↗</a></li>
        <li>Paste it here</li>
      </ol>
      <input name="key" type="password" inputmode="text" autocomplete="off" autocapitalize="off"
        autocorrect="off" spellcheck="false" enterkeyhint="done" placeholder="sk-…" aria-label="OpenAI API key">
      <p class="vs-err" hidden></p>
      <button type="submit" class="vs-go">Turn on voice</button>
      <p class="vs-fine">The key stays on your Papaya server and is never shown again. Costs are
        a fraction of a cent per message, billed by OpenAI.</p>
    </form>`
  const form = sheet.querySelector('form')
  const input = form.querySelector('input')
  const err = form.querySelector('.vs-err')
  const go = form.querySelector('.vs-go')
  const close = () => sheet.remove()
  sheet.querySelector('.vs-close').addEventListener('click', close)
  sheet.addEventListener('click', (e) => {
    if (e.target === sheet) close()
  })
  sheet.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') close()
  })
  form.addEventListener('submit', async (e) => {
    e.preventDefault()
    const key = input.value.trim()
    if (!key) return input.focus()
    err.hidden = true
    go.disabled = true
    go.textContent = 'Checking with OpenAI…'
    try {
      await api.setVoiceKey(key)
      go.textContent = '✓ Voice is on'
      form.classList.add('is-done')
      setTimeout(() => {
        close()
        onEnabled?.()
      }, 900)
    } catch (ex) {
      err.textContent = ex.message || 'That did not work. Try again.'
      err.hidden = false
      go.disabled = false
      go.textContent = 'Turn on voice'
    }
  })
  document.body.appendChild(sheet)
  setTimeout(() => input.focus(), 50)
}
