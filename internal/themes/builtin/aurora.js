// Aurora — pixel aurora curtains ripple across a night sky over dark mountains,
// and shimmer in the still lake below. Stars show through where the light is
// thin.
//
// A bell brightens the curtains for a moment; a papaya-say message runs a
// ripple across the lake; a cue makes the aurora surge and blush pink.

let CELL = 8
const GAP = 1

function hash(x, y) {
  let h = (x * 374761393 + y * 668265263) | 0
  h = (h ^ (h >>> 13)) * 1274126177
  return ((h ^ (h >>> 16)) >>> 0) / 4294967296
}

const BAYER = [0, 8, 2, 10, 12, 4, 14, 6, 3, 11, 1, 9, 15, 7, 13, 5].map((v) => (v + 0.5) / 16)

papaya.scene({
  setup(s) {
    const st = s.state
    st.bright = 0
    st.surge = 0
    st.ripples = []
  },

  resize(s) {
    const st = s.state
    CELL = s.w < 700 ? 6 : 8
    st.W = Math.ceil(s.w / CELL)
    st.H = Math.ceil(s.h / CELL)
    st.horizon = Math.round(st.H * 0.8)
    // Mountains and the lake's dark water, drawn once.
    const c = document.createElement('canvas')
    c.width = Math.ceil(s.w); c.height = Math.ceil(s.h)
    const g = c.getContext('2d')
    const grad = g.createLinearGradient(0, 0, 0, s.h)
    grad.addColorStop(0, '#04081a')
    grad.addColorStop(0.75, '#0a1430')
    grad.addColorStop(1, '#050b1e')
    g.fillStyle = grad
    g.fillRect(0, 0, s.w, s.h)
    st.ridge = []
    for (let x = 0; x < st.W; x++) {
      const m = Math.abs(Math.sin(x * 0.045 + 1.2)) * 7 + Math.abs(Math.sin(x * 0.11)) * 3 + hash(x, 3) * 1.2
      st.ridge.push(Math.round(st.horizon - m))
    }
    for (let x = 0; x < st.W; x++) {
      for (let y = st.ridge[x]; y < st.horizon; y++) {
        g.fillStyle = y < st.ridge[x] + 1 ? '#3a4f80' : y < st.ridge[x] + 3 && hash(x, y) > 0.5 ? '#26365f' : '#18233f'
        g.fillRect(x * CELL + GAP / 2, y * CELL + GAP / 2, CELL - GAP, CELL - GAP)
      }
    }
    st.base = c
    st.stars = []
    for (let y = 0; y < st.horizon - 4; y++) for (let x = 0; x < st.W; x++) {
      const h = hash(x * 13 + 7, y * 17 + 3)
      if (h > 0.985) st.stars.push({ x, y, ph: h * 70 })
    }
  },

  on(s, ev) {
    const st = s.state
    if (ev.type === 'bell') st.bright = 1
    if (ev.type === 'cue') st.surge = 1
    if (ev.type === 'say') st.ripples.push({ x: st.W * (0.2 + Math.random() * 0.6), t: 0 })
  },

  draw(s) {
    const st = s.state, ctx = s.ctx, dt = s.dt, t = s.t
    const W = st.W, H = st.H, hz = st.horizon
    if (!W || !st.base) return
    ctx.drawImage(st.base, 0, 0)
    st.bright = Math.max(0, st.bright - dt * 0.5)
    st.surge = Math.max(0, st.surge - dt * 0.2)
    const put = (x, y, color, a) => {
      ctx.globalAlpha = a
      ctx.fillStyle = color
      ctx.fillRect(x * CELL + GAP / 2, y * CELL + GAP / 2, CELL - GAP, CELL - GAP)
    }

    for (const p of st.stars) put(p.x, p.y, '#e8f0ff', 0.3 + 0.5 * Math.max(0, Math.sin(t * 0.7 + p.ph)))

    // The curtain: for each column a wavering centre line; brightness falls off
    // above and below it, and is dithered into pixels. Greens at the bottom
    // edge, teal in the middle, violet at the top.
    const gain = 0.8 + st.bright * 0.5 + st.surge * 0.6
    const field = new Float32Array(W * hz)
    for (let x = 0; x < W; x++) {
      const c = hz * 0.32 + s.noise(x * 0.035, t * 0.06) * hz * 0.22 + Math.sin(x * 0.08 + t * 0.3) * 2
      // Curtains are made of vertical rays: a fast-varying brightness per
      // column on top of the slow swell of the band.
      const swell = Math.max(0, s.noise(x * 0.03 + 10, t * 0.05) - 0.12) * 1.4
      const rays = 0.45 + 0.55 * s.noise(x * 0.45, t * 0.25)
      const strength = swell * rays * gain
      if (strength <= 0.02) continue
      for (let y = 0; y < hz; y++) {
        const d = y - c
        // A sharp lower edge and a long, thin fade upwards, like real curtains.
        const v = d > 0 ? Math.exp(-(d * d) / 5) : Math.exp(-(d * d) / 420) * 0.85
        field[y * W + x] = v * strength
      }
    }
    for (let y = 0; y < hz; y++) for (let x = 0; x < W; x++) {
      const v = field[y * W + x]
      if (v < 0.06) continue
      if (BAYER[(x & 3) + (y & 3) * 4] > v * 1.4) continue
      const c = field[Math.min(hz - 1, y + 3) * W + x] > v ? (y < hz * 0.25 ? '#b07cff' : '#5ff2e6') : '#3df5a5'
      put(x, y, st.surge > 0.3 && y < hz * 0.3 ? '#ff7ab8' : c, Math.min(0.9, 0.35 + v * 0.6))
    }

    // The lake: the curtain mirrored, broken up by a gentle shimmer and any
    // ripples travelling across.
    for (let y = hz; y < H; y++) {
      const my = hz - (y - hz) * 2 - 1
      if (my < 0) continue
      for (let x = 0; x < W; x++) {
        let shift = Math.round(Math.sin(y * 0.9 + t * 1.5) * 1.2)
        for (const r of st.ripples) shift += Math.round(Math.sin((x - r.x) * 0.5 - r.t * 6) * Math.max(0, 2 - Math.abs(x - r.x - r.t * 10) * 0.08) * Math.max(0, 1 - r.t / 4))
        const sx = Math.min(W - 1, Math.max(0, x + shift))
        const v = field[my * W + sx] * 0.7
        if (v < 0.06 || BAYER[(x & 3) + (y & 3) * 4] > v * 1.5) continue
        put(x, y, '#3df5a5', Math.min(0.75, 0.25 + v * 0.7))
      }
    }
    st.ripples = st.ripples.filter((r) => (r.t += dt) < 4)
    ctx.globalAlpha = 1
  },
})
