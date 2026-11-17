// Teletext Clouds — big blocky clouds drift under a crescent moon, the way an
// old teletext page drew weather. Stars blink between them.
//
// A papaya-say message sends a little bird flapping across the sky; a bell
// makes the moon glow; a cue blushes the clouds pink like a sunrise.

let CELL = 12
const GAP = 1

function hash(x, y) {
  let h = (x * 374761393 + y * 668265263) | 0
  h = (h ^ (h >>> 13)) * 1274126177
  return ((h ^ (h >>> 16)) >>> 0) / 4294967296
}

const MOON = [
  '..###.',
  '.##...',
  '##....',
  '##....',
  '##....',
  '.##...',
  '..###.',
]

papaya.scene({
  setup(s) {
    const st = s.state
    st.rnd = s.rand(17)
    st.birds = []
    st.moonGlow = 0
    st.blush = 0
    st.drift = 0
  },

  resize(s) {
    const st = s.state
    CELL = s.w < 700 ? 9 : 12
    st.W = Math.ceil(s.w / CELL)
    st.H = Math.ceil(s.h / CELL)
    st.stars = []
    for (let y = 0; y < st.H; y++) for (let x = 0; x < st.W; x++) {
      const h = hash(x * 5 + 1, y * 3 + 7)
      if (h > 0.975) st.stars.push({ x, y, ph: h * 60, gold: h > 0.993 })
    }
    const sky = document.createElement('canvas')
    sky.width = Math.ceil(s.w); sky.height = Math.ceil(s.h)
    const g = sky.getContext('2d')
    const grad = g.createLinearGradient(0, 0, 0, s.h)
    grad.addColorStop(0, '#16238f')
    grad.addColorStop(0.6, '#1c2aa6')
    grad.addColorStop(1, '#141c78')
    g.fillStyle = grad
    g.fillRect(0, 0, s.w, s.h)
    g.fillStyle = 'rgba(0,0,40,0.18)'
    for (let x = 0; x < s.w; x += CELL) g.fillRect(x, 0, 1, s.h)
    for (let y = 0; y < s.h; y += CELL) g.fillRect(0, y, s.w, 1)
    st.sky = sky
  },

  on(s, ev) {
    const st = s.state
    if (ev.type === 'say') st.birds.push({ x: -3, y: st.H * (0.12 + st.rnd() * 0.25), ph: st.rnd() * 6 })
    if (ev.type === 'bell') st.moonGlow = 1
    if (ev.type === 'cue') st.blush = 1
  },

  draw(s) {
    const st = s.state, ctx = s.ctx, dt = s.dt, t = s.t
    const W = st.W, H = st.H
    if (!W || !st.sky) return
    ctx.drawImage(st.sky, 0, 0)
    const put = (x, y, color, a = 1) => {
      if (x < 0 || x >= W || y < 0 || y >= H) return
      ctx.globalAlpha = a
      ctx.fillStyle = color
      ctx.fillRect(x * CELL + GAP / 2, y * CELL + GAP / 2, CELL - GAP, CELL - GAP)
    }

    for (const p of st.stars) {
      const on = Math.sin(t * 0.8 + p.ph) > 0.2
      if (on) put(p.x, p.y, p.gold ? '#ffd34d' : '#ffffff', 0.85)
    }

    // The moon, top right, with a glow when an agent rings.
    st.moonGlow = Math.max(0, st.moonGlow - dt * 0.6)
    const mx = W - 9, my = 3
    if (st.moonGlow > 0) {
      for (let y = -2; y < 9; y++) for (let x = -2; x < 8; x++) put(mx + x, my + y, '#ffe9a0', st.moonGlow * 0.18)
    }
    MOON.forEach((row, y) => [...row].forEach((c, x) => { if (c === '#') put(mx + x, my + y, '#ffd34d') }))

    // Clouds: a slowly moving noise field, thresholded into blocks, in bands
    // that keep to the top and the bottom of the screen.
    st.drift += dt * 0.35
    st.blush = Math.max(0, st.blush - dt * 0.25)
    for (let y = 0; y < H; y++) {
      const band = Math.max(Math.exp(-((y - H * 0.28) ** 2) / (H * H * 0.006)), Math.exp(-((y - H * 0.86) ** 2) / (H * H * 0.01)))
      if (band < 0.05) continue
      for (let x = 0; x < W; x++) {
        const n = s.noise((x + st.drift) * 0.09, y * 0.22) * 0.75 + s.noise((x + st.drift * 1.6) * 0.21, y * 0.4) * 0.25
        const v = n * band
        if (v < 0.42) continue
        // Lit tops, shaded bellies, like the reference's chunky clouds.
        const above = s.noise((x + st.drift) * 0.09, (y - 1) * 0.22) * band
        let color = v > 0.58 ? '#3b6ff0' : '#2f49d1'
        if (above < 0.42) color = '#58c7f0'
        else if (v < 0.47) color = '#2a3cb8'
        put(x, y, color, 0.95)
        if (st.blush > 0 && above >= 0.42 && v < 0.5) put(x, y, '#ff6fa5', st.blush * 0.8)
      }
    }

    // Birds: a two-frame flap.
    st.birds = st.birds.filter((b) => {
      b.x += dt * 6
      const up = Math.sin(t * 9 + b.ph) > 0
      const y = Math.round(b.y + Math.sin(t * 1.5 + b.ph) * 1.5), x = Math.round(b.x)
      put(x, y, '#f4f6ff')
      put(x - 1, y + (up ? -1 : 0), '#f4f6ff'); put(x + 1, y + (up ? -1 : 0), '#f4f6ff')
      put(x - 2, y + (up ? -2 : 1), '#d7dcff'); put(x + 2, y + (up ? -2 : 1), '#d7dcff')
      return b.x < W + 4
    })
    ctx.globalAlpha = 1
  },
})
