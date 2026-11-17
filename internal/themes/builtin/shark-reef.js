// Shark Reef — blue reef water seen from above. Grey reef sharks made
// of letter tiles bend and sweep as they swim, a hammerhead patrols, a manta
// glides over the reef, a school of small fish scatters when a shark comes
// close, and once in a while a spotted whale shark drifts across.
//
// Every tile carries a letter, denser where the body is darker
// (o . : → o s t → t x k → x X k → X S 8 → S # 8), so a shark reads as shaded
// type. Where a shark has just been, its tiles fade out as faint dots: a wake.
//
// Each open tab owns a shark: it swims faster while its agent prints, comes to
// circle the middle when the agent rings the bell or finishes, and on
// papaya-say the sender's shark swims over to the receiver's. A cue scatters
// the school and sends every shark off at once in a cloud of bubbles.
//
// Technique: each shark follows its own trail (the path its head took), so the
// body bends through turns; samples along the bent spine are snapped to the
// tile grid, and every tile is stamped from a small pre-drawn atlas.

let CELL = 11
const GAP = 1.4
// Letters per shade level (light → dark), three variants each.
const GL = ['o.:', 'ost', 'txk', 'xXk', 'XS8', 'S#8']
// Tile grey per level, and the tints a few tiles pick up (grey, rose, lilac, sea).
const TILE = [222, 200, 176, 146, 112, 80]
// Tints: shark slate, rose, lilac, sea green, sand.
const TINT = [[-6, 0, 8], [30, -14, -6], [10, -10, 30], [-24, 16, 4], [22, 10, -26]]
const GHOST = '.-o:'

function hash(x, y) {
  let h = (x * 374761393 + y * 668265263) | 0
  h = (h ^ (h >>> 13)) * 1274126177
  return ((h ^ (h >>> 16)) >>> 0) / 4294967296
}
const c255 = (v) => (v < 0 ? 0 : v > 255 ? 255 : v | 0)

// The atlas: rows 0–4 are the tints, columns are level × variant; row 5 holds
// the wake's ghost glyphs, row 6 fish (> <) and a bubble ring.
function buildAtlas(s) {
  const st = s.state
  const T = Math.max(4, Math.round(CELL * s.dpr))
  const c = document.createElement('canvas')
  c.width = T * 18
  c.height = T * 7
  const g = c.getContext('2d')
  const inset = Math.max(1, Math.round((GAP * s.dpr) / 2))
  g.font = `700 ${Math.round(T * 0.66)}px ui-monospace, Menlo, Consolas, monospace`
  g.textAlign = 'center'
  g.textBaseline = 'middle'
  const tileAt = (x, y, lv, ti, ch) => {
    const tt = TINT[ti], b = TILE[lv]
    const r = c255(b + tt[0]), gg = c255(b + tt[1]), bb = c255(b + tt[2])
    g.fillStyle = `rgb(${r},${gg},${bb})`
    g.fillRect(x + inset, y + inset, T - inset * 2, T - inset * 2)
    const ink = lv >= 4 ? 92 : -78
    g.fillStyle = `rgb(${c255(r + ink)},${c255(gg + ink)},${c255(bb + ink)})`
    g.fillText(ch, x + T / 2, y + T / 2 + T * 0.04)
  }
  for (let ti = 0; ti < 5; ti++) {
    for (let lv = 0; lv < 6; lv++) for (let v = 0; v < 3; v++) tileAt((lv * 3 + v) * T, ti * T, lv, ti, GL[lv][v])
  }
  g.fillStyle = '#b8dcea'
  for (let v = 0; v < 4; v++) g.fillText(GHOST[v], v * T + T / 2, 5 * T + T / 2)
  tileAt(0, 6 * T, 0, 4, '>')
  tileAt(T, 6 * T, 0, 4, '<')
  g.strokeStyle = 'rgba(220,245,255,0.9)'
  g.lineWidth = Math.max(1, s.dpr)
  g.beginPath()
  g.arc(2 * T + T / 2, 6 * T + T / 2, T * 0.28, 0, 6.283)
  g.stroke()
  st.atlas = c
  st.T = T
}

function stamp(ctx, st, col, row, x, y) {
  ctx.drawImage(st.atlas, col * st.T, row * st.T, st.T, st.T, x * CELL, y * CELL, CELL, CELL)
}
const tileCol = (lv, va) => lv * 3 + va

// ---------------------------------------------------------------- the reef

function backdrop(s) {
  const st = s.state, W = st.W, H = st.H
  const c = document.createElement('canvas')
  c.width = Math.ceil(s.w * s.dpr)
  c.height = Math.ceil(s.h * s.dpr)
  const g = c.getContext('2d')
  g.scale(s.dpr, s.dpr)
  const grad = g.createLinearGradient(0, 0, 0, s.h)
  grad.addColorStop(0, '#2a86ad')
  grad.addColorStop(0.55, '#17648c')
  grad.addColorStop(1, '#0d4566')
  g.fillStyle = grad
  g.fillRect(0, 0, s.w, s.h)
  // A soft vignette: brighter in the middle, deeper at the edges.
  const vg = g.createRadialGradient(s.w / 2, s.h * 0.45, Math.min(s.w, s.h) * 0.2, s.w / 2, s.h * 0.45, Math.max(s.w, s.h) * 0.75)
  vg.addColorStop(0, 'rgba(120,210,235,0.12)')
  vg.addColorStop(1, 'rgba(4,24,44,0.35)')
  g.fillStyle = vg
  g.fillRect(0, 0, s.w, s.h)
  g.fillStyle = 'rgba(0,30,50,0.12)'
  for (let x = 0; x < s.w; x += CELL) g.fillRect(x, 0, 1, s.h)
  for (let y = 0; y < s.h; y += CELL) g.fillRect(0, y, s.w, 1)

  // Reef formations rising from a sandy floor.
  const r = s.rand(23)
  const floor = H - 2
  st.surface = new Array(W).fill(floor)
  const cells = new Map()
  const put = (x, y, lv, ti) => {
    if (x < 0 || x >= W || y < 0 || y >= H) return
    cells.set(y * W + x, [lv, ti])
    if (y < st.surface[x]) st.surface[x] = y
  }
  let x = Math.floor(r() * 3)
  while (x < W) {
    const kind = r()
    if (kind < 0.32) {
      // Brain coral: a dome.
      const w = 4 + Math.floor(r() * 5), hg = Math.max(2, Math.round(w * 0.5)), ti = r() < 0.5 ? 1 : 2
      for (let i = 0; i < w; i++) {
        const f = (i - (w - 1) / 2) / (w / 2)
        const hh = Math.max(1, Math.round(hg * Math.sqrt(Math.max(0, 1 - f * f))))
        for (let j = 1; j <= hh; j++) put(x + i, floor - j, j === hh ? 1 : hash(x + i, j) > 0.5 ? 2 : 3, ti)
      }
      x += w + Math.floor(r() * 3)
    } else if (kind < 0.6) {
      // Staghorn: branches that wander up and fork.
      const ti = r() < 0.5 ? 3 : 2, top = st.reefH + 1
      const tips = [{ x: x + 1, y: floor - 1 }, { x: x + 2, y: floor - 1 }]
      for (let n = 0; n < 70 && tips.length; n++) {
        const tp = tips.shift()
        put(tp.x, tp.y, tp.y < floor - top * 0.6 ? 1 : 2, ti)
        if (floor - tp.y >= top) continue
        const nx = tp.x + (r() < 0.35 ? (r() < 0.5 ? -1 : 1) : 0)
        tips.push({ x: nx, y: tp.y - 1 })
        if (r() < 0.3 && tips.length < 6) tips.push({ x: tp.x + (r() < 0.5 ? -1 : 1), y: tp.y - 1 })
      }
      x += 4 + Math.floor(r() * 3)
    } else if (kind < 0.8) {
      // Sea fan: a lattice that widens upward.
      const hg = Math.min(st.reefH, 4 + Math.floor(r() * 3)), ti = 1
      put(x + 3, floor - 1, 3, 0)
      for (let j = 2; j <= hg + 1; j++) {
        const half = Math.round(((j - 1) / hg) * 3)
        for (let i = -half; i <= half; i++) if (((i + j) & 1) === 0 || j === hg + 1) put(x + 3 + i, floor - j, j > hg - 1 ? 1 : 2, ti)
      }
      x += 8 + Math.floor(r() * 2)
    } else {
      // A low rock.
      const w = 3 + Math.floor(r() * 4)
      for (let i = 0; i < w; i++) {
        const hh = i === 0 || i === w - 1 ? 1 : 1 + Math.floor(r() * 2)
        for (let j = 1; j <= hh; j++) put(x + i, floor - j, 3, 0)
      }
      x += w + 1 + Math.floor(r() * 3)
    }
  }
  for (let y = floor; y < H; y++) for (let x2 = 0; x2 < W; x2++) cells.set(y * W + x2, [hash(x2, y) > 0.86 ? 1 : 0, 4])
  // The atlas is drawn at device pixels; stamp it 1:1 into the backdrop.
  for (const [k, [lv, ti]] of cells) {
    const cx = k % W, cy = (k / W) | 0
    g.drawImage(st.atlas, tileCol(lv, (hash(cx, cy + 7) * 3) | 0) * st.T, ti * st.T, st.T, st.T, cx * CELL, cy * CELL, CELL, CELL)
  }
  st.backdrop = c

  // Anemones that sway on the reef.
  st.anem = []
  const n = Math.max(3, Math.round(W / 22))
  for (let i = 0; i < n; i++) {
    const ax = Math.floor(((i + 0.5) / n) * W + (r() - 0.5) * 6)
    if (ax < 0 || ax >= W) continue
    st.anem.push({ x: ax, ti: r() < 0.5 ? 1 : 3, ph: r() * 6.28, arms: 3 + Math.floor(r() * 2), len: 2 + Math.floor(r() * 3) })
  }
}

// --------------------------------------------------------------- creatures

function newShark(st, kind, i, r) {
  const L = kind === 'whale' ? st.L * 2.1 : st.L * (0.9 + r() * 0.2)
  return {
    kind, id: i, len: L, half: L * (kind === "whale" ? 0.1 : 0.075),
    x: 0, y: 0, a: r() * 6.283, turnRate: 0, dash: 0, phase: r() * 6.28,
    speed: kind === 'whale' ? 1.1 : 2.4 + r() * 0.6,
    goal: null, trail: [], sp: [], tab: null, circle: null, chase: null, chaseUntil: 0, active: true,
  }
}

function placeShark(sh, x, y, a) {
  sh.x = x; sh.y = y; sh.a = a
  sh.trail = []
  for (let d = 0.5; d < sh.len * 1.3; d += 0.5) sh.trail.push({ x: x - Math.cos(a) * d, y: y - Math.sin(a) * d })
}

function randomGoal(st, r) {
  return { x: -st.W * 0.05 + r() * st.W * 1.1, y: 3 + r() * Math.max(4, st.reefTop - 7) }
}

// The spine: points every half tile from the nose back along the trail, with
// a swimming wave that grows toward the tail.
function buildSpine(sh) {
  const pts = sh.trail
  const n = Math.ceil((sh.len * 1.22) / 0.5) + 2
  let px = sh.x, py = sh.y, idx = 0, start = 0
  let qx = pts[0].x, qy = pts[0].y, seg = Math.hypot(qx - px, qy - py)
  for (let i = 0; i < n; i++) {
    const d = i * 0.5
    while (d > start + seg && idx < pts.length - 1) {
      start += seg; px = qx; py = qy; idx++
      qx = pts[idx].x; qy = pts[idx].y; seg = Math.hypot(qx - px, qy - py)
    }
    let tx = qx - px, ty = qy - py
    const tl = Math.hypot(tx, ty) || 1
    tx /= tl; ty /= tl
    const along = d - start
    const x = px + tx * along, y = py + ty * along
    const u = d / sh.len
    const off = sh.amp * Math.sin(u * 5.2 - sh.phase) * (0.15 + u)
    const p = sh.sp[i] || (sh.sp[i] = {})
    p.x = x - ty * off; p.y = y + tx * off; p.nx = -ty; p.ny = tx
  }
  sh.spN = n
}

function at(sh, u, v, out) {
  const f = Math.max(0, Math.min(sh.spN - 1.001, (u * sh.len) / 0.5))
  const i = f | 0, k = f - i, a = sh.sp[i], b = sh.sp[i + 1]
  const nx = a.nx + (b.nx - a.nx) * k, ny = a.ny + (b.ny - a.ny) * k
  out.x = a.x + (b.x - a.x) * k + nx * v
  out.y = a.y + (b.y - a.y) * k + ny * v
  return out
}

function bodyHalf(u, sh) {
  const m = sh.half
  if (sh.kind === 'whale') {
    if (u < 0.05) return m * (0.75 + (u / 0.05) * 0.2)
    if (u < 0.28) return m * 0.95 + m * 0.05 * (u / 0.28)
    if (u < 0.84) return m * (1 - ((u - 0.28) / 0.56) * 0.76)
    return m * 0.24
  }
  if (u < 0.06) return m * (0.35 + (u / 0.06) * 0.35)
  if (u < 0.3) return m * (0.7 + ((u - 0.06) / 0.24) * 0.3)
  if (u < 0.82) return m * (1 - ((u - 0.3) / 0.52) * 0.78)
  return m * 0.22 * (1 - ((u - 0.82) / 0.18) * 0.4)
}

const P = { x: 0, y: 0 }

function mark(st, x, y, lv, ti, va, pr) {
  const cx = Math.floor(x), cy = Math.floor(y)
  if (cx < 0 || cy < 0 || cx >= st.W || cy >= st.H) return
  const k = cy * st.W + cx
  if (st.prio[k] < 0) st.touched.push(k)
  else if (pr <= st.prio[k]) return
  st.prio[k] = pr; st.lv[k] = lv; st.ti[k] = ti; st.va[k] = va
}

function sharkMark(st, sh, u, v, lv, pr) {
  at(sh, u, v, P)
  const hu = Math.round(u * sh.len * 2), hv = Math.round(v * 2)
  const h = hash(hu + sh.id * 97, hv + 40)
  const tint = h > 0.955 ? 1 : h > 0.92 ? 2 : 0
  mark(st, P.x, P.y, lv, tint, (hash(hu + 5, hv + sh.id * 31) * 3) | 0, pr === undefined ? lv : pr)
}

// A fin: a triangle in body coordinates (u along, v across), root a–b, tip c.
function fin(st, sh, au, av, bu, bv, cu, cv, lvRoot, lvTip) {
  const size = Math.max(Math.hypot((cu - au) * sh.len, cv - av), Math.hypot((bu - au) * sh.len, bv - av))
  const n = Math.max(2, Math.ceil(size / 0.4))
  for (let i = 0; i <= n; i++) {
    const p = i / n
    for (let j = 0; j <= n; j++) {
      const q = j / n
      const ru = au + (bu - au) * q, rv = av + (bv - av) * q
      sharkMark(st, sh, ru + (cu - ru) * p, rv + (cv - rv) * p, p < 0.5 ? lvRoot : lvTip)
    }
  }
}

function rasterShark(st, sh) {
  const h = sh.half, du = 0.4 / sh.len
  for (let u = 0; u <= 0.96; u += du) {
    const hw = bodyHalf(u, sh)
    for (let v = -hw; v <= hw + 0.01; v += 0.45) {
      const rr = Math.abs(v) / Math.max(0.01, hw)
      let lv = rr < 0.4 ? 4 : rr < 0.78 ? 3 : 2
      if (u < 0.05 || u > 0.84) lv = Math.min(lv, 3)
      let pr = lv
      if (sh.kind !== 'whale' && u > 0.33 && u < 0.47 && Math.abs(v) < Math.max(0.6, h * 0.3)) { lv = 5; pr = 5 }
      if (sh.kind === 'whale' && rr < 0.85 && hash(Math.round(u * sh.len * 1.1) + 300, Math.round(v * 1.1) + 77) > 0.78) { lv = 1; pr = 6 }
      sharkMark(st, sh, u, v, lv, pr)
    }
  }
  // Pectoral fins, pelvic fins and a forked tail.
  for (const sd of [-1, 1]) {
    fin(st, sh, 0.27, sd * h * 0.9, 0.38, sd * h * 0.85, 0.5, sd * h * (sh.kind === "whale" ? 1.9 : 2.1), 3, 2)
    fin(st, sh, 0.64, sd * h * 0.45, 0.7, sd * h * 0.4, 0.75, sd * h * 1.15, 3, 2)
  }
  fin(st, sh, 0.93, h * 0.15, 0.93, -h * 0.15, 1.17, h * 0.85, 3, 3)
  fin(st, sh, 0.93, h * 0.15, 0.93, -h * 0.15, 1.08, -h * 0.6, 3, 2)
  if (sh.kind === 'hammer') {
    // The hammer: a bar across the head with an eye at each end.
    for (let v = -h * 2.4; v <= h * 2.4; v += 0.45) {
      const e = Math.abs(v) / (h * 2.4)
      sharkMark(st, sh, 0.012 + e * e * 0.03, v, e > 0.85 ? 5 : 3, e > 0.85 ? 7 : 3)
      sharkMark(st, sh, 0.035 + e * e * 0.03, v, 3, 3)
    }
  } else {
    for (const sd of [-1, 1]) sharkMark(st, sh, 0.085, sd * bodyHalf(0.085, sh) * 0.7, 5, 7)
  }
}

function rasterRay(st, ry, t) {
  const ca = Math.cos(ry.a), sa = Math.sin(ry.a)
  const m = (f, g, lv, pr) => {
    const h = hash(Math.round(f * 2) + 900, Math.round(g * 2))
    mark(st, ry.x + ca * f - sa * g, ry.y + sa * f + ca * g, lv, h > 0.94 ? 2 : 0, (h * 3) | 0, pr === undefined ? lv : pr)
  }
  const span = ry.span * (0.72 + 0.28 * Math.cos(t * 1.4 + ry.ph))
  const L = ry.len
  for (let f = -L * 0.5; f <= L * 0.5; f += 0.4) {
    const back = (f + L * 0.5) / L
    const w = span * (back < 0.62 ? Math.pow(back / 0.62, 0.7) : 1 - (back - 0.62) / 0.38) * 1
    // The wing tips trail behind as the manta flaps.
    for (let g = -w; g <= w + 0.01; g += 0.45) {
      const e = Math.abs(g) / Math.max(0.01, span)
      const sweep = -e * e * L * 0.35
      m(f + sweep, g, e < 0.25 ? 4 : e < 0.6 ? 3 : 2)
    }
  }
  for (const sd of [-1, 1]) m(L * 0.56, sd * span * 0.16, 4)
  for (let f = -L * 0.5; f > -L * 1.35; f -= 0.4) m(f, Math.sin(t * 2 + f * 0.4) * 0.3, 3)
}

function steer(sh, gx, gy, dt, maxTurn) {
  let turn = Math.atan2(gy - sh.y, gx - sh.x) - sh.a
  turn = Math.atan2(Math.sin(turn), Math.cos(turn))
  const want = Math.max(-maxTurn, Math.min(maxTurn, turn * 1.2))
  sh.turnRate += (want - sh.turnRate) * Math.min(1, dt * 1.5)
  sh.a += sh.turnRate * dt
}

function moveShark(st, sh, dt, t) {
  const v = sh.speed * (1 + sh.dash * 0.9) * (sh.chase ? 1.4 : 1)
  sh.x += Math.cos(sh.a) * v * dt
  sh.y += Math.sin(sh.a) * v * dt
  sh.phase += dt * (2.0 + v * 0.55)
  sh.amp = sh.half * (0.55 + Math.min(0.5, Math.abs(sh.turnRate)) * 0.6)
  sh.dash = Math.max(0, sh.dash - dt * 0.22)
  const last = sh.trail[0]
  if (Math.hypot(sh.x - last.x, sh.y - last.y) >= 0.35) {
    sh.trail.unshift({ x: sh.x, y: sh.y })
    let acc = 0
    for (let i = 1; i < sh.trail.length; i++) {
      acc += Math.hypot(sh.trail[i].x - sh.trail[i - 1].x, sh.trail[i].y - sh.trail[i - 1].y)
      if (acc > sh.len * 1.3 + 2) { sh.trail.length = i + 1; break }
    }
  }
}

function bubbles(st, x, y, n) {
  for (let i = 0; i < n; i++) st.bub.push({ x: x + (st.rnd() - 0.5) * 3, y: y + (st.rnd() - 0.5) * 2, v: 2 + st.rnd() * 3, w: st.rnd() * 6.28, life: 2.5 + st.rnd() * 2 })
  if (st.bub.length > 180) st.bub.splice(0, st.bub.length - 180)
}

function assignTabs(s) {
  s.state.sharks.forEach((sh, i) => { sh.tab = s.tabs[i] || null })
}

papaya.scene({
  setup(s) {
    const st = s.state
    st.rnd = s.rand(7)
    st.bub = []
    st.touched = []
    st.whale = null
    st.nextWhale = 50 + st.rnd() * 40
  },

  resize(s) {
    const st = s.state, r = st.rnd
    const phone = s.w < 700
    CELL = phone ? 8 : 11
    st.W = Math.ceil(s.w / CELL)
    st.H = Math.ceil(s.h / CELL)
    st.L = phone ? 17 : 25
    st.reefH = Math.max(5, Math.round(st.H * 0.12))
    st.reefTop = st.H - st.reefH
    const N = st.W * st.H
    st.prio = new Int8Array(N).fill(-1)
    st.lv = new Uint8Array(N); st.ti = new Uint8Array(N); st.va = new Uint8Array(N)
    st.fade = new Float32Array(N)
    st.touched = []
    buildAtlas(s)
    backdrop(s)

    const want = phone ? ['reef', 'hammer', 'reef'] : ['reef', 'hammer', 'reef', 'reef']
    if (!st.sharks || st.sharks.length !== want.length || st.sharkL !== st.L) {
      st.sharkL = st.L
      st.sharks = want.map((k, i) => newShark(st, k, i, r))
      for (const sh of st.sharks) {
        const g = randomGoal(st, r)
        placeShark(sh, Math.max(4, Math.min(st.W - 4, g.x)), g.y, sh.a)
        sh.goal = randomGoal(st, r)
      }
      assignTabs(s)
    } else {
      for (const sh of st.sharks) sh.goal = randomGoal(st, r)
    }
    st.whale = null

    st.ray = {
      x: st.W * 0.3, y: st.reefTop - 2, a: 0, ph: 0, dir: 1,
      span: phone ? 4.5 : 6.5, len: phone ? 5 : 7,
    }
    const sc = { x: st.W * 0.7, y: st.reefTop * 0.4, a: 3.14, goal: randomGoal(st, r), flee: 0, fish: [] }
    for (let i = 0; i < (phone ? 10 : 15); i++) {
      const ox = (r() - 0.5) * 7, oy = (r() - 0.5) * 4
      sc.fish.push({ ox, oy, x: sc.x + ox, y: sc.y + oy, ph: r() * 6.28 })
    }
    st.school = sc
    st.plank = []
    const np = Math.round((st.W * st.H) / 45)
    for (let i = 0; i < np; i++) {
      const x = r() * st.W, y = r() * st.reefTop
      const edge = Math.max(Math.abs(x / st.W - 0.5), Math.abs(y / st.H - 0.45)) * 2
      if (r() > 0.2 + edge * edge * 0.8) continue
      st.plank.push({ x, y, dash: r() < 0.6, a: 0.25 + r() * 0.4, ph: r() * 6.28 })
    }
  },

  init(s) { assignTabs(s) },

  on(s, ev) {
    const st = s.state
    if (!st.sharks) return
    if (ev.type === 'tab-open' || ev.type === 'tab-close' || ev.type === 'tab-rename') return assignTabs(s)
    const own = st.sharks.find((sh) => sh.tab && sh.tab.id === ev.id)
    const named = (n) => {
      const w = String(n || '').toLowerCase().replace(/^papaya-/, '')
      return st.sharks.find((sh) => sh.tab && sh.tab.name && sh.tab.name.toLowerCase().startsWith(w))
    }
    const circle = (sh) => {
      sh.circle = { cx: st.W / 2, cy: st.reefTop * 0.48, r: Math.max(6, Math.min(st.W, st.reefTop) * 0.24), until: s.t + 14 }
      sh.dash = Math.max(sh.dash, 0.6)
    }
    if (ev.type === 'activity' && own) own.dash = 1
    if (ev.type === 'focus' && own) own.dash = Math.max(own.dash, 0.4)
    if ((ev.type === 'bell' || ev.type === 'done') && own) { circle(own); bubbles(st, own.x, own.y, 6) }
    if (ev.type === 'say') {
      const from = named(ev.from), to = named(ev.to)
      if (from && to && from !== to) { from.chase = to; from.chaseUntil = s.t + 14; from.circle = null; bubbles(st, from.x, from.y, 5) }
      else if (to) { to.dash = 1; bubbles(st, to.x, to.y, 8) }
    }
    if (ev.type === 'cue') {
      for (const sh of st.sharks) { sh.dash = 1; sh.circle = null; sh.goal = randomGoal(st, st.rnd); bubbles(st, sh.x, sh.y, 10) }
      st.school.flee = 1.5
      st.school.goal = randomGoal(st, st.rnd)
    }
  },

  draw(s) {
    const st = s.state, ctx = s.ctx
    if (!st.backdrop || !st.sharks) return
    const W = st.W, H = st.H, dt = s.dt, t = s.t, r = st.rnd

    // ---- move
    for (const sh of st.sharks) {
      let gx = sh.goal.x, gy = sh.goal.y, maxTurn = 0.62
      if (sh.chase) {
        gx = sh.chase.x; gy = sh.chase.y; maxTurn = 0.9
        if (Math.hypot(gx - sh.x, gy - sh.y) < sh.len * 0.5 || t > sh.chaseUntil) {
          if (t <= sh.chaseUntil) { bubbles(st, sh.x, sh.y, 10); sh.chase.dash = 1 }
          sh.chase = null; sh.goal = randomGoal(st, r)
        }
      } else if (sh.circle) {
        const c = sh.circle
        const ang = Math.atan2(sh.y - c.cy, sh.x - c.cx) + 0.7
        gx = c.cx + Math.cos(ang) * c.r; gy = c.cy + Math.sin(ang) * c.r * 0.8
        maxTurn = 0.9
        if (t > c.until) { sh.circle = null; sh.goal = randomGoal(st, r) }
      } else if (Math.hypot(gx - sh.x, gy - sh.y) < 4) sh.goal = randomGoal(st, r)
      // Keep clear of the other sharks, so two never swim through each other.
      if (!sh.chase) {
        for (const o of st.sharks) {
          if (o === sh) continue
          const ax = sh.x - o.x, ay = sh.y - o.y, d = Math.hypot(ax, ay)
          if (d < sh.len * 0.8 && d > 0.01) { gx = sh.x + (ax / d) * sh.len; gy = sh.y + (ay / d) * sh.len; maxTurn = 0.8 }
        }
      }
      if (sh.y > st.reefTop - 3) gy = Math.min(gy, st.reefTop - 6)
      steer(sh, gx, gy, dt, maxTurn)
      moveShark(st, sh, dt, t)
    }

    // A whale shark drifts across now and then.
    if (!st.whale && t > st.nextWhale) {
      const wh = newShark(st, 'whale', 9, r)
      const fromLeft = r() < 0.5, y = st.reefTop * (0.3 + r() * 0.35)
      placeShark(wh, fromLeft ? -2 : W + 2, y, fromLeft ? 0.05 : Math.PI - 0.05)
      wh.goal = { x: fromLeft ? W + wh.len * 2 : -wh.len * 2, y: y + (r() - 0.5) * 8 }
      st.whale = wh
    }
    if (st.whale) {
      const wh = st.whale
      steer(wh, wh.goal.x, wh.goal.y, dt, 0.15)
      moveShark(st, wh, dt, t)
      const tail = wh.trail[wh.trail.length - 1]
      if ((wh.x > W + 2 && tail.x > W + 1) || (wh.x < -2 && tail.x < -1)) { st.whale = null; st.nextWhale = t + 150 + r() * 150 }
    }

    // The manta glides along above the reef and turns at the edges.
    const ry = st.ray
    ry.ph += dt
    const want = ry.dir > 0 ? 0 : Math.PI
    let da = want + Math.sin(t * 0.13) * 0.18 - ry.a
    da = Math.atan2(Math.sin(da), Math.cos(da))
    ry.a += da * Math.min(1, dt * 0.6)
    const rv = 1.3 + Math.max(0, Math.cos(t * 1.4 + ry.ph)) * 0.6
    ry.x += Math.cos(ry.a) * rv * dt
    ry.y = st.reefTop - ry.span * 0.35 + Math.sin(t * 0.2) * 1.2 - 1
    if (ry.x > W + ry.len * 2) ry.dir = -1
    if (ry.x < -ry.len * 2) ry.dir = 1

    // The school wanders, and scatters from any shark that comes close.
    const sc = st.school
    let near = null, nd = 1e9
    for (const sh of st.sharks.concat(st.whale ? [st.whale] : [])) {
      const d = Math.hypot(sh.x - sc.x, sh.y - sc.y)
      if (d < nd) { nd = d; near = sh }
    }
    if (near && nd < 8) {
      sc.flee = Math.max(sc.flee, 1)
      const ax = sc.x - near.x, ay = sc.y - near.y, al = Math.hypot(ax, ay) || 1
      sc.goal = { x: Math.max(2, Math.min(W - 2, sc.x + (ax / al) * 16)), y: Math.max(2, Math.min(st.reefTop - 3, sc.y + (ay / al) * 12)) }
    }
    if (Math.hypot(sc.goal.x - sc.x, sc.goal.y - sc.y) < 3) sc.goal = randomGoal(st, r)
    {
      let turn = Math.atan2(sc.goal.y - sc.y, sc.goal.x - sc.x) - sc.a
      turn = Math.atan2(Math.sin(turn), Math.cos(turn))
      sc.a += Math.max(-1.2, Math.min(1.2, turn * 1.5)) * dt
      const v = 1.8 + sc.flee * 4
      sc.x += Math.cos(sc.a) * v * dt
      sc.y += Math.sin(sc.a) * v * dt
      sc.flee = Math.max(0, sc.flee - dt * 0.5)
    }

    // ---- stamp creatures into the grid
    for (const sh of st.sharks) { buildSpine(sh); rasterShark(st, sh) }
    rasterRay(st, ry, t)
    if (st.whale) { buildSpine(st.whale); rasterShark(st, st.whale) }

    // ---- draw
    ctx.drawImage(st.backdrop, 0, 0, s.w, s.h)

    // Light playing on the water near the surface.
    const lit = Math.floor(st.reefTop * 0.55)
    for (let y = 0; y < lit; y++) {
      const fall = 1 - y / lit
      for (let x = (y & 1); x < W; x += 2) {
        const n = s.noise(x * 0.1 + t * 0.06, y * 0.1 - t * 0.04)
        if (n > 0.78) {
          ctx.fillStyle = `rgba(200,245,255,${((n - 0.78) * 0.55 * fall).toFixed(3)})`
          ctx.fillRect(x * CELL + GAP / 2, y * CELL + GAP / 2, CELL - GAP, CELL - GAP)
        }
      }
    }

    // Plankton: specks and dashes drifting, thicker toward the edges.
    for (const p of st.plank) {
      p.x += (s.noise(p.x * 0.05, t * 0.1 + p.ph) - 0.5) * 0.6 * dt
      p.y -= 0.06 * dt
      if (p.y < 0) p.y = st.reefTop
      const a = p.a * (0.7 + 0.3 * Math.sin(t * 0.8 + p.ph))
      ctx.fillStyle = `rgba(210,240,250,${a.toFixed(3)})`
      const px = Math.round(p.x * CELL), py = Math.round(p.y * CELL)
      if (p.dash) ctx.fillRect(px, py, Math.round(CELL * 0.4), 1)
      else ctx.fillRect(px, py, 1.5, 1.5)
    }

    // Anemones sway along the reef top.
    for (const an of st.anem) {
      const base = st.surface[an.x]
      for (let k = 0; k < an.arms; k++) {
        const x0 = an.x + k - (an.arms >> 1)
        for (let i = 1; i <= an.len; i++) {
          const x = Math.round(x0 + Math.sin(t * 0.9 + an.ph + k * 0.7 - i * 0.5) * i * 0.35)
          stamp(ctx, st, tileCol(i === an.len ? 0 : 1, (k + i) % 3), an.ti, x, base - i)
        }
      }
    }

    // The fish.
    const face = Math.cos(sc.a) >= 0 ? 0 : 1
    for (const f of sc.fish) {
      const spread = 1 + sc.flee * 1.8
      const tx = sc.x + f.ox * spread + Math.sin(t * 1.7 + f.ph) * 0.5
      const ty = sc.y + f.oy * spread + Math.cos(t * 1.3 + f.ph) * 0.4
      f.x += (tx - f.x) * Math.min(1, dt * 3)
      f.y += (ty - f.y) * Math.min(1, dt * 3)
      stamp(ctx, st, face, 6, Math.floor(f.x), Math.floor(f.y))
    }

    // The wake: tiles a shark just left fade out as faint dots.
    const dec = Math.exp(-dt * 1.5)
    const N = W * H, fade = st.fade, prio = st.prio
    for (let k = 0; k < N; k++) {
      if (prio[k] >= 0) { fade[k] = 1; continue }
      let f = fade[k]
      if (f < 0.04) continue
      f *= dec
      fade[k] = f
      if (f > 0.92) continue
      ctx.globalAlpha = f * 0.55
      const x = k % W, y = (k / W) | 0
      stamp(ctx, st, (hash(x, y) * 4) | 0, 5, x, y)
    }
    ctx.globalAlpha = 1

    for (const k of st.touched) {
      stamp(ctx, st, tileCol(st.lv[k], st.va[k]), st.ti[k], k % W, (k / W) | 0)
      prio[k] = -1
    }
    st.touched.length = 0

    // Bubbles rise and wobble.
    st.bub = st.bub.filter((b) => {
      b.y -= b.v * dt; b.life -= dt
      ctx.globalAlpha = Math.min(0.85, b.life * 0.45)
      stamp(ctx, st, 2, 6, Math.round(b.x + Math.sin(t * 2 + b.w) * 0.6), Math.round(b.y))
      return b.life > 0 && b.y > 0
    })
    ctx.globalAlpha = 1
  },
})
