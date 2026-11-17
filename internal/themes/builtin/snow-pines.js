// Snow Pines — a pixel forest at night. Stars twinkle over violet hills, three
// layers of pines stand along the bottom, snow drifts down on a slow wind, and
// a cabin window flickers warm.
//
// A bell puffs smoke from the cabin chimney and brightens its window; a
// papaya-say message streaks a shooting star across the sky; a cue sets off
// fireworks over the trees.

let CELL = 8
const GAP = 1.2

function hash(x, y) {
  let h = (x * 374761393 + y * 668265263) | 0
  h = (h ^ (h >>> 13)) * 1274126177
  return ((h ^ (h >>> 16)) >>> 0) / 4294967296
}

function tree(g, x, base, h, color, snow) {
  // A pixel pine: tiers that widen downwards, snow on the upper edges.
  for (let i = 0; i < h; i++) {
    const tier = i % 4
    const half = Math.round(((i / h) * h) / 3.2) - (3 - tier) * 0.25
    const w = Math.max(0, Math.round(half))
    for (let j = -w; j <= w; j++) {
      g(x + j, base - h + i, tier === 0 && Math.abs(j) >= w - 1 ? snow : color)
    }
  }
  g(x, base, '#3a2a22')
}

function backdrop(s) {
  const st = s.state
  const W = st.W, H = st.H
  const c = document.createElement('canvas')
  c.width = Math.ceil(s.w)
  c.height = Math.ceil(s.h)
  const g = c.getContext('2d')
  const grad = g.createLinearGradient(0, 0, 0, s.h)
  grad.addColorStop(0, '#141039')
  grad.addColorStop(0.55, '#2c2463')
  grad.addColorStop(0.8, '#5a4486')
  grad.addColorStop(1, '#6d5596')
  g.fillStyle = grad
  g.fillRect(0, 0, s.w, s.h)
  const put = (x, y, color) => {
    if (x < 0 || x >= W || y < 0 || y >= H) return
    g.fillStyle = color
    g.fillRect(x * CELL + GAP / 2, y * CELL + GAP / 2, CELL - GAP, CELL - GAP)
  }
  // Far hills, a soft band.
  for (let x = 0; x < W; x++) {
    const top = Math.round(H * 0.7 + Math.sin(x * 0.07) * 2.5 + Math.sin(x * 0.023 + 1) * 3)
    for (let y = top; y < H; y++) put(x, y, hash(x, y) > 0.85 ? '#3b3478' : '#332d6d')
  }
  // Three rows of pines, far to near.
  const rows = [
    { y: 0.8, color: '#2a4f63', snow: '#8fa3d8', h: [6, 10], gap: [3, 6] },
    { y: 0.88, color: '#245a5e', snow: '#b8c6f0', h: [9, 15], gap: [5, 10] },
    { y: 0.98, color: '#2f7068', snow: '#e8f0ff', h: [13, 22], gap: [9, 18] },
  ]
  // Tree heights are fractions of the screen, not fixed tile counts, so a tall
  // phone gets tall trees too.
  const S = H / 60
  let seed = 3
  const r = () => (seed = (seed * 16807) % 2147483647) / 2147483647
  for (const row of rows) {
    const base = Math.round(H * row.y)
    for (let x = -2; x < W + 2;) {
      // Keep the middle of a wide screen open, with trees massed at the sides.
      const mid = W > 120 && x > W * 0.35 && x < W * 0.65
      const h = Math.round((row.h[0] + r() * (row.h[1] - row.h[0]) * (mid ? 0.55 : 1)) * S)
      tree(put, x, base, h, row.color, row.snow)
      x += Math.round((row.gap[0] + r() * (row.gap[1] - row.gap[0])) * Math.min(1.6, S) + (mid ? 6 : 0))
    }
  }
  // Snow on the ground.
  for (let x = 0; x < W; x++) for (let y = Math.round(H * 0.97); y < H; y++) put(x, y, hash(x, y) > 0.7 ? '#cdd4f6' : '#e3e8ff')
  // The cabin, on the right.
  const cx = Math.round(W * 0.82), cy = Math.round(H * 0.97) - 1
  for (let y = 0; y < 4; y++) for (let x = 0; x < 7; x++) put(cx + x, cy - y, '#5a3a2e')
  for (let i = 0; i < 4; i++) for (let x = i; x < 7 - i; x++) put(cx + x, cy - 4 - i, i === 0 ? '#e8f0ff' : '#7a4a3a')
  put(cx + 5, cy - 6, '#5a3a2e'); put(cx + 5, cy - 7, '#5a3a2e')
  st.cabin = { x: cx, y: cy }
  st.backdrop = c
}

papaya.scene({
  setup(s) {
    const st = s.state
    st.rnd = s.rand(8)
    st.flakes = []
    st.smoke = []
    st.shooting = []
    st.sparks = []
    st.window = 0
  },

  resize(s) {
    const st = s.state
    CELL = s.w < 700 ? 6 : 8
    st.W = Math.ceil(s.w / CELL)
    st.H = Math.ceil(s.h / CELL)
    st.stars = []
    for (let y = 0; y < st.H * 0.7; y++) for (let x = 0; x < st.W; x++) {
      const h = hash(x * 7 + 3, y * 11 + 5)
      if (h > 0.985) st.stars.push({ x, y, ph: h * 50, big: h > 0.997 })
    }
    const want = Math.round((st.W * st.H) / 160)
    while (st.flakes.length < want) st.flakes.push({ x: st.rnd() * st.W, y: st.rnd() * st.H, v: 1.5 + st.rnd() * 2.5, w: st.rnd() * 6.28, big: st.rnd() > 0.85 })
    st.flakes.length = Math.min(st.flakes.length, want)
    backdrop(s)
  },

  on(s, ev) {
    const st = s.state
    if (ev.type === 'bell') {
      st.window = 1
      for (let i = 0; i < 6; i++) st.smoke.push({ x: 0, y: -i * 0.8, life: 3 + i * 0.3 })
    }
    if (ev.type === 'say') st.shooting.push({ x: st.W * (0.1 + st.rnd() * 0.5), y: st.H * (0.05 + st.rnd() * 0.25), t: 0 })
    if (ev.type === 'cue') {
      for (let n = 0; n < 4; n++) {
        const x = st.W * (0.15 + st.rnd() * 0.7), y = st.H * (0.12 + st.rnd() * 0.35)
        const hue = ['#ff7f9b', '#ffcf6a', '#7fe3e8', '#c9a2ff'][n]
        for (let i = 0; i < 26; i++) {
          const a = (i / 26) * 6.283
          st.sparks.push({ x, y, vx: Math.cos(a) * (6 + st.rnd() * 3), vy: Math.sin(a) * (6 + st.rnd() * 3), life: 1.6 + n * 0.4, delay: n * 0.45, color: hue })
        }
      }
    }
  },

  draw(s) {
    const st = s.state, ctx = s.ctx, dt = s.dt, t = s.t
    const W = st.W, H = st.H
    if (!W || !st.backdrop) return
    ctx.drawImage(st.backdrop, 0, 0)
    const put = (x, y, color, a = 1) => {
      x = Math.round(x); y = Math.round(y)
      if (x < 0 || x >= W || y < 0 || y >= H) return
      ctx.globalAlpha = a
      ctx.fillStyle = color
      ctx.fillRect(x * CELL + GAP / 2, y * CELL + GAP / 2, CELL - GAP, CELL - GAP)
    }

    for (const p of st.stars) {
      const tw = 0.35 + 0.65 * Math.max(0, Math.sin(t * 0.9 + p.ph))
      put(p.x, p.y, '#fff7d6', tw)
      if (p.big && tw > 0.8) { put(p.x - 1, p.y, '#fff7d6', tw * 0.4); put(p.x + 1, p.y, '#fff7d6', tw * 0.4) }
    }

    // The cabin window, flickering like a fire inside.
    st.window = Math.max(0, st.window - dt * 0.5)
    const flick = 0.75 + 0.25 * s.noise(t * 2.5, 0.5) + st.window * 0.4
    put(st.cabin.x + 2, st.cabin.y - 2, '#ffcf6a', Math.min(1, flick))
    put(st.cabin.x + 4, st.cabin.y - 2, '#ffb347', Math.min(1, flick * 0.9))
    put(st.cabin.x + 3, st.cabin.y, '#ffcf6a', 0.5 + st.window * 0.5)

    st.smoke = st.smoke.filter((p) => {
      p.y -= dt * 2.2; p.life -= dt
      if (p.y < 0) put(st.cabin.x + 5 + Math.sin(p.y * 0.8) * 1.2, st.cabin.y - 8 + p.y, '#cfd3ee', Math.max(0, p.life / 4) * 0.7)
      return p.life > 0
    })

    // Snow on a slow, gusting wind.
    const wind = 0.6 + Math.sin(t * 0.12) * 0.8
    for (const f of st.flakes) {
      f.y += f.v * dt
      f.x += (wind + Math.sin(t * 0.8 + f.w) * 0.6) * dt
      if (f.y > H) { f.y = -1; f.x = st.rnd() * W }
      if (f.x > W) f.x -= W
      if (f.x < 0) f.x += W
      put(f.x, f.y, '#f4f6ff', f.big ? 0.95 : 0.6)
    }

    st.shooting = st.shooting.filter((sh) => {
      sh.t += dt
      for (let i = 0; i < 7; i++) {
        const q = Math.max(0, sh.t - i * 0.03)
        put(sh.x + q * 38, sh.y + q * 12, '#fffbe6', (1 - i / 7) * Math.max(0, 1 - sh.t / 1.4))
      }
      return sh.t < 1.4
    })

    st.sparks = st.sparks.filter((p) => {
      if (p.delay > 0) { p.delay -= dt; return true }
      p.x += p.vx * dt; p.y += p.vy * dt; p.vy += 4 * dt
      p.vx *= 1 - dt * 0.8; p.vy *= 1 - dt * 0.4
      p.life -= dt
      put(p.x, p.y, p.color, Math.min(1, p.life))
      return p.life > 0
    })
    ctx.globalAlpha = 1
  },
})
