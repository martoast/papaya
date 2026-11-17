// Night Reef — deep water at night. A glowing manta ray sweeps along the
// bottom, a turtle cruises the edges, jellyfish pulse their way up, and
// plankton twinkles like stars. Everything glows: tiles are laid twice, a soft
// halo first and the bright tile on top.
//
// Each tab owns a creature (manta, turtle, then the jellyfish). Its agent
// working makes it swim faster; a bell sets the plankton around it alight; on
// papaya-say a streak of light travels from sender to receiver. A cue lights
// up the whole reef.

let CELL = 10
const GAP = 1.6

function hash(x, y) {
  let h = (x * 374761393 + y * 668265263) | 0
  h = (h ^ (h >>> 13)) * 1274126177
  return ((h ^ (h >>> 16)) >>> 0) / 4294967296
}

function backdrop(s) {
  const st = s.state
  const c = document.createElement('canvas')
  c.width = Math.ceil(s.w)
  c.height = Math.ceil(s.h)
  const g = c.getContext('2d')
  const grad = g.createLinearGradient(0, 0, 0, s.h)
  grad.addColorStop(0, '#0d2257')
  grad.addColorStop(0.6, '#0a1845')
  grad.addColorStop(1, '#060e2c')
  g.fillStyle = grad
  g.fillRect(0, 0, s.w, s.h)
  const glow = g.createRadialGradient(s.w * 0.5, s.h * 0.35, 0, s.w * 0.5, s.h * 0.35, Math.max(s.w, s.h) * 0.6)
  glow.addColorStop(0, 'rgba(60,110,220,0.18)')
  glow.addColorStop(1, 'rgba(0,0,0,0)')
  g.fillStyle = glow
  g.fillRect(0, 0, s.w, s.h)
  g.fillStyle = 'rgba(120,160,255,0.045)'
  for (let x = 0; x < s.w; x += CELL) g.fillRect(x, 0, 1, s.h)
  for (let y = 0; y < s.h; y += CELL) g.fillRect(0, y, s.w, 1)
  // Coral silhouettes along the floor.
  for (let x = 0; x < st.W; x++) {
    const h = Math.round(1 + Math.max(0, Math.sin(x * 0.31) * 2.2 + Math.sin(x * 0.11 + 2) * 1.6 + (hash(x, 7) > 0.8 ? 2 : 0)))
    for (let y = st.H - h; y < st.H; y++) {
      g.fillStyle = hash(x, y) > 0.7 ? '#13245a' : '#0f1d4c'
      g.fillRect(x * CELL + GAP / 2, y * CELL + GAP / 2, CELL - GAP, CELL - GAP)
    }
  }
  st.backdrop = c
}

// A creature is drawn as a list of [dx, dy, colour] tiles around its centre.
// Seen from above: wide triangular wings that flap, a rounded head with two
// cephalic fins, and a long thin tail. It swims along x.
function manta(k, flap) {
  const out = []
  const span = Math.round(7 * k), len = Math.round(2.6 * k)
  for (let i = -len; i <= len; i++) {
    // Wing half-width at this point along the body: widest a little forward.
    const f = 1 - Math.abs(i + len * 0.25) / (len * 1.25)
    const half = Math.max(0, Math.round(span * f))
    for (let j = -half; j <= half; j++) {
      const tip = Math.abs(j) / Math.max(1, span)
      const lift = Math.round(flap * tip * tip * 1.6 * k)
      out.push([i, j + (j > 0 ? lift : -lift), tip > 0.8 ? '#a6fff8' : tip > 0.4 ? '#45eedb' : '#22c7ba'])
    }
  }
  out.push([len + 1, -1, '#7ff9ff'], [len + 1, 1, '#7ff9ff'], [len + 2, -1, '#7ff9ff'], [len + 2, 1, '#7ff9ff'])
  for (let i = 1; i <= Math.round(4 * k); i++) out.push([-len - i, 0, '#1aa79e'])
  return out
}

function turtle(k, paddle) {
  const out = []
  const r = Math.round(2.4 * k)
  for (let y = -r; y <= r; y++) for (let x = -r; x <= r; x++) {
    const d = (x * x) / (r * r) + (y * y) / ((r * 0.8) * (r * 0.8))
    if (d <= 1) out.push([x, y, d > 0.6 ? '#3fe0d0' : (hash(x + 9, y + 9) > 0.8 ? '#ffb347' : '#3a3fd8')])
  }
  const fl = Math.round(paddle * 1.5)
  for (const sx of [-1, 1]) {
    for (let i = 1; i <= Math.round(1.6 * k); i++) out.push([sx * (r + i), -Math.round(r * 0.3) + fl * sx, '#3fe0d0'])
    out.push([sx * (r - 1), r + 1, '#3fe0d0'])
  }
  for (let i = 1; i <= Math.round(1.2 * k); i++) out.push([0, -r - i, '#5ff2e6'])
  return out
}

papaya.scene({
  setup(s) {
    const st = s.state
    st.rnd = s.rand(21)
    st.flash = []
    st.streaks = []
    st.glowAll = 0
    st.creatures = [
      { kind: 'manta', x: 0, y: 0, dir: 1, speed: 2.2, dash: 0, tab: null, ph: 0 },
      { kind: 'turtle', x: 0, y: 0, dir: -1, speed: 1.4, dash: 0, tab: null, ph: 1.7 },
      { kind: 'jelly', x: 0, y: 0, speed: 0.9, dash: 0, tab: null, ph: 0.4 },
      { kind: 'jelly', x: 0, y: 0, speed: 0.7, dash: 0, tab: null, ph: 2.9 },
    ]
  },

  resize(s) {
    const st = s.state
    CELL = s.w < 700 ? 7 : 10
    st.W = Math.ceil(s.w / CELL)
    st.H = Math.ceil(s.h / CELL)
    st.k = s.w < 700 ? 1.05 : 1.45
    // Plankton lives at fixed sites, sparser up high; each twinkles on its own.
    st.sites = []
    for (let y = 0; y < st.H; y++) for (let x = 0; x < st.W; x++) {
      const h = hash(x * 3 + 1, y * 5 + 2)
      if (h > 0.955 - 0.03 * (y / st.H)) st.sites.push({ x, y, ph: h * 40, hue: hash(x, y * 3) > 0.8 ? '#ffd27a' : '#7ff9ff' })
    }
    const [m, tu, j1, j2] = st.creatures
    if (!m.x) {
      m.x = st.W * 0.3; m.y = st.H * 0.8
      tu.x = st.W * 0.85; tu.y = st.H * 0.22
      j1.x = st.W * 0.08; j1.y = st.H * 0.7
      j2.x = st.W * 0.93; j2.y = st.H * 0.55
    }
    backdrop(s)
  },

  init(s) { s.state.creatures.forEach((c, i) => { c.tab = s.tabs[i] || null }) },

  on(s, ev) {
    const st = s.state
    if (ev.type === 'tab-open' || ev.type === 'tab-close' || ev.type === 'tab-rename') {
      st.creatures.forEach((c, i) => { c.tab = s.tabs[i] || null })
      return
    }
    const own = st.creatures.find((c) => c.tab && c.tab.id === ev.id)
    const named = (n) => {
      const w = String(n || '').toLowerCase().replace(/^papaya-/, '')
      return st.creatures.find((c) => c.tab && c.tab.name && c.tab.name.toLowerCase().startsWith(w))
    }
    if (ev.type === 'activity' && own) own.dash = 1
    if (ev.type === 'bell' && own) st.flash.push({ x: own.x, y: own.y, t: 0 })
    if (ev.type === 'say') {
      const a = named(ev.from), b = named(ev.to)
      if (a && b && a !== b) st.streaks.push({ a, b, t: 0 })
      else if (b) st.flash.push({ x: b.x, y: b.y, t: 0 })
    }
    if (ev.type === 'cue') st.glowAll = 1
  },

  draw(s) {
    const st = s.state, ctx = s.ctx, dt = s.dt, t = s.t
    const W = st.W, H = st.H
    if (!W || !st.backdrop) return
    ctx.drawImage(st.backdrop, 0, 0)
    const tile = (x, y, color, a = 1) => {
      ctx.globalAlpha = a * 0.22
      ctx.fillStyle = color
      ctx.fillRect(x * CELL - CELL * 0.35, y * CELL - CELL * 0.35, CELL * 1.7, CELL * 1.7)
      ctx.globalAlpha = a
      ctx.fillRect(x * CELL + GAP / 2, y * CELL + GAP / 2, CELL - GAP, CELL - GAP)
    }

    // Plankton: sparse points that twinkle, flare in a ring around a flash, and
    // all light up on a cue.
    st.glowAll = Math.max(0, st.glowAll - dt * 0.35)
    for (const p of st.sites) {
      let lit = 0.08 + 0.36 * Math.max(0, Math.sin(t * 0.7 + p.ph))
      for (const f of st.flash) {
        const d = Math.hypot(p.x - f.x, p.y - f.y)
        lit = Math.max(lit, Math.max(0, 1 - Math.abs(d - f.t * 9) / 4) * Math.max(0, 1 - f.t / 2.2))
      }
      lit = Math.max(lit, st.glowAll)
      tile(p.x, p.y, p.hue, Math.min(0.9, lit) * (0.55 + 0.45 * (p.y / H)))
    }
    st.flash = st.flash.filter((f) => (f.t += dt) < 2.2)

    // Creatures.
    for (const c of st.creatures) {
      c.dash = Math.max(0, c.dash - dt * 0.25)
      const v = c.speed * (1 + c.dash * 1.5)
      let shape
      if (c.kind === 'manta') {
        c.x += c.dir * v * dt
        c.y = H * 0.8 + Math.sin(t * 0.25 + c.ph) * H * 0.06
        if (c.x > W + 8) c.dir = -1
        if (c.x < -8) c.dir = 1
        shape = manta(st.k, Math.sin(t * (1.2 + c.dash)))
      } else if (c.kind === 'turtle') {
        c.x += c.dir * v * dt
        c.y = H * 0.2 + Math.sin(t * 0.18 + c.ph) * H * 0.08
        if (c.x > W + 6) c.dir = -1
        if (c.x < -6) c.dir = 1
        shape = turtle(st.k, Math.sin(t * (1.5 + c.dash * 2)))
      } else {
        const pulse = Math.max(0, Math.sin(t * 1.4 + c.ph))
        c.y -= (0.3 + pulse * 1.4) * v * dt
        if (c.y < -6) c.y = H + 4
        const k = st.k
        // A wide dome that squeezes as it pulses, trailing wavy tentacles.
        const rx = Math.round(2.6 * k * (1 - pulse * 0.15)), ry = Math.round(1.8 * k * (1 + pulse * 0.1))
        shape = []
        for (let x = -rx; x <= rx; x++) for (let y = -ry; y <= 0; y++) {
          if ((x * x) / (rx * rx) + (y * y) / (ry * ry) <= 1) shape.push([x, y, y === 0 ? '#ffb3e2' : y === -ry ? '#f0c9ff' : '#d48bff'])
        }
        const len = 4 + Math.round(3 * k)
        for (let i = -rx + 1; i < rx; i++) {
          if ((i + rx) % 2) continue
          for (let j = 1; j <= len - Math.abs(i) % 3; j++) shape.push([i + Math.round(Math.sin(t * 1.6 + j * 0.55 + i) * (j / len) * 1.4), j, j % 3 ? '#b07cff' : '#e3b5ff'])
        }
      }
      const cx = Math.round(c.x), cy = Math.round(c.y)
      const shine = c.kind === 'manta' || c.kind === 'turtle' ? 0.95 : 0.75
      for (const [dx, dy, col] of shape) {
        const x = cx + (c.dir === -1 && c.kind !== 'jelly' ? -dx : dx), y = cy + dy
        if (x >= 0 && x < W && y >= 0 && y < H) tile(x, y, col, shine)
      }
    }

    // A streak of light from one creature to another.
    st.streaks = st.streaks.filter((sk) => {
      sk.t += dt * 0.6
      const p = Math.min(1, sk.t)
      for (let i = 0; i < 6; i++) {
        const q = Math.max(0, p - i * 0.03)
        const x = Math.round(s.lerp(sk.a.x, sk.b.x, q)), y = Math.round(s.lerp(sk.a.y, sk.b.y, q) - Math.sin(q * 3.14) * 6)
        tile(x, y, '#fff6a8', 1 - i / 6)
      }
      if (p >= 1) { st.flash.push({ x: sk.b.x, y: sk.b.y, t: 0 }); sk.b.dash = 1 }
      return p < 1
    })
    ctx.globalAlpha = 1
  },
})
