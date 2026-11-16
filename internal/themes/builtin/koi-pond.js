// Koi Pond — a mosaic pond. Koi with patterned bodies, fins and swaying
// tails swim mostly along the edges, under lily pads, leaving the middle calm
// for text. Each open tab owns a koi: it darts while its agent prints, sends
// out a ripple when the agent rings the bell, and on papaya-say the sender's
// koi swims over to the receiver's. A cue ripples the whole pond.
//
// Technique: shapes are drawn smoothly into a canvas one pixel per tile, then
// every opaque pixel is laid as a square tile with a gap, so the pond reads as
// mosaic. Water is a cached gradient with faint tile lines and moving caustics.

// Tile size: smaller on a phone, so koi keep their proportions on a narrow screen.
let CELL = 10
const GAP = 1.5

// Body colours from head to tail.
const PATTERNS = [
  ['#f7f3ec', '#e8542a', '#e8542a', '#f7f3ec', '#f7f3ec', '#e8542a', '#e8542a', '#f7f3ec', '#f7f3ec', '#f7f3ec', '#f7f3ec', '#f7f3ec'], // kohaku
  ['#ff8f2e', '#ff8f2e', '#ffa64d', '#ff8f2e', '#ff8f2e', '#ffa64d', '#ff8f2e', '#ff8f2e', '#ffb366', '#ffb366', '#ffc285', '#ffc285'], // ogon
  ['#f7f3ec', '#f7f3ec', '#1d1d22', '#e8542a', '#f7f3ec', '#1d1d22', '#f7f3ec', '#e8542a', '#f7f3ec', '#1d1d22', '#f7f3ec', '#f7f3ec'], // sanke
  ['#d63a2a', '#f7f3ec', '#f7f3ec', '#f7f3ec', '#f7f3ec', '#f7f3ec', '#f7f3ec', '#f7f3ec', '#f7f3ec', '#f7f3ec', '#f7f3ec', '#f7f3ec'], // tancho
  ['#1d1d22', '#1d1d22', '#ff8f2e', '#1d1d22', '#ff8f2e', '#ff8f2e', '#1d1d22', '#1d1d22', '#ff8f2e', '#1d1d22', '#1d1d22', '#1d1d22'], // showa
]
const WIDTH = [1.9, 2.6, 3.0, 3.1, 3.0, 2.7, 2.35, 2.0, 1.6, 1.25, 0.95, 0.7]

function hash(x, y) {
  let h = (x * 374761393 + y * 668265263) | 0
  h = (h ^ (h >>> 13)) * 1274126177
  return ((h ^ (h >>> 16)) >>> 0) / 4294967296
}

// A goal near the edges: the left or right fifth, or the bottom or top band.
function edgeGoal(st, r) {
  const side = Math.floor(r() * 4)
  const W = st.W, H = st.H
  if (side === 0) return { x: r() * W * 0.2, y: r() * H }
  if (side === 1) return { x: W * (0.8 + r() * 0.2), y: r() * H }
  if (side === 2) return { x: r() * W, y: H * (0.8 + r() * 0.2) }
  return { x: r() * W, y: r() * H * 0.15 }
}

function placePads(st) {
  const W = st.W, H = st.H
  st.pads = [
    { x: W * 0.04, y: H * 0.1, r: 4.5, f: true }, { x: W * 0.12, y: H * 0.05, r: 3 },
    { x: W * 0.07, y: H * 0.86, r: 5, f: false }, { x: W * 0.16, y: H * 0.95, r: 3.2, f: true },
    { x: W * 0.93, y: H * 0.14, r: 4, f: false }, { x: W * 0.97, y: H * 0.3, r: 2.6 },
    { x: W * 0.9, y: H * 0.9, r: 5.5, f: true }, { x: W * 0.8, y: H * 0.97, r: 3 },
    { x: W * 0.55, y: H * 1.0, r: 3.4 },
  ].map((p, i) => ({ ...p, notch: (i * 1.7) % 6.28, ph: i }))
}

function water(s) {
  const st = s.state
  const c = document.createElement('canvas')
  c.width = Math.ceil(s.w)
  c.height = Math.ceil(s.h)
  const g = c.getContext('2d')
  const grad = g.createLinearGradient(0, 0, s.w, s.h)
  grad.addColorStop(0, '#1d6f68')
  grad.addColorStop(0.5, '#135955')
  grad.addColorStop(1, '#0b3d3d')
  g.fillStyle = grad
  g.fillRect(0, 0, s.w, s.h)
  // A soft light pool in the middle, where the text is.
  const pool = g.createRadialGradient(s.w * 0.45, s.h * 0.45, 0, s.w * 0.45, s.h * 0.45, Math.max(s.w, s.h) * 0.6)
  pool.addColorStop(0, 'rgba(160,230,215,0.10)')
  pool.addColorStop(1, 'rgba(0,0,0,0)')
  g.fillStyle = pool
  g.fillRect(0, 0, s.w, s.h)
  // Faint tile grout.
  g.fillStyle = 'rgba(0,0,0,0.10)'
  for (let x = 0; x < s.w; x += CELL) g.fillRect(x, 0, 1, s.h)
  for (let y = 0; y < s.h; y += CELL) g.fillRect(0, y, s.w, 1)
  st.water = c
}

function assign(s) {
  s.state.fish.forEach((f, i) => { f.tab = s.tabs[i] || null })
}

function fishFor(s, name) {
  const want = String(name || '').toLowerCase().replace(/^papaya-/, '')
  return s.state.fish.find((f) => f.tab && f.tab.name && f.tab.name.toLowerCase() === want) ||
    s.state.fish.find((f) => f.tab && f.tab.name && f.tab.name.toLowerCase().startsWith(want))
}

function drawFish(lc, f, t) {
  // Spine from the head backwards, bending with a travelling wave.
  const pts = []
  let x = f.x, y = f.y, a = f.a
  const wave = t * (1.1 + f.dash * 0.9) + f.phase
  for (let i = 0; i < WIDTH.length; i++) {
    pts.push([x, y, a])
    const bend = Math.sin(wave - i * 0.5) * 0.045 * (1 + i * 0.22)
    a += bend
    x -= Math.cos(a) * 1.3
    y -= Math.sin(a) * 1.3
  }
  // Shadow on the pond floor.
  lc.fillStyle = 'rgba(4,30,30,0.55)'
  for (let i = 0; i < pts.length; i += 2) {
    lc.beginPath(); lc.arc(pts[i][0] + 1.6, pts[i][1] + 2.2, WIDTH[i] * 0.95, 0, 6.283); lc.fill()
  }
  // Pectoral fins.
  const [fx, fy, fa] = pts[2]
  lc.fillStyle = 'rgba(255,245,235,0.85)'
  for (const side of [-1, 1]) {
    const sa = fa + side * 1.9 + Math.sin(wave * 0.8) * 0.2 * side
    lc.beginPath(); lc.ellipse(fx + Math.cos(sa) * 2.6, fy + Math.sin(sa) * 2.6, 1.6, 0.9, sa, 0, 6.283); lc.fill()
  }
  // Tail fin, a fan behind the last point.
  const [tx, ty, ta] = pts[pts.length - 1]
  lc.fillStyle = 'rgba(255,240,228,0.8)'
  for (const side of [-1, 1]) {
    const sa = ta + Math.PI + side * 0.45 + Math.sin(wave - 6) * 0.3
    lc.beginPath(); lc.ellipse(tx + Math.cos(sa) * 1.6, ty + Math.sin(sa) * 1.6, 1.9, 0.9, sa, 0, 6.283); lc.fill()
  }
  // Body, tail first so the head sits on top.
  for (let i = pts.length - 1; i >= 0; i--) {
    lc.fillStyle = f.pat[i]
    lc.beginPath(); lc.arc(pts[i][0], pts[i][1], WIDTH[i], 0, 6.283); lc.fill()
  }
  // Eyes.
  const [hx, hy, ha] = pts[0]
  lc.fillStyle = '#111'
  for (const side of [-1, 1]) lc.fillRect(hx + Math.cos(ha + side * 0.9) * 1.1 - 0.5, hy + Math.sin(ha + side * 0.9) * 1.1 - 0.5, 1, 1)
}

papaya.scene({
  setup(s) {
    const st = s.state
    st.low = document.createElement('canvas')
    st.lc = st.low.getContext('2d', { willReadFrequently: true })
    st.rnd = s.rand(5)
    st.fish = PATTERNS.map((pat, i) => ({
      x: 0, y: 0, a: st.rnd() * 6.283, pat, phase: st.rnd() * 6.283,
      speed: 0.55 + st.rnd() * 0.35, turnRate: 0, dash: 0, goal: null, chase: null, tab: null, seeded: false, i,
    }))
    st.ripples = []
  },

  resize(s) {
    const st = s.state
    CELL = s.w < 700 ? 7 : 10
    st.W = Math.ceil(s.w / CELL)
    st.H = Math.ceil(s.h / CELL)
    st.low.width = st.W
    st.low.height = st.H
    placePads(st)
    water(s)
    for (const f of st.fish) {
      if (!f.seeded) { const g = edgeGoal(st, st.rnd); f.x = g.x; f.y = g.y; f.seeded = true }
      f.goal = edgeGoal(st, st.rnd)
    }
  },

  init(s) { assign(s) },

  on(s, ev) {
    const st = s.state
    if (ev.type === 'tab-open' || ev.type === 'tab-close' || ev.type === 'tab-rename') return assign(s)
    const own = st.fish.find((f) => f.tab && f.tab.id === ev.id)
    if (ev.type === 'activity' && own) own.dash = 1
    if (ev.type === 'focus' && own) own.dash = Math.max(own.dash, 0.5)
    if (ev.type === 'bell' && own) st.ripples.push({ x: own.x, y: own.y, t: 0 })
    if (ev.type === 'say') {
      const from = fishFor(s, ev.from), to = fishFor(s, ev.to)
      if (from && to && from !== to) { from.chase = to; from.dash = 1 }
      else if (to) { to.dash = 1; st.ripples.push({ x: to.x, y: to.y, t: 0 }) }
    }
    if (ev.type === 'cue') {
      for (let i = 0; i < 7; i++) st.ripples.push({ x: st.rnd() * st.W, y: st.rnd() * st.H, t: -i * 0.18 })
      st.fish.forEach((f) => { f.dash = 1 })
    }
  },

  draw(s) {
    const st = s.state, lc = st.lc, ctx = s.ctx
    const W = st.W, H = st.H
    if (!W || !st.water) return
    const dt = s.dt, t = s.t

    // Swim.
    for (const f of st.fish) {
      const goal = f.chase || f.goal
      const dx = goal.x - f.x, dy = goal.y - f.y, d = Math.hypot(dx, dy)
      let turn = Math.atan2(dy, dx) - f.a
      turn = Math.atan2(Math.sin(turn), Math.cos(turn))
      // Eased steering: the turn rate itself changes slowly, so koi swing
      // through wide, lazy arcs instead of snapping toward their goal.
      const want = s.clamp(turn * 0.6, -0.35, 0.35)
      f.turnRate += (want - f.turnRate) * Math.min(1, dt * 0.8)
      f.a += f.turnRate * dt
      const v = f.speed * (1 + f.dash * 1.1) * (f.chase ? 1.6 : 1)
      f.x += Math.cos(f.a) * v * dt
      f.y += Math.sin(f.a) * v * dt
      f.dash = Math.max(0, f.dash - dt * 0.25)
      if (f.chase && d < 3.5) {
        st.ripples.push({ x: f.x, y: f.y, t: 0 })
        f.chase.dash = 1
        f.chase = null
        f.goal = edgeGoal(st, st.rnd)
      } else if (!f.chase && d < 4) f.goal = edgeGoal(st, st.rnd)
    }

    // Shapes, one pixel per tile.
    lc.clearRect(0, 0, W, H)
    for (const f of st.fish) drawFish(lc, f, t)
    for (const p of st.pads) {
      const bob = Math.sin(t * 0.25 + p.ph) * 0.12
      lc.fillStyle = 'rgba(6,40,30,0.5)'
      lc.beginPath(); lc.arc(p.x + 0.8, p.y + 1.2, p.r, 0, 6.283); lc.fill()
      lc.fillStyle = '#3f9a52'
      lc.beginPath(); lc.moveTo(p.x, p.y); lc.arc(p.x, p.y + bob, p.r, p.notch + 0.35, p.notch + 6.0); lc.closePath(); lc.fill()
      lc.fillStyle = '#5ab866'
      lc.beginPath(); lc.arc(p.x - p.r * 0.3, p.y - p.r * 0.3 + bob, p.r * 0.45, 0, 6.283); lc.fill()
      if (p.f) {
        lc.fillStyle = '#ff9fc4'
        lc.beginPath(); lc.arc(p.x + p.r * 0.2, p.y + bob, 1.4, 0, 6.283); lc.fill()
        lc.fillStyle = '#ffe27a'
        lc.fillRect(Math.round(p.x + p.r * 0.2), Math.round(p.y + bob), 1, 1)
      }
    }

    // Lay the tiles.
    ctx.drawImage(st.water, 0, 0)
    // Caustics: a few water tiles catch the light.
    for (let y = 0; y < H; y += 1) {
      for (let x = (y & 1); x < W; x += 2) {
        const n = s.noise(x * 0.11 + t * 0.06, y * 0.11 - t * 0.045)
        if (n > 0.78) {
          ctx.fillStyle = `rgba(190,245,230,${(n - 0.78) * 0.6})`
          ctx.fillRect(x * CELL + GAP / 2, y * CELL + GAP / 2, CELL - GAP, CELL - GAP)
        }
      }
    }
    const img = lc.getImageData(0, 0, W, H).data
    for (let y = 0; y < H; y++) {
      for (let x = 0; x < W; x++) {
        const k = (y * W + x) * 4, a = img[k + 3]
        // Every tile the shapes touch is laid, as opaque as the share of it they
        // cover. As a fish moves, tiles fade in ahead and out behind it, so it
        // glides across the mosaic instead of jumping from cell to cell.
        if (a < 10) continue
        const j = 0.94 + hash(x, y) * 0.12
        const r = Math.min(255, img[k] * j) | 0, g = Math.min(255, img[k + 1] * j) | 0, b = Math.min(255, img[k + 2] * j) | 0
        ctx.fillStyle = a > 245 ? `rgb(${r},${g},${b})` : `rgba(${r},${g},${b},${(a / 255).toFixed(3)})`
        ctx.fillRect(x * CELL + GAP / 2, y * CELL + GAP / 2, CELL - GAP, CELL - GAP)
      }
    }

    // Ripples as tiles.
    st.ripples = st.ripples.filter((rp) => {
      rp.t += dt
      if (rp.t < 0) return true
      const rad = 1 + rp.t * 3.2, alpha = Math.max(0, 0.45 - rp.t * 0.11)
      ctx.fillStyle = `rgba(225,255,248,${alpha})`
      const seen = new Set()
      for (let a = 0; a < 6.283; a += 0.6 / rad) {
        const x = Math.round(rp.x + Math.cos(a) * rad), y = Math.round(rp.y + Math.sin(a) * rad * 0.8)
        const key = x * 9999 + y
        if (seen.has(key)) continue
        seen.add(key)
        ctx.fillRect(x * CELL + GAP / 2, y * CELL + GAP / 2, CELL - GAP, CELL - GAP)
      }
      return rp.t < 4
    })
  },
})
