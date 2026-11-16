// Night Sky — braille-dot stars over dithered hills.
//
// Stars twinkle on a slow noise field and crowd into a diagonal band. A
// shooting star crosses for every papaya-say message; a bell makes one star
// flare; a cue sends a meteor shower.

const BAYER = [0, 8, 2, 10, 12, 4, 14, 6, 3, 11, 1, 9, 15, 7, 13, 5].map((v) => (v + 0.5) / 16)

papaya.scene({
  setup(s) {
    const st = s.state
    const r = s.rand(42)
    st.stars = []
    for (let i = 0; i < 900; i++) {
      // Half the stars gather along the band; the rest are spread evenly.
      let x = r(), y = r()
      if (i % 2) {
        const u = r()
        x = u
        y = 0.75 - u * 0.62 + (r() - 0.5) * 0.18 * (0.6 + r())
      }
      st.stars.push({ x, y, b: 0.25 + r() * 0.75, ph: r() * 100, big: r() < 0.04 })
    }
    st.meteors = []
    st.flares = []
  },

  on(s, ev) {
    const st = s.state
    const shoot = (delay = 0) => st.meteors.push({
      x: 0.1 + Math.random() * 0.6, y: Math.random() * 0.35, vx: 0.55 + Math.random() * 0.3, vy: 0.22 + Math.random() * 0.12, life: 1.1, wait: delay,
    })
    if (ev.type === 'say') shoot()
    if (ev.type === 'cue') for (let i = 0; i < 14; i++) shoot(i * 0.12)
    if (ev.type === 'bell') {
      const star = st.stars[(Math.random() * st.stars.length) | 0]
      st.flares.push({ star, life: 1.6 })
    }
  },

  draw(s) {
    const { ctx, w, h, t, dt } = s
    const st = s.state

    const sky = ctx.createLinearGradient(0, 0, 0, h)
    sky.addColorStop(0, s.color('surface-0'))
    sky.addColorStop(1, s.color('surface-2'))
    ctx.fillStyle = sky
    ctx.fillRect(0, 0, w, h)

    // Stars, snapped to a 3px dot grid for the braille look.
    const g = 3
    ctx.fillStyle = s.color('text-hi')
    for (const st1 of st.stars) {
      const tw = s.noise(st1.ph + t * 0.35, st1.ph * 0.5)
      const a = st1.b * (0.35 + 0.65 * tw)
      if (a < 0.18) continue
      const x = Math.round((st1.x * w) / g) * g, y = Math.round((st1.y * h * 0.82) / g) * g
      ctx.globalAlpha = Math.min(1, a)
      ctx.fillRect(x, y, st1.big ? 2 : 1.4, st1.big ? 2 : 1.4)
    }
    ctx.globalAlpha = 1

    // Bell flares.
    st.flares = st.flares.filter((f) => {
      f.life -= dt
      const x = f.star.x * w, y = f.star.y * h * 0.82, r = 3 + (1.6 - f.life) * 10
      ctx.strokeStyle = s.color('accent', Math.max(0, f.life / 1.6))
      ctx.beginPath(); ctx.moveTo(x - r, y); ctx.lineTo(x + r, y); ctx.moveTo(x, y - r); ctx.lineTo(x, y + r); ctx.stroke()
      return f.life > 0
    })

    // Meteors.
    st.meteors = st.meteors.filter((m) => {
      if (m.wait > 0) { m.wait -= dt; return true }
      m.x += m.vx * dt; m.y += m.vy * dt; m.life -= dt
      const x = m.x * w, y = m.y * h
      const tail = ctx.createLinearGradient(x, y, x - m.vx * 160, y - m.vy * 160)
      tail.addColorStop(0, s.color('text-hi', Math.max(0, m.life)))
      tail.addColorStop(1, s.color('text-hi', 0))
      ctx.strokeStyle = tail
      ctx.lineWidth = 1.5
      ctx.beginPath(); ctx.moveTo(x, y); ctx.lineTo(x - m.vx * 160, y - m.vy * 160); ctx.stroke()
      return m.life > 0
    })

    // Dithered hills along the bottom.
    const px = 3
    ctx.fillStyle = s.color('surface-1')
    for (let x = 0; x < w; x += px) {
      const ridge = h * (0.86 + 0.05 * Math.sin(x * 0.004) + 0.03 * s.noise(x * 0.01, 3))
      ctx.fillRect(x, ridge + 6, px, h - ridge)
      for (let y = ridge - 6; y < ridge + 6; y += px) {
        if (BAYER[(((y / px) | 0) & 3) * 4 + (((x / px) | 0) & 3)] < (y - ridge + 6) / 12) ctx.fillRect(x, y, px, px)
      }
    }
  },
})
