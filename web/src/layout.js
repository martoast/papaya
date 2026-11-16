// Split-tree layout model for the grid view.
//
// A layout is a binary tree. Leaves hold a session id; splits hold a direction,
// a ratio, and two children. This is the same model tmux and editor split panes
// use, and it is what makes arbitrary "one big, three small" arrangements fall
// out naturally: resizing is just moving one ratio, and every panel stays
// rectangular with no overlap and no gaps.
//
// Everything here is pure. The DOM lives in grid.js.

export const MIN_RATIO = 0.12
export const MAX_RATIO = 0.88

export function leaf(id) {
  return { type: 'leaf', id }
}

export function split(dir, a, b, ratio = 0.5) {
  return { type: 'split', dir, ratio, a, b }
}

export function isLeaf(node) {
  return Boolean(node) && node.type === 'leaf'
}

// ids lists every session in the tree, left to right, top to bottom.
export function ids(node) {
  if (!node) return []
  if (isLeaf(node)) return [node.id]
  return [...ids(node.a), ...ids(node.b)]
}

export function count(node) {
  return ids(node).length
}

export function findLeaf(node, id) {
  if (!node) return null
  if (isLeaf(node)) return node.id === id ? node : null
  return findLeaf(node.a, id) || findLeaf(node.b, id)
}

// add inserts a new session by splitting an existing panel.
//
// Splitting the focused panel is what a person expects: the new terminal appears
// where they were looking. The direction alternates with the panel's shape so
// repeated adds tile sensibly instead of producing ten slivers.
export function add(node, id, { target = null, dir = null } = {}) {
  if (!node) return leaf(id)
  if (findLeaf(node, id)) return node // already present

  const targetId = target && findLeaf(node, target) ? target : firstId(node)
  return replaceLeaf(node, targetId, (found) =>
    split(dir || nextDir(node, targetId), found, leaf(id)),
  )
}

// remove drops a session and collapses its parent split into the sibling.
export function remove(node, id) {
  if (!node) return null
  if (isLeaf(node)) return node.id === id ? null : node

  const a = remove(node.a, id)
  const b = remove(node.b, id)

  if (!a) return b
  if (!b) return a
  if (a === node.a && b === node.b) return node
  return { ...node, a, b }
}

// swap exchanges the positions of two sessions.
export function swap(node, idA, idB) {
  if (!node || idA === idB) return node
  if (isLeaf(node)) {
    if (node.id === idA) return leaf(idB)
    if (node.id === idB) return leaf(idA)
    return node
  }
  return { ...node, a: swap(node.a, idA, idB), b: swap(node.b, idA, idB) }
}

// setRatio adjusts one split, identified by its path from the root.
export function setRatio(node, path, ratio) {
  const clamped = Math.min(MAX_RATIO, Math.max(MIN_RATIO, ratio))
  return updateAtPath(node, path, (n) => ({ ...n, ratio: clamped }))
}

// balance resets every ratio, undoing accumulated dragging.
export function balance(node) {
  if (!node || isLeaf(node)) return node
  return { ...node, ratio: 0.5, a: balance(node.a), b: balance(node.b) }
}

// fromIds builds a reasonable starting layout for a set of sessions.
//
// Splitting the list in half and recursing produces a balanced tiling: two
// sessions side by side, four as a 2x2, and so on, rather than a staircase.
export function fromIds(list) {
  if (!list.length) return null
  if (list.length === 1) return leaf(list[0])

  const mid = Math.ceil(list.length / 2)
  const dir = list.length > 2 ? 'row' : 'row'
  return split(dir, fromIds(list.slice(0, mid)), fromIds(list.slice(mid)))
}

// grid builds a layout that tiles into roughly square cells, which reads better
// than repeated halving once there are more than a few sessions.
export function grid(list) {
  if (!list.length) return null
  if (list.length === 1) return leaf(list[0])

  const cols = Math.ceil(Math.sqrt(list.length))
  const rows = []
  for (let i = 0; i < list.length; i += cols) {
    rows.push(list.slice(i, i + cols))
  }

  const rowNode = (cells) =>
    cells.length === 1 ? leaf(cells[0]) : evenSplit('row', cells.map(leaf))

  return rows.length === 1 ? rowNode(rows[0]) : evenSplit('col', rows.map(rowNode))
}

// prune drops sessions that no longer exist and adds ones that are missing, so a
// restored layout survives sessions being closed elsewhere.
export function reconcile(node, liveIds) {
  const live = new Set(liveIds)

  let next = node
  for (const id of ids(node)) {
    if (!live.has(id)) next = remove(next, id)
  }

  const present = new Set(ids(next))
  for (const id of liveIds) {
    if (!present.has(id)) next = add(next, id)
  }
  return next
}

// --- internals ---

function firstId(node) {
  return isLeaf(node) ? node.id : firstId(node.a)
}

// nextDir alternates against the depth of the target so tiles stay squarish.
function nextDir(node, id) {
  const depth = leafDepth(node, id, { row: 0, col: 0 })
  if (!depth) return 'row'
  return depth.row > depth.col ? 'col' : 'row'
}

function leafDepth(node, id, acc) {
  if (!node) return null
  if (isLeaf(node)) return node.id === id ? acc : null

  const bump = (side) =>
    leafDepth(node[side], id, {
      row: acc.row + (node.dir === 'row' ? 1 : 0),
      col: acc.col + (node.dir === 'col' ? 1 : 0),
    })

  return bump('a') || bump('b')
}

function replaceLeaf(node, id, fn) {
  if (!node) return node
  if (isLeaf(node)) return node.id === id ? fn(node) : node
  return { ...node, a: replaceLeaf(node.a, id, fn), b: replaceLeaf(node.b, id, fn) }
}

function updateAtPath(node, path, fn) {
  if (!node) return node
  if (!path.length) return fn(node)

  const [head, ...rest] = path
  return { ...node, [head]: updateAtPath(node[head], rest, fn) }
}

function evenSplit(dir, nodes) {
  if (nodes.length === 1) return nodes[0]
  const mid = Math.ceil(nodes.length / 2)
  return split(dir, evenSplit(dir, nodes.slice(0, mid)), evenSplit(dir, nodes.slice(mid)))
}

// --- persistence ---

const STORAGE_KEY = 'papaya.layout.v1'

// saveLayout records the arrangement, stamping each leaf with the workspace its
// session is attached to.
//
// The tree is keyed by session id because that is what the DOM works in, but an
// id is worthless the moment papaya restarts: every session gets a new one, and
// a restored layout would find none of its leaves alive, drop them all, and
// rebuild a default tiling. The workspace is the handle that survives, so it is
// what the arrangement is really pinned to. The id is kept alongside it, since
// it is still the faster match for an ordinary reload.
export function saveLayout(node, workspaceFor) {
  const stamp = (n) => {
    if (!n) return null
    if (isLeaf(n)) {
      const w = workspaceFor ? workspaceFor(n.id) : ''
      return w ? { type: 'leaf', id: n.id, w } : { type: 'leaf', id: n.id }
    }
    return { ...n, a: stamp(n.a), b: stamp(n.b) }
  }

  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(stamp(node)))
  } catch {
    // Private browsing or a full quota; the layout simply will not persist.
  }
}

// rehydrate maps a stored tree onto the sessions that exist right now.
//
// Leaves that resolve to nothing are dropped and their splits collapse into the
// surviving sibling, so a layout thins out gracefully rather than holding empty
// panels for work that is gone.
export function rehydrate(node, sessions) {
  if (!node) return null

  const byWorkspace = new Map()
  const liveIds = new Set()
  for (const s of sessions || []) {
    if (!s?.id) continue
    liveIds.add(s.id)
    if (s.workspace) byWorkspace.set(s.workspace, s.id)
  }

  const walk = (n) => {
    if (!n) return null
    if (isLeaf(n)) {
      // Workspace first. The id a leaf was saved with belonged to a process
      // that may since have died, while the workspace outlived it.
      const id = (n.w && byWorkspace.get(n.w)) || (liveIds.has(n.id) ? n.id : null)
      return id ? leaf(id) : null
    }

    const a = walk(n.a)
    const b = walk(n.b)
    if (!a) return b
    if (!b) return a
    return { ...n, a, b }
  }

  return walk(node)
}

export function loadLayout() {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return null
    const parsed = JSON.parse(raw)
    return isValid(parsed) ? parsed : null
  } catch {
    return null
  }
}

// isValid rejects anything that is not a well-formed tree, so a corrupted or
// hand-edited entry cannot break rendering.
function isValid(node) {
  if (!node || typeof node !== 'object') return false
  if (node.type === 'leaf') {
    if (typeof node.id !== 'string' || node.id.length === 0) return false
    // The workspace stamp is optional — a layout saved before it existed, or a
    // session with tmux off, simply has none.
    return node.w === undefined || typeof node.w === 'string'
  }
  if (node.type !== 'split') return false
  if (node.dir !== 'row' && node.dir !== 'col') return false
  if (typeof node.ratio !== 'number' || !Number.isFinite(node.ratio)) return false
  return isValid(node.a) && isValid(node.b)
}
