// Orchard — Papaya's own scene, and a worked example of the scene API.
//
// Every open tab is a seed. A seed glows while its agent prints, rings when it
// rings the terminal bell, and a trail of light runs from one seed to another
// when one agent messages another with papaya-say. `papaya-theme cue <name>`
// bursts the whole orchard. Drawn as dithered pixel art at a quarter of the
// screen's resolution, straight into an ImageData buffer.

const BAYER = [0, 8, 2, 10, 12, 4, 14, 6, 3, 11, 1, 9, 15, 7, 13, 5].map((v) => (v + 0.5) / 16)

function rgb(s, name) {
  const c = s.color(name)
  const m = /^#([0-9a-f]{6})/i.exec(c)
  if (m) {
    const v = parseInt(m[1], 16)
    return [(v >> 16) & 255, (v >> 8) & 255, v & 255]
  }
  const p = c.match(/[\d.]+/g) || [0, 0, 0]
  return [+p[0], +p[1], +p[2]]
}
const pack = (c) => (255 << 24) | (c[2] << 16) | (c[1] << 8) | c[0]
const mix = (a, b, t) => [a[0] + (b[0] - a[0]) * t, a[1] + (b[1] - a[1]) * t, a[2] + (b[2] - a[2]) * t].map(Math.round)

function layout(s) {
  const st = s.state
  if (!st.W) return
  const tabs = s.tabs.length ? s.tabs : Array.from({ length: 6 }, (_, i) => ({ id: 'ghost' + i, ghost: true }))
  const n = tabs.length
  const cx = st.W * 0.5, cy = st.H * 0.54, rx = st.W * 0.28, ry = st.H * 0.27
  const keep = new Map()
  tabs.forEach((t, i) => {
    const a = n === 1 ? 0 : (i / n) * Math.PI * 2 - Math.PI / 2
    const seed = st.seeds.get(t.id) || { glow: 0, ring: 0 }
    seed.x = n === 1 ? cx : cx + Math.cos(a) * rx
    seed.y = n === 1 ? cy : cy + Math.sin(a) * ry
    seed.name = (t.name || '').toLowerCase()
    seed.ghost = !!t.ghost
    keep.set(t.id, seed)
  })
  st.seeds = keep
}

function seedFor(s, name) {
  if (!name) return null
  const want = String(name).toLowerCase().replace(/^papaya-/, '')
  for (const seed of s.state.seeds.values()) if (seed.name === want) return seed
  for (const seed of s.state.seeds.values()) if (seed.name && seed.name.startsWith(want)) return seed
  return null
}

papaya.scene({
  setup(s) {
    const st = s.state
    st.px = 4
    st.low = document.createElement('canvas')
    st.lc = st.low.getContext('2d')
    st.seeds = new Map()
    st.trails = []
    st.sparks = []
    st.dust = []
    const r = s.rand(7)
    for (let i = 0; i < 110; i++) st.dust.push({ x: r(), y: r(), v: 0.004 + r() * 0.01, ph: r() * 6.283 })
    st.bg = rgb(s, 'surface-0')
    st.hi = rgb(s, 'accent')
    st.dim = mix(st.bg, st.hi, 0.42)
    st.dust1 = mix(st.bg, rgb(s, 'text-lo'), 0.28)
    st.rind = rgb(s, 'ansi-green')
    st.seedC = [29, 18, 12]
    st.shine = mix(st.seedC, [255, 255, 255], 0.45)
  },

  resize(s) {
    const st = s.state
    st.W = st.low.width = Math.ceil(s.w / st.px)
    st.H = st.low.height = Math.ceil(s.h / st.px)
    st.img = st.lc.createImageData(st.W, st.H)
    st.buf = new Uint32Array(st.img.data.buffer)
    layout(s)
  },

  init(s) { layout(s) },

  on(s, ev) {
    const st = s.state
    if (ev.type === 'tab-open' || ev.type === 'tab-close' || ev.type === 'tab-rename') return layout(s)
    const seed = ev.id ? st.seeds.get(ev.id) : null
    if (ev.type === 'activity' && seed) seed.glow = Math.min(1, seed.glow + 0.3)
    if (ev.type === 'focus' && seed) seed.glow = Math.max(seed.glow, 0.35)
    if (ev.type === 'bell' && seed) { seed.ring = 1; seed.glow = 1 }
    if (ev.type === 'say') {
      const to = seedFor(s, ev.to)
      const from = seedFor(s, ev.from) || { x: 0, y: st.H * 0.2 }
      if (to) st.trails.push({ a: from, b: to, p: 0, bend: (Math.random() - 0.5) * st.H * 0.5 })
    }
    if (ev.type === 'cue') {
      const cx = st.W * 0.5, cy = st.H * 0.54
      for (let i = 0; i < 160; i++) {
        const a = Math.random() * Math.PI * 2, v = 18 + Math.random() * 46
        st.sparks.push({ x: cx, y: cy, vx: Math.cos(a) * v, vy: Math.sin(a) * v - 14, life: 1.4 + Math.random() * 1.2 })
      }
      for (const sd of st.seeds.values()) { sd.glow = 1; sd.ring = 1 }
    }
  },

  draw(s) {
    const st = s.state
    const { W, H, buf } = st
    if (!buf) return
    const dt = s.dt
    const plot = (x, y, c) => {
      x |= 0; y |= 0
      if (x >= 0 && y >= 0 && x < W && y < H) buf[y * W + x] = c
    }
    buf.fill(pack(st.bg))

    // Drifting dust.
    const dc = pack(st.dust1)
    for (const d of st.dust) {
      d.y -= d.v * dt
      if (d.y < 0) d.y += 1
      plot(d.x * W + Math.sin(s.t * 0.6 + d.ph) * 2, d.y * H, dc)
    }

    // The seeds: a dithered glow, a ring on the bell, the seed itself.
    const hi = pack(st.hi), dim = pack(st.dim), seedC = pack(st.seedC), shine = pack(st.shine)
    for (const sd of st.seeds.values()) {
      const breath = 0.5 + 0.5 * Math.sin(s.t * 1.3 + sd.x * 0.1)
      const g = sd.ghost ? 0.16 + 0.05 * breath : 0.22 + 0.06 * breath + sd.glow * 0.78
      const sig = 3 + 7 * g, R = Math.ceil(sig * 2.6)
      for (let y = Math.max(0, (sd.y - R) | 0); y < Math.min(H, sd.y + R); y++) {
        for (let x = Math.max(0, (sd.x - R) | 0); x < Math.min(W, sd.x + R); x++) {
          const dx = x - sd.x, dy = (y - sd.y) * 1.25
          const I = g * Math.exp(-(dx * dx + dy * dy) / (2 * sig * sig))
          const t = BAYER[(y & 3) * 4 + (x & 3)]
          if (I > t) buf[y * W + x] = hi
          else if (I * 2 > t) buf[y * W + x] = dim
        }
      }
      if (sd.ring > 0) {
        const r = (1 - sd.ring) * 26 + 4
        for (let a = 0; a < 6.283; a += 1.2 / r) {
          if (Math.random() < sd.ring) plot(sd.x + Math.cos(a) * r, sd.y + Math.sin(a) * r * 0.8, hi)
        }
        sd.ring = Math.max(0, sd.ring - dt * 0.9)
      }
      for (const [dx, dy] of [[0, -2], [-1, -1], [0, -1], [1, -1], [-1, 0], [0, 0], [1, 0], [0, 1]]) plot(sd.x + dx, sd.y + dy, seedC)
      plot(sd.x - 1, sd.y - 1, shine)
      sd.glow = Math.max(0, sd.glow - dt * 0.45)
    }

    // papaya-say trails: a bright head with a dithered tail along a curve.
    st.trails = st.trails.filter((tr) => {
      tr.p += dt * 0.8
      const at = (p) => {
        const mx = (tr.a.x + tr.b.x) / 2, my = (tr.a.y + tr.b.y) / 2 + tr.bend
        const u = 1 - p
        return [u * u * tr.a.x + 2 * u * p * mx + p * p * tr.b.x, u * u * tr.a.y + 2 * u * p * my + p * p * tr.b.y]
      }
      for (let k = 0; k < 18; k++) {
        const p = tr.p - k * 0.012
        if (p < 0 || p > 1) continue
        const [x, y] = at(p)
        if (k === 0 || BAYER[((y | 0) & 3) * 4 + ((x | 0) & 3)] > k / 18) plot(x, y, k < 4 ? hi : dim)
      }
      if (tr.p >= 1) { tr.b.glow = 1; tr.b.ring = 1; return false }
      return true
    })

    // Cue sparks.
    st.sparks = st.sparks.filter((sp) => {
      sp.vy += 26 * dt
      sp.x += sp.vx * dt
      sp.y += sp.vy * dt
      sp.life -= dt
      plot(sp.x, sp.y, sp.life > 0.6 ? hi : dim)
      return sp.life > 0
    })

    st.lc.putImageData(st.img, 0, 0)
    const ctx = s.ctx
    ctx.imageSmoothingEnabled = false
    ctx.clearRect(0, 0, s.w, s.h)
    ctx.drawImage(st.low, 0, 0, W * st.px, H * st.px)
  },
})
