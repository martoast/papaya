// Koi pond: pixel koi drift under lily pads. Each tab owns a koi; it darts
// when its agent prints, and on papaya-say the sender's koi swims to the
// receiver's. A cue scatters ripples across the pond.

const PX = 4

function makeKoi(r, i) {
  const palettes = [['#ffffff', '#ff6a2b'], ['#ff8a3d', '#1a1a1a'], ['#fff4e0', '#e83a3a'], ['#ffd27a', '#ff6a2b']]
  return { x: r(), y: r(), a: r() * 6.28, v: 0.02 + r() * 0.02, phase: r() * 6.28, pal: palettes[i % palettes.length], dash: 0, target: null, tab: null }
}

papaya.scene({
  setup(s) {
    const r = s.rand(11)
    s.state.koi = Array.from({ length: 6 }, (_, i) => makeKoi(r, i))
    s.state.pads = Array.from({ length: 9 }, () => ({ x: r(), y: r(), size: 5 + Math.floor(r() * 5), flower: r() < 0.35 }))
    s.state.ripples = []
    s.state.low = document.createElement('canvas')
    s.state.lc = s.state.low.getContext('2d')
  },
  resize(s) {
    s.state.low.width = Math.ceil(s.w / PX)
    s.state.low.height = Math.ceil(s.h / PX)
  },
  init(s) { assign(s) },
  on(s, ev) {
    const st = s.state
    if (ev.type.startsWith('tab-')) assign(s)
    const own = st.koi.find((k) => k.tab && (k.tab.id === ev.id || k.tab.name === ev.tab))
    if (ev.type === 'activity' && own) own.dash = 1
    if (ev.type === 'bell' && own) st.ripples.push({ x: own.x, y: own.y, t: 0 })
    if (ev.type === 'say') {
      const from = st.koi.find((k) => k.tab && k.tab.name.toLowerCase() === String(ev.from).toLowerCase())
      const to = st.koi.find((k) => k.tab && k.tab.name.toLowerCase() === String(ev.to).toLowerCase())
      if (from && to) from.target = to
    }
    if (ev.type === 'cue') for (let i = 0; i < 8; i++) st.ripples.push({ x: Math.random(), y: Math.random(), t: -i * 0.15 })
  },
  draw(s) {
    const st = s.state
    const lc = st.lc, W = st.low.width, H = st.low.height
    if (!W) return
    // water
    const g = lc.createLinearGradient(0, 0, W, H)
    g.addColorStop(0, '#0b3a37'); g.addColorStop(1, '#05201e')
    lc.fillStyle = g
    lc.fillRect(0, 0, W, H)
    // caustic shimmer
    lc.fillStyle = 'rgba(120,220,200,0.06)'
    for (let y = 0; y < H; y += 3) for (let x = (y * 7) % 5; x < W; x += 5) {
      if (s.noise(x * 0.08 + s.t * 0.4, y * 0.08 - s.t * 0.3) > 0.68) lc.fillRect(x, y, 2, 1)
    }
    // ripples
    st.ripples = st.ripples.filter((rp) => {
      rp.t += s.dt
      if (rp.t < 0) return true
      const rad = rp.t * 30
      lc.strokeStyle = `rgba(200,255,240,${Math.max(0, 0.5 - rp.t * 0.25)})`
      lc.beginPath(); lc.ellipse(rp.x * W, rp.y * H, rad, rad * 0.6, 0, 0, 6.28); lc.stroke()
      return rp.t < 2
    })
    // koi
    for (const k of st.koi) {
      if (k.target) {
        const dx = k.target.x - k.x, dy = k.target.y - k.y
        k.a = Math.atan2(dy, dx)
        if (Math.hypot(dx, dy) < 0.03) { k.target.dash = 1; st.ripples.push({ x: k.x, y: k.y, t: 0 }); k.target = null }
      } else {
        k.a += (s.noise(k.phase, s.t * 0.2) - 0.5) * 0.04
      }
      const speed = (k.target ? 0.18 : k.v) * (1 + k.dash * 3)
      k.dash = Math.max(0, k.dash - s.dt * 0.8)
      k.x = (k.x + Math.cos(k.a) * speed * s.dt + 1) % 1
      k.y = (k.y + Math.sin(k.a) * speed * s.dt * 1.4 + 1) % 1
      drawKoi(lc, k.x * W, k.y * H, k.a, k.pal, s.t + k.phase)
    }
    // lily pads above the fish
    for (const p of st.pads) {
      const x = Math.round(p.x * W), y = Math.round(p.y * H)
      lc.fillStyle = '#2f7a3e'
      lc.beginPath(); lc.arc(x, y, p.size, 0.35, 6.0); lc.lineTo(x, y); lc.fill()
      lc.fillStyle = '#3f9550'
      lc.fillRect(x - 1, y - p.size + 2, 2, 2)
      if (p.flower) { lc.fillStyle = '#ffb3d1'; lc.fillRect(x - 1, y - 1, 3, 3); lc.fillStyle = '#ffe680'; lc.fillRect(x, y, 1, 1) }
    }
    const ctx = s.ctx
    ctx.imageSmoothingEnabled = false
    ctx.drawImage(st.low, 0, 0, W * PX, H * PX)
  },
})

function assign(s) {
  s.state.koi.forEach((k, i) => { k.tab = s.tabs[i] || null })
}

function drawKoi(lc, x, y, a, pal, t) {
  // a 9-segment body that sways; head first
  for (let i = 0; i < 9; i++) {
    const sway = Math.sin(t * 4 - i * 0.6) * i * 0.12
    const px = x - Math.cos(a + sway) * i * 0.9
    const py = y - Math.sin(a + sway) * i * 0.9
    const w = i < 2 ? 2 : i < 6 ? 3 : i < 8 ? 2 : 1
    lc.fillStyle = (i === 2 || i === 5) ? pal[1] : pal[0]
    lc.fillRect(Math.round(px - w / 2), Math.round(py - w / 2), w, w)
  }
}
