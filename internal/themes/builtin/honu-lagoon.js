// Honu Lagoon — a mosaic lagoon. Sea turtles paddle through turquoise water,
// kelp sways along the edges, a school of tiny fish wanders, and light plays
// on the sand. Life keeps to the edges; the middle stays calm for text.
//
// Each open tab owns a turtle: it paddles harder while its agent prints, lets
// out a ring of bubbles when the agent rings the bell, and on papaya-say the
// sender's turtle swims over to the receiver's. A cue sends every turtle off
// at once in a cloud of bubbles.
//
// Technique (as in Koi Pond): shapes are drawn one pixel per tile into a small
// canvas, then every touched pixel is laid as a square tile with a gap.

let CELL = 10
const GAP = 1.5

const SHELLS = [
  { shell: '#c8456b', rim: '#f06d8f', scute: '#8f2a4c', skin: '#f3d27a' },
  { shell: '#4f9a5a', rim: '#79c27f', scute: '#2f6b3c', skin: '#e9d79a' },
  { shell: '#c9853a', rim: '#e8ab62', scute: '#8a5520', skin: '#f1dfa6' },
]

function hash(x, y) {
  let h = (x * 374761393 + y * 668265263) | 0
  h = (h ^ (h >>> 13)) * 1274126177
  return ((h ^ (h >>> 16)) >>> 0) / 4294967296
}

function edgeGoal(st, r) {
  const W = st.W, H = st.H
  const side = Math.floor(r() * 4)
  if (side === 0) return { x: r() * W * 0.22, y: H * 0.1 + r() * H * 0.7 }
  if (side === 1) return { x: W * (0.78 + r() * 0.2), y: H * 0.1 + r() * H * 0.7 }
  if (side === 2) return { x: r() * W, y: H * (0.68 + r() * 0.12) }
  return { x: r() * W, y: H * (0.04 + r() * 0.12) }
}

function backdrop(s) {
  const st = s.state
  const c = document.createElement('canvas')
  c.width = Math.ceil(s.w)
  c.height = Math.ceil(s.h)
  const g = c.getContext('2d')
  const grad = g.createLinearGradient(0, 0, s.w, s.h * 0.9)
  grad.addColorStop(0, '#8fdbe6')
  grad.addColorStop(0.45, '#3fb1d6')
  grad.addColorStop(1, '#1679ad')
  g.fillStyle = grad
  g.fillRect(0, 0, s.w, s.h)
  // A light shaft from the top left.
  const shaft = g.createLinearGradient(0, 0, s.w * 0.6, s.h)
  shaft.addColorStop(0, 'rgba(255,255,255,0.18)')
  shaft.addColorStop(0.5, 'rgba(255,255,255,0.0)')
  g.fillStyle = shaft
  g.fillRect(0, 0, s.w, s.h)
  // Grout.
  g.fillStyle = 'rgba(0,40,70,0.10)'
  for (let x = 0; x < s.w; x += CELL) g.fillRect(x, 0, 1, s.h)
  for (let y = 0; y < s.h; y += CELL) g.fillRect(0, y, s.w, 1)
  // Sand: a mosaic band along the bottom, rising in soft mounds.
  for (let x = 0; x < st.W; x++) {
    const top = st.H - 3 - Math.round(1.6 + Math.sin(x * 0.17) * 1.2 + Math.sin(x * 0.05 + 1) * 1.4)
    for (let y = top; y < st.H; y++) {
      const n = hash(x, y)
      const shade = y === top ? '#f4e6b8' : n > 0.85 ? '#d9c38a' : n > 0.5 ? '#e8d6a0' : '#e1cd94'
      g.fillStyle = shade
      g.fillRect(x * CELL + GAP / 2, y * CELL + GAP / 2, CELL - GAP, CELL - GAP)
    }
  }
  st.backdrop = c
}

function placeKelp(st, r) {
  const W = st.W, H = st.H
  st.kelp = []
  const spots = [0.02, 0.05, 0.09, 0.13, 0.86, 0.9, 0.94, 0.97]
  for (const f of spots) {
    st.kelp.push({
      x: Math.round(W * f), len: Math.round(H * (0.18 + r() * 0.22)), ph: r() * 6.28,
      color: r() > 0.45 ? '#e3c34a' : '#3e9a5a', tip: r() > 0.5 ? '#f2dc78' : '#6cc07a',
    })
  }
}

function drawTurtle(lc, tu, t) {
  const k = tu.size
  const ca = Math.cos(tu.a), sa = Math.sin(tu.a)
  const P = (fx, fy) => [tu.x + ca * fx - sa * fy, tu.y + sa * fx + ca * fy]
  const paddle = Math.sin(t * (1.6 + tu.dash * 2.2) + tu.phase)
  // Shadow.
  lc.fillStyle = 'rgba(0,50,80,0.35)'
  lc.beginPath(); lc.ellipse(tu.x + 1.5, tu.y + 2.4, k * 1.3, k * 1.0, tu.a, 0, 6.283); lc.fill()
  // Flippers: front pair sweep wide, back pair small.
  lc.fillStyle = tu.c.skin
  for (const side of [-1, 1]) {
    const [fx, fy] = P(k * 0.45, side * k * 0.95)
    const fa = tu.a + side * (1.1 + paddle * 0.55)
    lc.beginPath(); lc.ellipse(fx + Math.cos(fa) * k * 0.6, fy + Math.sin(fa) * k * 0.6, k * 0.85, k * 0.32, fa, 0, 6.283); lc.fill()
    const [bx, by] = P(-k * 0.75, side * k * 0.7)
    const ba = tu.a + Math.PI - side * (0.7 - paddle * 0.25)
    lc.beginPath(); lc.ellipse(bx + Math.cos(ba) * k * 0.3, by + Math.sin(ba) * k * 0.3, k * 0.42, k * 0.22, ba, 0, 6.283); lc.fill()
  }
  // Head.
  const [hx, hy] = P(k * 1.35 + Math.sin(t * 0.7 + tu.phase) * 0.15, 0)
  lc.beginPath(); lc.ellipse(hx, hy, k * 0.42, k * 0.34, tu.a, 0, 6.283); lc.fill()
  lc.fillStyle = '#1b1b1b'
  for (const side of [-1, 1]) {
    const [ex, ey] = P(k * 1.5, side * k * 0.2)
    lc.fillRect(ex - 0.4, ey - 0.4, 0.8, 0.8)
  }
  // Shell, rim, and scutes.
  lc.fillStyle = tu.c.rim
  lc.beginPath(); lc.ellipse(tu.x, tu.y, k * 1.12, k * 0.92, tu.a, 0, 6.283); lc.fill()
  lc.fillStyle = tu.c.shell
  lc.beginPath(); lc.ellipse(tu.x, tu.y, k * 0.95, k * 0.76, tu.a, 0, 6.283); lc.fill()
  lc.fillStyle = tu.c.scute
  for (const [fx, fy] of [[0.35, 0], [-0.3, 0], [0, 0.38], [0, -0.38]]) {
    const [sx, sy] = P(k * fx, k * fy)
    lc.beginPath(); lc.arc(sx, sy, k * 0.2, 0, 6.283); lc.fill()
  }
}

papaya.scene({
  setup(s) {
    const st = s.state
    st.low = document.createElement('canvas')
    st.lc = st.low.getContext('2d', { willReadFrequently: true })
    st.rnd = s.rand(11)
    st.turtles = SHELLS.map((c, i) => ({
      x: 0, y: 0, a: st.rnd() * 6.283, c, phase: st.rnd() * 6.283, size: 3,
      speed: 0.45 + st.rnd() * 0.25, turnRate: 0, dash: 0, goal: null, chase: null, tab: null, seeded: false, i,
    }))
    st.school = { x: 0, y: 0, a: 0, goal: null, fish: [] }
    for (let i = 0; i < 9; i++) st.school.fish.push({ ox: (st.rnd() - 0.5) * 6, oy: (st.rnd() - 0.5) * 4, ph: st.rnd() * 6.28 })
    st.bubbles = []
  },

  resize(s) {
    const st = s.state
    CELL = s.w < 700 ? 7 : 10
    st.W = Math.ceil(s.w / CELL)
    st.H = Math.ceil(s.h / CELL)
    st.low.width = st.W
    st.low.height = st.H
    const k = s.w < 700 ? 2.6 : 3.2
    for (const tu of st.turtles) {
      tu.size = k
      if (!tu.seeded) { const g = edgeGoal(st, st.rnd); tu.x = g.x; tu.y = g.y; tu.seeded = true }
      tu.goal = edgeGoal(st, st.rnd)
    }
    if (!st.school.goal) { const g = edgeGoal(st, st.rnd); st.school.x = g.x; st.school.y = g.y }
    st.school.goal = edgeGoal(st, st.rnd)
    placeKelp(st, s.rand(4))
    backdrop(s)
  },

  init(s) { s.state.turtles.forEach((tu, i) => { tu.tab = s.tabs[i] || null }) },

  on(s, ev) {
    const st = s.state
    if (ev.type === 'tab-open' || ev.type === 'tab-close' || ev.type === 'tab-rename') {
      st.turtles.forEach((tu, i) => { tu.tab = s.tabs[i] || null })
      return
    }
    const own = st.turtles.find((tu) => tu.tab && tu.tab.id === ev.id)
    const named = (n) => {
      const w = String(n || '').toLowerCase().replace(/^papaya-/, '')
      return st.turtles.find((tu) => tu.tab && tu.tab.name && tu.tab.name.toLowerCase().startsWith(w))
    }
    const puff = (x, y, n) => { for (let i = 0; i < n; i++) st.bubbles.push({ x: x + (st.rnd() - 0.5) * 3, y, v: 2 + st.rnd() * 3, w: st.rnd() * 6.28, life: 3 + st.rnd() * 2 }) }
    if (ev.type === 'activity' && own) own.dash = 1
    if (ev.type === 'focus' && own) own.dash = Math.max(own.dash, 0.5)
    if (ev.type === 'bell' && own) puff(own.x, own.y, 10)
    if (ev.type === 'say') {
      const from = named(ev.from), to = named(ev.to)
      if (from && to && from !== to) { from.chase = to; from.dash = 1 }
      else if (to) { to.dash = 1; puff(to.x, to.y, 8) }
    }
    if (ev.type === 'cue') st.turtles.forEach((tu) => { tu.dash = 1; tu.goal = edgeGoal(st, st.rnd); puff(tu.x, tu.y, 12) })
  },

  draw(s) {
    const st = s.state, lc = st.lc, ctx = s.ctx
    const W = st.W, H = st.H
    if (!W || !st.backdrop) return
    const dt = s.dt, t = s.t

    // Turtles glide on eased steering, like the koi.
    for (const tu of st.turtles) {
      const goal = tu.chase || tu.goal
      const dx = goal.x - tu.x, dy = goal.y - tu.y, d = Math.hypot(dx, dy)
      let turn = Math.atan2(dy, dx) - tu.a
      turn = Math.atan2(Math.sin(turn), Math.cos(turn))
      const want = s.clamp(turn * 0.5, -0.3, 0.3)
      tu.turnRate += (want - tu.turnRate) * Math.min(1, dt * 0.7)
      tu.a += tu.turnRate * dt
      const v = tu.speed * (1 + tu.dash * 1.2) * (tu.chase ? 1.5 : 1)
      tu.x += Math.cos(tu.a) * v * dt
      tu.y += Math.sin(tu.a) * v * dt
      tu.dash = Math.max(0, tu.dash - dt * 0.22)
      if (tu.chase && d < tu.size * 2.2) {
        for (let i = 0; i < 8; i++) st.bubbles.push({ x: tu.x, y: tu.y, v: 2 + st.rnd() * 3, w: st.rnd() * 6.28, life: 3 })
        tu.chase.dash = 1; tu.chase = null; tu.goal = edgeGoal(st, st.rnd)
      } else if (!tu.chase && d < 4) tu.goal = edgeGoal(st, st.rnd)
      if (st.rnd() < dt * 0.25) st.bubbles.push({ x: tu.x + Math.cos(tu.a) * tu.size * 1.4, y: tu.y, v: 1.5 + st.rnd() * 2, w: st.rnd() * 6.28, life: 3 })
    }
    // The school follows a lazy leader.
    const sc = st.school
    {
      const dx = sc.goal.x - sc.x, dy = sc.goal.y - sc.y
      if (Math.hypot(dx, dy) < 3) sc.goal = edgeGoal(st, st.rnd)
      let turn = Math.atan2(dy, dx) - sc.a
      turn = Math.atan2(Math.sin(turn), Math.cos(turn))
      sc.a += s.clamp(turn, -0.6, 0.6) * dt
      sc.x += Math.cos(sc.a) * 1.4 * dt
      sc.y += Math.sin(sc.a) * 1.4 * dt
    }

    lc.clearRect(0, 0, W, H)
    // Kelp sways from the sand up.
    for (const k of st.kelp) {
      for (let i = 0; i < k.len; i++) {
        const f = i / k.len
        const x = k.x + Math.sin(t * 0.55 + k.ph - i * 0.18) * f * 2.6
        lc.fillStyle = i > k.len - 3 ? k.tip : k.color
        lc.fillRect(Math.round(x), H - 4 - i, 1, 1)
        if (i % 4 === 2) lc.fillRect(Math.round(x) + (i % 8 === 2 ? 1 : -1), H - 4 - i, 1, 1)
      }
    }
    for (const tu of st.turtles) drawTurtle(lc, tu, t)
    lc.fillStyle = '#ffe066'
    for (const f of sc.fish) {
      const x = sc.x + f.ox + Math.sin(t * 1.3 + f.ph) * 0.6, y = sc.y + f.oy + Math.cos(t * 1.1 + f.ph) * 0.5
      lc.fillRect(Math.round(x), Math.round(y), 1, 1)
    }

    ctx.drawImage(st.backdrop, 0, 0)
    // Light dancing on the water.
    for (let y = 0; y < H - 4; y++) {
      for (let x = (y & 1); x < W; x += 2) {
        const n = s.noise(x * 0.1 + t * 0.07, y * 0.1 - t * 0.05)
        if (n > 0.8) {
          ctx.fillStyle = `rgba(230,255,255,${(n - 0.8) * 0.7})`
          ctx.fillRect(x * CELL + GAP / 2, y * CELL + GAP / 2, CELL - GAP, CELL - GAP)
        }
      }
    }
    const img = lc.getImageData(0, 0, W, H).data
    for (let y = 0; y < H; y++) {
      for (let x = 0; x < W; x++) {
        const k = (y * W + x) * 4, a = img[k + 3]
        if (a < 10) continue
        const j = 0.94 + hash(x, y) * 0.12
        const r = Math.min(255, img[k] * j) | 0, g = Math.min(255, img[k + 1] * j) | 0, b = Math.min(255, img[k + 2] * j) | 0
        ctx.fillStyle = a > 245 ? `rgb(${r},${g},${b})` : `rgba(${r},${g},${b},${(a / 255).toFixed(3)})`
        ctx.fillRect(x * CELL + GAP / 2, y * CELL + GAP / 2, CELL - GAP, CELL - GAP)
      }
    }
    // Bubbles rise and wobble.
    st.bubbles = st.bubbles.filter((b) => {
      b.y -= b.v * dt; b.life -= dt
      const x = Math.round(b.x + Math.sin(t * 2 + b.w) * 0.6), y = Math.round(b.y)
      ctx.fillStyle = `rgba(240,255,255,${Math.min(0.8, b.life * 0.4)})`
      ctx.fillRect(x * CELL + GAP, y * CELL + GAP, CELL - GAP * 2, CELL - GAP * 2)
      return b.life > 0 && b.y > 0
    })
    if (st.bubbles.length > 160) st.bubbles.splice(0, st.bubbles.length - 160)
  },
})
