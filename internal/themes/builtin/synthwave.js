// Synthwave — a striped sun over a neon grid that rolls toward you.
//
// Agent output nudges the grid's glow; a papaya-say message races down a grid
// line; a cue flares the sun.

papaya.scene({
  setup(s) {
    s.state.flare = 0
    s.state.pulse = 0
    s.state.racers = []
  },

  on(s, ev) {
    const st = s.state
    if (ev.type === 'activity') st.pulse = Math.min(1, st.pulse + 0.12)
    if (ev.type === 'say') st.racers.push({ lane: Math.floor(Math.random() * 9) - 4, z: 0 })
    if (ev.type === 'cue' || ev.type === 'bell') st.flare = 1
  },

  draw(s) {
    const { ctx, w, h, t, dt } = s
    const st = s.state
    const horizon = h * 0.6
    const move = s.reducedMotion ? 0 : t

    const sky = ctx.createLinearGradient(0, 0, 0, horizon)
    sky.addColorStop(0, '#0b0420')
    sky.addColorStop(0.7, '#2a0b45')
    sky.addColorStop(1, '#5a1458')
    ctx.fillStyle = sky
    ctx.fillRect(0, 0, w, horizon)

    // The sun, striped more toward its base.
    const r = Math.min(w, h) * 0.2 * (1 + st.flare * 0.06)
    const cx = w * 0.62, cy = horizon - r * 0.35
    const sun = ctx.createLinearGradient(0, cy - r, 0, cy + r)
    sun.addColorStop(0, '#ffe36e')
    sun.addColorStop(0.55, '#ff7a59')
    sun.addColorStop(1, '#ff2d95')
    ctx.save()
    ctx.beginPath(); ctx.arc(cx, cy, r, 0, Math.PI * 2); ctx.clip()
    ctx.fillStyle = sun
    ctx.fillRect(cx - r, cy - r, r * 2, r * 2)
    ctx.fillStyle = '#2a0b45'
    for (let i = 0; i < 7; i++) {
      const y = cy + r * (0.05 + i * 0.14), gap = 1.5 + i * 1.6
      ctx.fillRect(cx - r, y, r * 2, gap)
    }
    ctx.restore()
    if (st.flare > 0) {
      const glow = ctx.createRadialGradient(cx, cy, r * 0.8, cx, cy, r * 2.2)
      glow.addColorStop(0, `rgba(255,120,150,${0.35 * st.flare})`)
      glow.addColorStop(1, 'rgba(255,120,150,0)')
      ctx.fillStyle = glow
      ctx.fillRect(0, 0, w, horizon)
      st.flare = Math.max(0, st.flare - dt * 0.7)
    }

    // Mountains.
    ctx.fillStyle = '#16052b'
    ctx.beginPath(); ctx.moveTo(0, horizon)
    for (let x = 0; x <= w; x += 24) ctx.lineTo(x, horizon - 18 - 46 * s.noise(x * 0.006, 1) * (x < w * 0.45 || x > w * 0.8 ? 1 : 0.35))
    ctx.lineTo(w, horizon); ctx.fill()

    // Floor and grid.
    ctx.fillStyle = '#0a0216'
    ctx.fillRect(0, horizon, w, h - horizon)
    const glow = 0.45 + 0.4 * st.pulse
    ctx.strokeStyle = `rgba(255,60,190,${glow})`
    ctx.lineWidth = 1
    const vx = w * 0.5
    for (let i = -24; i <= 24; i++) {
      ctx.beginPath(); ctx.moveTo(vx + i * 14, horizon); ctx.lineTo(vx + i * w * 0.16, h); ctx.stroke()
    }
    const off = (move * 0.35) % 1
    for (let k = 0; k < 18; k++) {
      const z = (k + off) / 18
      const y = horizon + (h - horizon) * z * z
      ctx.globalAlpha = Math.min(1, z * 1.6)
      ctx.beginPath(); ctx.moveTo(0, y); ctx.lineTo(w, y); ctx.stroke()
    }
    ctx.globalAlpha = 1
    st.pulse = Math.max(0, st.pulse - dt * 0.4)

    // papaya-say racers: a bright dash running down one grid lane.
    st.racers = st.racers.filter((rc) => {
      rc.z += dt * 0.7
      const z = rc.z * rc.z
      const y = horizon + (h - horizon) * z
      const x = vx + rc.lane * (14 + (w * 0.16 - 14) * z)
      ctx.fillStyle = '#7ff9ff'
      ctx.shadowColor = '#7ff9ff'; ctx.shadowBlur = 12
      ctx.fillRect(x - 2, y - 10 * z - 2, 4, 10 * z + 4)
      ctx.shadowBlur = 0
      return rc.z < 1
    })
  },
})
