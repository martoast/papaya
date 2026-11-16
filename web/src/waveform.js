// Live waveform for the recording indicator.
//
// The point is not decoration: without it there is no way to tell a working
// microphone from a muted one until the transcript comes back empty. The meter
// also reports peak level so the UI can say "no audio detected" while there is
// still time to do something about it.

const BAR_WIDTH = 3
const BAR_GAP = 2
const SILENCE_THRESHOLD = 0.012

export class WaveformMeter {
  constructor(canvas, { onLevel } = {}) {
    this.canvas = canvas
    this.onLevel = onLevel

    this.ctx = canvas.getContext('2d')
    this.audioCtx = null
    this.analyser = null
    this.source = null
    this.raf = 0
    this.levels = []
    this.peak = 0
    this.running = false
  }

  // attach starts metering a live microphone stream.
  async attach(stream) {
    const AudioCtx = window.AudioContext || window.webkitAudioContext
    if (!AudioCtx) return false

    try {
      this.audioCtx = new AudioCtx()
      // Browsers may hand back a suspended context outside a user gesture.
      if (this.audioCtx.state === 'suspended') await this.audioCtx.resume()

      this.source = this.audioCtx.createMediaStreamSource(stream)
      this.analyser = this.audioCtx.createAnalyser()
      // Small FFT: this is a level meter, not a spectrum analyser, and a short
      // window keeps it responsive to speech onsets.
      this.analyser.fftSize = 1024
      this.analyser.smoothingTimeConstant = 0.6
      this.source.connect(this.analyser)
    } catch {
      this.detach()
      return false
    }

    this.levels = []
    this.peak = 0
    this.running = true
    this.resize()
    this.tick()
    return true
  }

  resize() {
    const dpr = window.devicePixelRatio || 1
    const rect = this.canvas.getBoundingClientRect()
    const w = Math.max(1, Math.round(rect.width || this.canvas.clientWidth || 160))
    const h = Math.max(1, Math.round(rect.height || this.canvas.clientHeight || 28))

    this.canvas.width = w * dpr
    this.canvas.height = h * dpr
    this.ctx.setTransform(dpr, 0, 0, dpr, 0, 0)

    this.cssWidth = w
    this.cssHeight = h
    this.capacity = Math.max(8, Math.floor(w / (BAR_WIDTH + BAR_GAP)))
  }

  tick = () => {
    if (!this.running || !this.analyser) return

    const buf = new Uint8Array(this.analyser.fftSize)
    this.analyser.getByteTimeDomainData(buf)

    // RMS around the 128 midpoint gives a stable perceptual level; peak alone
    // jumps around too much to read.
    let sum = 0
    for (let i = 0; i < buf.length; i++) {
      const v = (buf[i] - 128) / 128
      sum += v * v
    }
    const rms = Math.sqrt(sum / buf.length)

    this.peak = Math.max(this.peak, rms)
    this.onLevel?.(rms, this.peak)

    this.levels.push(rms)
    if (this.levels.length > this.capacity) this.levels.shift()

    this.draw()
    this.raf = requestAnimationFrame(this.tick)
  }

  draw() {
    const { ctx, cssWidth: w, cssHeight: h } = this
    ctx.clearRect(0, 0, w, h)

    const mid = h / 2
    const styles = getComputedStyle(document.documentElement)
    const active = styles.getPropertyValue('--color-state-err').trim() || '#f85149'
    const quiet = styles.getPropertyValue('--color-surface-3').trim() || '#30363d'

    // Newest bar sits at the right edge and older ones scroll left.
    const start = w - this.levels.length * (BAR_WIDTH + BAR_GAP)

    for (let i = 0; i < this.levels.length; i++) {
      const level = this.levels[i]
      // sqrt keeps quiet speech visible without clipping loud speech.
      const amp = Math.min(1, Math.sqrt(level * 4))
      const barH = Math.max(2, amp * (h - 4))
      const x = start + i * (BAR_WIDTH + BAR_GAP)

      ctx.fillStyle = level > SILENCE_THRESHOLD ? active : quiet
      ctx.fillRect(x, mid - barH / 2, BAR_WIDTH, barH)
    }
  }

  // hasSignal reports whether anything above the noise floor was ever heard.
  hasSignal() {
    return this.peak > SILENCE_THRESHOLD
  }

  detach() {
    this.running = false
    cancelAnimationFrame(this.raf)

    try {
      this.source?.disconnect()
      this.analyser?.disconnect()
    } catch {
      // already torn down
    }
    // Closing releases the audio hardware; leaking contexts eventually throws.
    this.audioCtx?.close?.().catch(() => {})

    this.source = null
    this.analyser = null
    this.audioCtx = null

    if (this.ctx && this.cssWidth) {
      this.ctx.clearRect(0, 0, this.cssWidth, this.cssHeight)
    }
    this.levels = []
  }
}
