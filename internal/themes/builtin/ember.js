// Ember — a pixel campfire along the bottom of the screen, made with the
// classic demoscene fire: heat rises from a burning bottom row, cooling and
// drifting as it goes, and is coloured from black through red and orange to
// yellow-white. Embers float up and wink out.
//
// An agent printing feeds the fire a little; a bell makes it flare up; a cue
// throws a burst of sparks into the air.

let CELL = 8
const GAP = 1

// Black, deep red, red, orange, yellow, white.
const PALETTE = (() => {
  const stops = [[0, [20, 8, 6]], [0.18, [70, 12, 8]], [0.38, [170, 30, 12]], [0.58, [235, 90, 20]], [0.78, [255, 170, 50]], [1, [255, 240, 190]]]
  const out = []
  for (let i = 0; i < 37; i++) {
    const f = i / 36
    let k = 0
    while (k < stops.length - 2 && stops[k + 1][0] < f) k++
    const [f0, c0] = stops[k], [f1, c1] = stops[k + 1]
    const q = (f - f0) / (f1 - f0)
    out.push(`rgb(${c0.map((v, j) => Math.round(v + (c1[j] - v) * q)).join(',')})`)
  }
  return out
})()

papaya.scene({
  setup(s) {
    const st = s.state
    st.rnd = s.rand(53)
    st.embers = []
    st.heat = 0
    st.flare = 0
    st.acc = 0
  },

  resize(s) {
    const st = s.state
    CELL = s.w < 700 ? 6 : 8
    st.W = Math.ceil(s.w / CELL)
    st.H = Math.ceil(s.h / CELL)
    // The fire lives in a band at the bottom: about a fifth of the screen.
    st.FH = Math.max(18, Math.round(st.H * 0.3))
    st.fire = new Uint8Array(st.W * st.FH)
    const bg = document.createElement('canvas')
    bg.width = Math.ceil(s.w); bg.height = Math.ceil(s.h)
    const g = bg.getContext('2d')
    const grad = g.createLinearGradient(0, 0, 0, s.h)
    grad.addColorStop(0, '#120705')
    grad.addColorStop(0.7, '#1c0a07')
    grad.addColorStop(1, '#2e0f09')
    g.fillStyle = grad
    g.fillRect(0, 0, s.w, s.h)
    const glow = g.createRadialGradient(s.w * 0.5, s.h * 1.05, 0, s.w * 0.5, s.h * 1.05, s.h * 0.7)
    glow.addColorStop(0, 'rgba(255,90,30,0.28)')
    glow.addColorStop(1, 'rgba(0,0,0,0)')
    g.fillStyle = glow
    g.fillRect(0, 0, s.w, s.h)
    st.bg = bg
  },

  on(s, ev) {
    const st = s.state
    if (ev.type === 'activity') st.heat = Math.min(1, st.heat + 0.15)
    if (ev.type === 'bell' || ev.type === 'say') st.flare = 1
    if (ev.type === 'cue') {
      st.flare = 1
      for (let i = 0; i < 80; i++) st.embers.push({ x: st.rnd() * st.W, y: st.H - st.FH * 0.6, vx: (st.rnd() - 0.5) * 6, vy: -(8 + st.rnd() * 14), life: 1.5 + st.rnd() * 2 })
    }
  },

  draw(s) {
    const st = s.state, ctx = s.ctx, dt = s.dt, t = s.t
    const W = st.W, H = st.H, FH = st.FH, fire = st.fire
    if (!W || !st.bg) return
    ctx.drawImage(st.bg, 0, 0)

    st.heat = Math.max(0, st.heat - dt * 0.05)
    st.flare = Math.max(0, st.flare - dt * 0.4)
    // Step the fire at a steady 20 per second, whatever the frame rate.
    st.acc += dt
    while (st.acc > 0.05) {
      st.acc -= 0.05
      // The burning row: hotter in clumps that wander, so flames gather into
      // campfire-like tongues instead of a flat wall.
      for (let x = 0; x < W; x++) {
        const clump = s.noise(x * 0.07, t * 0.18)
        const lit = clump > 0.5 - st.heat * 0.12 - st.flare * 0.25
        fire[(FH - 1) * W + x] = lit ? Math.min(36, 22 + Math.round(clump * 12 + st.flare * 8)) : 0
      }
      for (let y = 1; y < FH; y++) {
        for (let x = 0; x < W; x++) {
          const src = y * W + x
          const v = fire[src]
          if (v === 0) { fire[src - W] = 0; continue }
          const r = (Math.random() * 3) | 0
          // Cooling sets how tall flames get: on average they die out well
          // before the top of the band, a little higher during a flare.
          const cool = (Math.random() * (st.flare > 0.2 ? 2.6 : 3.4)) | 0
          const dst = src - W - r + 1
          fire[dst < 0 ? 0 : dst] = Math.max(0, v - cool)
        }
      }
    }
    const top = H - FH
    for (let y = 0; y < FH; y++) for (let x = 0; x < W; x++) {
      const v = fire[y * W + x]
      if (v < 2) continue
      ctx.globalAlpha = Math.min(1, v / 10)
      ctx.fillStyle = PALETTE[v]
      ctx.fillRect(x * CELL + GAP / 2, (top + y) * CELL + GAP / 2, CELL - GAP, CELL - GAP)
    }

    // Embers.
    if (st.rnd() < dt * (3 + st.flare * 12 + st.heat * 4)) {
      st.embers.push({ x: st.rnd() * W, y: H - FH * 0.5, vx: (st.rnd() - 0.5) * 2, vy: -(3 + st.rnd() * 5), life: 2 + st.rnd() * 3 })
    }
    st.embers = st.embers.filter((e) => {
      e.x += (e.vx + Math.sin(t * 2 + e.life * 3) * 0.8) * dt
      e.y += e.vy * dt
      e.vy *= 1 - dt * 0.15
      e.life -= dt
      const a = Math.max(0, Math.min(1, e.life * 0.6)) * (0.6 + 0.4 * Math.sin(t * 12 + e.x))
      ctx.globalAlpha = a
      ctx.fillStyle = e.life > 1.2 ? '#ffd27a' : '#ff7a2f'
      ctx.fillRect(Math.round(e.x) * CELL + GAP, Math.round(e.y) * CELL + GAP, CELL - GAP * 2, CELL - GAP * 2)
      return e.life > 0 && e.y > 0
    })
    if (st.embers.length > 220) st.embers.splice(0, st.embers.length - 220)
    ctx.globalAlpha = 1
  },
})
