// Is papaya itself reachable?
//
// Every connection failure looks identical from the browser: a socket that will
// not open, a fetch that rejects. The WebSocket API never exposes the failed
// handshake's status code, so a 404 for a session the server has forgotten is
// indistinguishable from the tailnet being down — and the two need opposite
// responses. One is permanent and retrying it is pointless; the other is
// transient, the user can fix it, and they need to be told how.
//
// /healthz is the probe. It is the one route outside the identity middleware,
// so it answers whenever the process is reachable at all.

const PROBE_PATH = '/healthz'

// Slow enough not to matter, fast enough that reconnecting Tailscale clears the
// banner while the user is still looking at it.
const POLL_MS = 4000

// A probe must fail on a deadline, not on a rejection. Leaving the tailnet does
// not refuse connections — the address simply stops being routable and requests
// hang until the TCP stack gives up, which is far longer than anyone will sit
// looking at a terminal that appears frozen. Without this the banner would wait
// out that hang, which is exactly the silence it exists to break.
const PROBE_TIMEOUT_MS = 4000

class Reachability {
  constructor() {
    // Assume reachable until something says otherwise: the app has just been
    // served, so it plainly was a moment ago.
    this.reachable = true
    this.listeners = new Set()
    this.inFlight = null
    this.pollTimer = null

    // Losing Wi-Fi and leaving the tailnet are the same event here. Both mean
    // the last answer is no longer worth trusting.
    window.addEventListener('offline', () => this.set(false))
    window.addEventListener('online', () => this.check())
  }

  // subscribe is called immediately with the current value, so a caller does
  // not have to duplicate the initial render.
  subscribe(fn) {
    this.listeners.add(fn)
    fn(this.reachable)
    return () => this.listeners.delete(fn)
  }

  // check collapses concurrent callers onto a single request. Every open tab
  // reconnects at the same moment, and ten simultaneous probes would tell us
  // nothing that one does.
  check() {
    if (this.inFlight) return this.inFlight
    this.inFlight = this.probe().finally(() => {
      this.inFlight = null
    })
    return this.inFlight
  }

  async probe() {
    const control = new AbortController()
    const deadline = setTimeout(() => control.abort(), PROBE_TIMEOUT_MS)

    try {
      const res = await fetch(PROBE_PATH, { cache: 'no-store', signal: control.signal })
      this.set(res.ok)
      return res.ok
    } catch {
      // Rejected or timed out: either way there is no usable route to the
      // server. When Tailscale is off nothing answers a 100.x address at all.
      this.set(false)
      return false
    } finally {
      clearTimeout(deadline)
    }
  }

  set(reachable) {
    if (reachable === this.reachable) {
      // Still down: keep polling, so the banner clears itself the moment the
      // tailnet returns rather than waiting for a reload.
      if (!reachable) this.schedulePoll()
      return
    }

    this.reachable = reachable
    if (reachable) clearTimeout(this.pollTimer)
    else this.schedulePoll()

    for (const fn of this.listeners) fn(reachable)
  }

  schedulePoll() {
    clearTimeout(this.pollTimer)
    this.pollTimer = setTimeout(() => this.check(), POLL_MS)
  }
}

export const reachability = new Reachability()
