// Dither Dunes — warm paper, a dithered sun, and dunes drawn in ordered
// dithering that drift very slowly. A small caravan of camels walks the front
// ridge. A light theme: the scene stays pale so dark text reads cleanly.
//
// A bell blows a puff of sand off the ridge; a papaya-say message sends a
// gust across the dunes; a cue makes the sun flare.

let DOT = 4

// 4x4 Bayer matrix, for ordered dithering.
const BAYER = [0, 8, 2, 10, 12, 4, 14, 6, 3, 11, 1, 9, 15, 7, 13, 5].map((v) => (v + 0.5) / 16)

const CAMEL = [
  '....##......',
  '...####..#..',
  '..######.##.',
  '.##########.',
  '.#.......#..',
  '.#.......#..',
]

papaya.scene({
  setup(s) {
    const st = s.state
    st.rnd = s.rand(29)
    st.flare = 0
    st.gusts = []
    st.puffs = []
    st.walk = 0
  },

  resize(s) {
    const st = s.state
    DOT = s.w < 700 ? 3 : 4
    st.W = Math.ceil(s.w / DOT)
    st.H = Math.ceil(s.h / DOT)
    // The dunes are drawn into a cache at most a few times a second: they move
    // so slowly that redrawing them every frame would only burn battery.
    st.cache = document.createElement('canvas')
    st.cache.width = Math.ceil(s.w); st.cache.height = Math.ceil(s.h)
    st.cachedAt = -1
    // The caravan starts partway across, so it is there from the first look.
    if (!st.walk) st.walk = 20 + st.W * 0.45
  },

  on(s, ev) {
    const st = s.state
    if (ev.type === 'cue') st.flare = 1
    if (ev.type === 'say') st.gusts.push({ x: -10, t: 0 })
    if (ev.type === 'bell') {
      for (let i = 0; i < 30; i++) st.puffs.push({ x: st.W * (0.55 + st.rnd() * 0.35), y: st.H * 0.84, vx: 8 + st.rnd() * 14, vy: -4 - st.rnd() * 6, life: 1.5 + st.rnd() })
    }
  },

  draw(s) {
    const st = s.state, ctx = s.ctx, dt = s.dt, t = s.t
    const W = st.W, H = st.H
    if (!W) return
    const ridge = (k, x) => H * k.base + Math.sin((x + t * k.v) * k.f + k.p) * H * k.a + Math.sin((x - t * k.v * 0.7) * k.f * 2.3 + k.p * 2) * H * k.a * 0.35
    const layers = [
      { base: 0.62, a: 0.03, f: 0.012, v: 1.2, p: 0.5, ink: [216, 195, 154], dens: 0.45 },
      { base: 0.7, a: 0.035, f: 0.016, v: 1.8, p: 2.1, ink: [196, 168, 119], dens: 0.55 },
      { base: 0.8, a: 0.04, f: 0.02, v: 2.6, p: 4.0, ink: [167, 135, 87], dens: 0.65 },
      { base: 0.9, a: 0.035, f: 0.026, v: 3.4, p: 1.3, ink: [138, 106, 64], dens: 0.75 },
    ]

    if (t - st.cachedAt > 0.25 || st.cachedAt < 0) {
      st.cachedAt = t
      const g = st.cache.getContext('2d')
      g.fillStyle = '#f2e6cc'
      g.fillRect(0, 0, s.w, s.h)
      // Paper grain.
      for (let y = 0; y < H; y += 2) for (let x = (y * 3) % 5; x < W; x += 5) {
        if (BAYER[(x & 3) + (y & 3) * 4] > 0.93) { g.fillStyle = 'rgba(160,130,90,0.12)'; g.fillRect(x * DOT, y * DOT, DOT - 1, DOT - 1) }
      }
      for (const k of layers) {
        g.fillStyle = `rgb(${k.ink.join(',')})`
        for (let x = 0; x < W; x++) {
          const top = Math.round(ridge(k, x))
          for (let y = top; y < H; y++) {
            // Denser just under the crest, fading down the slope: the dither
            // threshold does the shading.
            const depth = (y - top) / (H * 0.12)
            const shade = y === top ? 1 : k.dens * Math.max(0.25, 1 - depth * 0.5)
            if (BAYER[(x & 3) + (y & 3) * 4] < shade) g.fillRect(x * DOT, y * DOT, DOT - 1, DOT - 1)
          }
        }
      }
      // The red ridge line on the front dune, as in a print.
      g.fillStyle = '#d9532f'
      const front = layers[2]
      for (let x = Math.round(W * 0.7); x < W; x++) g.fillRect(x * DOT, Math.round(ridge(front, x)) * DOT - DOT, DOT - 1, DOT - 1)
    }
    ctx.drawImage(st.cache, 0, 0)

    // The sun: a dithered disc that breathes.
    st.flare = Math.max(0, st.flare - dt * 0.35)
    const sx = W * 0.82, sy = H * 0.26, R = Math.min(W, H) * (0.07 + 0.01 * Math.sin(t * 0.6)) * (1 + st.flare * 0.5)
    ctx.fillStyle = '#d9532f'
    for (let y = Math.floor(sy - R * 1.8); y < sy + R * 1.8; y++) for (let x = Math.floor(sx - R * 1.8); x < sx + R * 1.8; x++) {
      const d = Math.hypot(x - sx, y - sy) / R
      const v = d < 1 ? 1 : Math.max(0, 1 - (d - 1) * 1.4) * 0.5
      if (v > 0 && BAYER[(x & 3) + (y & 3) * 4] < v) ctx.fillRect(x * DOT, y * DOT, DOT - 1, DOT - 1)
    }

    // The caravan walks the front ridge from right to left, and comes round again.
    st.walk += dt * 2.2
    const lead = W + 20 - (st.walk % (W + 80))
    const front = layers[3]
    ctx.fillStyle = '#5a3e22'
    for (let c = 0; c < 3; c++) {
      const cx = Math.round(lead + c * 16)
      const step = Math.floor(t * 4 + c) % 2
      const cy = Math.round(ridge(front, cx + 6)) - CAMEL.length
      CAMEL.forEach((row, y) => [...row].forEach((ch, x) => {
        if (ch !== '#') return
        // Legs alternate: a two-frame walk.
        if (y >= 4 && ((x < 4) === (step === 0))) return
        ctx.fillRect((cx + (11 - x)) * DOT, (cy + y) * DOT, DOT - 1, DOT - 1)
      }))
    }

    // A gust: a band of loose sand racing across.
    st.gusts = st.gusts.filter((gu) => {
      gu.t += dt; gu.x += dt * W * 0.45
      ctx.fillStyle = 'rgba(167,135,87,0.55)'
      for (let i = 0; i < 60; i++) {
        const x = Math.round(gu.x - i * 3 - (i % 7) * 2), y = Math.round(H * (0.66 + ((i * 37) % 30) / 100) + Math.sin(i + t * 4) * 2)
        ctx.fillRect(x * DOT, y * DOT, DOT - 1, DOT - 1)
      }
      return gu.x < W + 200
    })
    st.puffs = st.puffs.filter((p) => {
      p.x += p.vx * dt; p.y += p.vy * dt; p.vy += 6 * dt; p.life -= dt
      ctx.fillStyle = `rgba(138,106,64,${Math.max(0, p.life) * 0.5})`
      ctx.fillRect(Math.round(p.x) * DOT, Math.round(p.y) * DOT, DOT - 1, DOT - 1)
      return p.life > 0
    })
  },
})
