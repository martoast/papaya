// Departure Board — an amber dot-matrix board, like a station's, listing your
// tabs as departures. Each row shows the time, the tab and its status:
//   ON TIME    nothing happening
//   WORKING    its agent is printing
//   BOARDING   its agent rang the bell: it needs you
//   MESSAGE    it just got a papaya-say message
// When a status changes, the row's letters spin through the alphabet and
// settle, like a split-flap board. A cue flashes "ALL ABOARD" across it.

let DOT = 5

// A 5x7 dot font, one string of 7 rows per character ('1' = lit).
const FONT = {
  A: '01110100011000111111100011000110001', B: '11110100011000111110100011000111110',
  C: '01110100011000010000100001000101110', D: '11110100011000110001100011000111110',
  E: '11111100001000011110100001000011111', F: '11111100001000011110100001000010000',
  G: '01110100011000010111100011000101111', H: '10001100011000111111100011000110001',
  I: '01110001000010000100001000010001110', J: '00111000100001000010000101001001100',
  K: '10001100101010011000101001001010001', L: '10000100001000010000100001000011111',
  M: '10001110111010110101100011000110001', N: '10001110011010110011100011000110001',
  O: '01110100011000110001100011000101110', P: '11110100011000111110100001000010000',
  Q: '01110100011000110001101011001001101', R: '11110100011000111110101001001010001',
  S: '01111100001000001110000010000111110', T: '11111001000010000100001000010000100',
  U: '10001100011000110001100011000101110', V: '10001100011000110001100010101000100',
  W: '10001100011000110101101011010101010', X: '10001100010101000100010101000110001',
  Y: '10001100010101000100001000010000100', Z: '11111000010001000100010001000011111',
  0: '01110100011001110101110011000101110', 1: '00100011000010000100001000010001110',
  2: '01110100010000100010001000100011111', 3: '11111000100010000010000011000101110',
  4: '00010001100101010010111110001000010', 5: '11111100001111000001000011000101110',
  6: '00110010001000011110100011000101110', 7: '11111000010001000100010000100001000',
  8: '01110100011000101110100011000101110', 9: '01110100011000101111000010001001100',
  ':': '00000011000110000000011000110000000', '-': '00000000000000011111000000000000000',
  '.': '00000000000000000000000000110001100', ' ': '00000000000000000000000000000000000',
}
const SPIN = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789'

const STATUS = { idle: 'ON TIME', working: 'WORKING', boarding: 'BOARDING', message: 'MESSAGE' }

papaya.scene({
  setup(s) {
    const st = s.state
    st.rnd = s.rand(41)
    st.rows = new Map() // tab id -> { name, status, until, shown: [chars], spin: [t] }
    st.flash = 0
  },

  resize(s) {
    const st = s.state
    DOT = s.w < 700 ? 3 : 5
    st.W = Math.ceil(s.w / DOT)
    st.H = Math.ceil(s.h / DOT)
    // The unlit dots of the whole panel, drawn once.
    const c = document.createElement('canvas')
    c.width = Math.ceil(s.w); c.height = Math.ceil(s.h)
    const g = c.getContext('2d')
    g.fillStyle = '#140d06'
    g.fillRect(0, 0, s.w, s.h)
    const glow = g.createRadialGradient(s.w * 0.75, s.h * 0.7, 0, s.w * 0.75, s.h * 0.7, Math.max(s.w, s.h) * 0.7)
    glow.addColorStop(0, 'rgba(120,70,20,0.18)')
    glow.addColorStop(1, 'rgba(0,0,0,0)')
    g.fillStyle = glow
    g.fillRect(0, 0, s.w, s.h)
    g.fillStyle = 'rgba(255,170,60,0.07)'
    const r = DOT * 0.32
    for (let y = 0; y < st.H; y++) for (let x = 0; x < st.W; x++) {
      g.beginPath(); g.arc(x * DOT + DOT / 2, y * DOT + DOT / 2, r, 0, 6.283); g.fill()
    }
    st.panel = c
    // Room for this many characters per row: time (5), a gap, name, gap, status (8).
    // A compact board, not a banner: at most 30 characters a row.
    st.cols = Math.min(30, Math.floor((st.W - 4) / 6))
  },

  init(s) {
    for (const tab of s.tabs) s.state.rows.set(tab.id, null)
  },

  on(s, ev) {
    const st = s.state
    const set = (id, status, hold) => {
      const r = st.rows.get(id)
      if (r) { r.status = status; r.until = s.t + hold }
    }
    if (ev.type === 'tab-close') st.rows.delete(ev.id)
    if (ev.type === 'activity') set(ev.id, 'working', 6)
    if (ev.type === 'bell') set(ev.id, 'boarding', 3600)
    if (ev.type === 'focus') { const r = st.rows.get(ev.id); if (r && r.status === 'boarding') r.status = 'idle' }
    if (ev.type === 'say') {
      const want = String(ev.to || '').toLowerCase().replace(/^papaya-/, '')
      for (const [id, r] of st.rows) if (r && r.name.toLowerCase().startsWith(want)) set(id, 'message', 8)
    }
    if (ev.type === 'cue') st.flash = 1
  },

  draw(s) {
    const st = s.state, ctx = s.ctx, dt = s.dt, t = s.t
    if (!st.W || !st.panel) return
    ctx.drawImage(st.panel, 0, 0)
    const r = DOT * 0.36

    // Keep a row for every tab, in tab order.
    for (const tab of s.tabs) {
      let row = st.rows.get(tab.id)
      if (!row) {
        row = { name: tab.name || tab.host, status: 'idle', until: 0, shown: [], spin: [] }
        st.rows.set(tab.id, row)
      }
      row.name = tab.name || tab.host || ''
    }

    const now = new Date()
    const clock = `${String(now.getHours()).padStart(2, '0')}:${String(now.getMinutes()).padStart(2, '0')}`
    const nameCols = Math.max(4, st.cols - 5 - 1 - 1 - 8)
    const lines = []
    lines.push({ text: ('DEPARTURES').padEnd(st.cols).slice(0, st.cols), head: true })
    // With no tabs open (or in the picker's preview) the board still has a
    // timetable to show.
    const sample = s.tabs.length === 0
    if (sample) {
      for (const [id, name, status] of [['~1', 'CLAUDE', 'working'], ['~2', 'CODEX', 'idle'], ['~3', 'SERVER', 'idle']]) {
        if (!st.rows.has(id)) st.rows.set(id, { name, status, until: Infinity, shown: [], spin: [] })
      }
    } else for (const id of [...st.rows.keys()]) if (id.startsWith('~')) st.rows.delete(id)
    const listed = sample ? [{ id: '~1' }, { id: '~2' }, { id: '~3' }] : s.tabs.slice(0, 8)
    for (const tab of listed) {
      const row = st.rows.get(tab.id)
      if (row.status !== 'idle' && row.status !== 'boarding' && t > row.until) row.status = 'idle'
      const name = row.name.toUpperCase().replace(/[^A-Z0-9 .:-]/g, ' ').slice(0, nameCols).padEnd(nameCols)
      lines.push({ text: `${clock} ${name} ${STATUS[row.status].padEnd(8)}`, row })
    }
    if (st.flash > 0) {
      st.flash = Math.max(0, st.flash - dt * 0.3)
      lines.splice(1, 0, { text: 'ALL ABOARD'.padStart(Math.floor((st.cols + 10) / 2)).padEnd(st.cols), head: true })
    }

    // The board sits at the bottom right, clear of most text.
    const rowsH = lines.length * 9
    const oy = st.H - rowsH - 4
    const ox = st.W - st.cols * 6 - 2
    lines.forEach((line, li) => {
      const row = line.row
      const chars = [...line.text]
      if (row) {
        // Split-flap: a changed character spins for a moment before it lands.
        chars.forEach((ch, i) => {
          if (row.shown[i] !== ch) { row.shown[i] = ch; row.spin[i] = t + 0.25 + i * 0.03 }
        })
      }
      chars.forEach((ch, i) => {
        let c = ch
        if (row && row.spin[i] > t) c = SPIN[Math.floor((t * 30 + i * 7) % SPIN.length)]
        const glyph = FONT[c] || FONT[' ']
        const hot = line.head ? '#ffd07a' : row && row.status === 'boarding' && Math.sin(t * 5) > 0 ? '#ff6b4a' : row && row.status === 'working' && i > chars.length - 9 ? '#c8e05a' : '#ffb02e'
        for (let gy = 0; gy < 7; gy++) for (let gx = 0; gx < 5; gx++) {
          if (glyph[gy * 5 + gx] !== '1') continue
          const px = (ox + i * 6 + gx) * DOT + DOT / 2, py = (oy + li * 9 + gy) * DOT + DOT / 2
          ctx.fillStyle = hot
          ctx.globalAlpha = 0.25
          ctx.beginPath(); ctx.arc(px, py, r * 1.9, 0, 6.283); ctx.fill()
          ctx.globalAlpha = 1
          ctx.beginPath(); ctx.arc(px, py, r, 0, 6.283); ctx.fill()
        }
      })
    })
    ctx.globalAlpha = 1
  },
})
