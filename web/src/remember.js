// What was open, remembered in this browser.
//
// Reloading already restored the tabs, because the server holds the session
// list — but only while the server is up. That list lives in memory and dies
// with the process, so a papaya restart emptied the tab bar even though every
// remote shell was still running in tmux, untouched. Getting back in meant
// recreating a tab with exactly the original workspace name, from memory.
//
// Session ids are useless for this: they die with the same process. The durable
// handle is the workspace — the tmux session name on the remote host, which
// outlives papaya entirely. Remember that, and a tab can be put back on the
// very shell it was on before.
//
// This is a hint, never an authority. Everything here is checked against what
// is actually still running before it is used, so a stale entry can only ever
// be dropped, never resurrected as an empty tab pretending to be old work.

const KEY = 'papaya.tabs.v1'

// A browser that has been away for a month is not resuming a work session, and
// the workspaces it remembers have almost certainly been recycled by then.
const MAX_AGE_MS = 30 * 24 * 60 * 60 * 1000

// The last value written, so the periodic sweep that keeps this current does
// not write on every tick when nothing has changed.
let lastWritten = null

export function rememberTabs(tabs) {
  const entries = []
  for (const t of tabs) {
    const s = t.session
    // No workspace means no durable handle — tmux is off, or the shell never
    // started. Remembering it would only produce a tab attached to nothing.
    if (!s?.workspace) continue
    entries.push({
      host: s.host,
      user: s.user,
      workspace: s.workspace,
      name: s.name || '',
      order: s.order ?? 0,
    })
  }

  // Compare the entries alone, not the timestamp, or every tick would differ.
  const fingerprint = JSON.stringify(entries)
  if (fingerprint === lastWritten) return
  lastWritten = fingerprint

  try {
    if (!entries.length) {
      localStorage.removeItem(KEY)
      return
    }
    localStorage.setItem(KEY, JSON.stringify({ at: Date.now(), entries }))
  } catch {
    // Private mode, or the quota is full. Losing the hint costs a click.
  }
}

export function recallTabs() {
  let raw = null
  try {
    raw = localStorage.getItem(KEY)
  } catch {
    return []
  }
  if (!raw) return []

  let saved
  try {
    saved = JSON.parse(raw)
  } catch {
    forgetTabs()
    return []
  }

  if (!saved?.entries?.length || !Number.isFinite(saved.at)) return []
  if (Date.now() - saved.at > MAX_AGE_MS) {
    forgetTabs()
    return []
  }

  return saved.entries
    .filter((e) => e && typeof e.host === 'string' && typeof e.workspace === 'string')
    .sort((a, b) => (a.order ?? 0) - (b.order ?? 0))
}

export function forgetTabs() {
  try {
    localStorage.removeItem(KEY)
  } catch {
    // nothing to clean up
  }
}
