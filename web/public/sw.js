// A service worker with exactly one job: explain why the page will not load.
//
// papaya lives on a tailnet address. With Tailscale switched off there is no
// route to it at all, so the browser never reaches the server and shows its own
// "this site can't be reached" page — which says nothing about Tailscale, and
// looks identical to the server being down. This worker answers that one
// navigation from cache instead, with a page that names the actual cause.
//
// It deliberately caches NOTHING else. Caching the app bundle would mean a
// deploy could be shadowed by a stale build, and a terminal has no offline mode
// worth serving anyway: assets and API calls must keep failing honestly so the
// app's own reconnect logic sees the failure and explains it in place.

const CACHE = 'papaya-offline-v2'
const FALLBACK = '/offline.html'
const ASSETS = [FALLBACK, '/offline.js']

// How long to wait for the server before deciding it is not there.
//
// This is the whole point of the worker, and getting it wrong in either
// direction is what made the first version useless. An unroutable tailnet
// address does not refuse the connection — nothing answers at all, so the
// request hangs until the TCP stack gives up, which on macOS is over a minute.
// A fetch that never settles never rejects, so waiting for a rejection means
// waiting exactly as long as having no worker at all.
//
// Over WireGuard on a tailnet the real page answers in well under 100ms, so
// this is generous. A slow-but-working connection that trips it anyway is not
// stranded: the fallback page immediately probes /healthz and returns to the
// app the moment it answers.
const NETWORK_TIMEOUT_MS = 4000

self.addEventListener('install', (e) => {
  e.waitUntil(
    caches
      .open(CACHE)
      .then((c) => c.addAll(ASSETS))
      .then(() => self.skipWaiting()),
  )
})

self.addEventListener('activate', (e) => {
  e.waitUntil(
    caches
      .keys()
      .then((keys) => Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  )
})

self.addEventListener('fetch', (e) => {
  // Page loads only. Returning without calling respondWith leaves everything
  // else on the browser's default path, as though this worker did not exist.
  if (e.request.mode !== 'navigate') return

  e.respondWith(navigateOrExplain(e.request))
})

async function navigateOrExplain(request) {
  // AbortController rather than a bare race: without it the abandoned request
  // stays open, holding a connection the browser could otherwise reuse when
  // the tailnet returns.
  const control = new AbortController()
  const timer = setTimeout(() => control.abort(), NETWORK_TIMEOUT_MS)

  try {
    return await fetch(request, { signal: control.signal })
  } catch {
    return (await caches.match(FALLBACK)) || syntheticFallback()
  } finally {
    clearTimeout(timer)
  }
}

// The cache should always hold the real page. This exists so that a worker
// whose cache was evicted replaces one unhelpful browser error with a useful
// one, rather than with a different unhelpful browser error.
function syntheticFallback() {
  return new Response(
    '<!doctype html><meta charset="utf-8"><title>papaya unreachable</title>' +
      '<body style="font:14px ui-monospace,monospace;background:#0b0d10;color:#e6e6e6;padding:2rem">' +
      "<h1 style=\"font-size:1rem\">Can't reach papaya</h1>" +
      '<p>This terminal is only reachable over your tailnet. Turn on Tailscale, then reload.</p>',
    { status: 503, headers: { 'Content-Type': 'text/html; charset=utf-8' } },
  )
}
