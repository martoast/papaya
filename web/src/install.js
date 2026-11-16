// Putting Papaya on the home screen, so it opens like an app: full screen, its
// own icon, no address bar. On a phone that is the whole point of Papaya.
//
// Chrome and Edge (Android and desktop) offer a real one-click install through
// beforeinstallprompt; that event is caught and an "Install app" button shown.
// Safari on iPhone and iPad has no such API: nothing a page does can add it to
// the home screen, it is always Share → Add to Home Screen. So there the button
// opens a short guide that shows exactly those taps.

const DISMISSED = 'papaya.install.dismissed.v1'

const isStandalone = () =>
  window.matchMedia?.('(display-mode: standalone)').matches || window.navigator.standalone === true

// iPadOS reports itself as a Mac; a Mac with a touchscreen is an iPad.
const isIOS = () =>
  /iPhone|iPad|iPod/.test(navigator.userAgent) ||
  (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1)

const SHARE_ICON =
  '<svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 15V3"/><path d="M8 7l4-4 4 4"/><path d="M6 11H5a1 1 0 00-1 1v8a1 1 0 001 1h14a1 1 0 001-1v-8a1 1 0 00-1-1h-1"/></svg>'
const ADD_ICON =
  '<svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" aria-hidden="true"><rect x="4" y="4" width="16" height="16" rx="4"/><path d="M12 8v8M8 12h8"/></svg>'

let deferred = null

function show(button, on) {
  button.classList.toggle('hidden', !on)
  button.classList.toggle('inline-flex', on)
}

function remember(key) {
  try {
    localStorage.setItem(key, String(Date.now()))
  } catch {
    // Private mode: the guide may show again, which is harmless.
  }
}

function dismissed() {
  try {
    return Boolean(localStorage.getItem(DISMISSED))
  } catch {
    return false
  }
}

export function showIOSGuide() {
  if (document.getElementById('install-guide')) return
  const sheet = document.createElement('div')
  sheet.id = 'install-guide'
  sheet.setAttribute('role', 'dialog')
  sheet.setAttribute('aria-label', 'Add Papaya to your Home Screen')
  sheet.innerHTML = `
    <div class="ig-card">
      <button class="ig-close" aria-label="Not now">×</button>
      <div class="ig-head">
        <img src="/apple-touch-icon.png" alt="" width="44" height="44">
        <div><b>Put Papaya on your Home Screen</b><span>It opens full screen, like an app.</span></div>
      </div>
      <ol>
        <li><span class="ig-n">1</span>Tap <span class="ig-key">${SHARE_ICON} Share</span> in Safari's toolbar <em>(or under •••)</em></li>
        <li><span class="ig-n">2</span>Choose <span class="ig-key">${ADD_ICON} Add to Home Screen</span></li>
        <li><span class="ig-n">3</span>Tap <span class="ig-key">Add</span></li>
      </ol>
    </div>
    <div class="ig-arrow" aria-hidden="true">↓</div>`
  const close = () => {
    remember(DISMISSED)
    sheet.remove()
  }
  sheet.querySelector('.ig-close').addEventListener('click', close)
  sheet.addEventListener('click', (e) => {
    if (e.target === sheet) close()
  })
  document.body.appendChild(sheet)
}

// initInstall wires the status-bar button and, on an iPhone that has not seen
// it, shows the guide once after a short pause.
export function initInstall(button) {
  if (!button || isStandalone()) return

  window.addEventListener('beforeinstallprompt', (e) => {
    e.preventDefault()
    deferred = e
    show(button, true)
  })
  window.addEventListener('appinstalled', () => {
    deferred = null
    show(button, false)
  })

  if (isIOS()) {
    show(button, true)
    if (!dismissed()) setTimeout(() => !isStandalone() && showIOSGuide(), 4000)
  }

  button.addEventListener('click', async () => {
    if (deferred) {
      deferred.prompt()
      await deferred.userChoice.catch(() => {})
      deferred = null
      show(button, false)
    } else {
      showIOSGuide()
    }
  })
}
