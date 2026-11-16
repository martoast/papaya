// Poll until papaya answers, then go straight back to the app.
//
// Reconnecting Tailscale is the fix, and it happens in another window entirely.
// Requiring the user to also remember to come back and reload is how a working
// connection still looks broken.

const status = document.getElementById('status')
const retry = document.getElementById('retry')

const POLL_MS = 3000

// Each probe needs its own deadline. An unroutable tailnet address does not
// refuse the connection, it simply never answers, so a fetch without a timeout
// hangs for as long as the TCP stack allows — leaving this page stuck on
// "checking…" with no retry ever scheduled.
const PROBE_TIMEOUT_MS = 4000

let tries = 0
let timer = null

async function probe() {
  clearTimeout(timer)
  tries += 1
  status.textContent = 'checking…'

  const control = new AbortController()
  const deadline = setTimeout(() => control.abort(), PROBE_TIMEOUT_MS)

  try {
    // /healthz is the one route that needs no identity, so it answers as soon
    // as there is any route to the server at all.
    const res = await fetch('/healthz', { cache: 'no-store', signal: control.signal })
    if (res.ok) {
      // replace, not assign: the offline page should not sit in history where
      // Back would land on it again.
      location.replace('/')
      return
    }
  } catch {
    // Still no route. Fall through and try again.
  } finally {
    clearTimeout(deadline)
  }

  status.textContent = `still unreachable · checked ${tries}×`
  timer = setTimeout(probe, POLL_MS)
}

retry.addEventListener('click', probe)

// The tailnet coming up often shows here first.
window.addEventListener('online', probe)

probe()
