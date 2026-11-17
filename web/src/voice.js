// Voice input: record from the microphone, send the audio to papaya, and let the
// server put the transcript at the shell cursor.
//
// The OpenAI key never reaches this file — the browser talks only to papaya.

const PREFERRED_TYPES = [
  'audio/webm;codecs=opus',
  'audio/webm',
  'audio/ogg;codecs=opus',
  'audio/mp4',
]

function pickMimeType() {
  if (typeof MediaRecorder === 'undefined') return null
  for (const t of PREFERRED_TYPES) {
    if (MediaRecorder.isTypeSupported(t)) return t
  }
  return ''
}

function extensionFor(mimeType) {
  if (!mimeType) return 'webm'
  if (mimeType.includes('ogg')) return 'ogg'
  if (mimeType.includes('mp4')) return 'm4a'
  return 'webm'
}

// secureContextOK reports whether the browser will grant microphone access at
// all. getUserMedia is unavailable outside a secure context, so over plain HTTP
// the button would fail no matter what the server supports.
export function micAvailable() {
  return Boolean(
    window.isSecureContext &&
      navigator.mediaDevices &&
      navigator.mediaDevices.getUserMedia &&
      typeof MediaRecorder !== 'undefined',
  )
}

export function micUnavailableReason() {
  if (!window.isSecureContext) {
    return 'voice needs HTTPS — start papaya with PAPAYA_TLS=true'
  }
  if (typeof MediaRecorder === 'undefined' || !navigator.mediaDevices?.getUserMedia) {
    return 'this browser cannot record audio'
  }
  return ''
}

export class VoiceRecorder {
  constructor({ onStateChange, onNotice, onStream }) {
    this.onStateChange = onStateChange
    this.onNotice = onNotice
    // Handed the live MediaStream so the caller can meter it. Metering is a
    // read-only tap; it does not affect what gets recorded.
    this.onStream = onStream

    this.recording = false
    // Acquiring the microphone is asynchronous and can take a noticeable moment
    // the first time, or when another application holds the device. Without a
    // pending flag a second press during that window starts a *second*
    // recording instead of stopping the first.
    this.starting = false
    this.cancelRequested = false
    this.stream = null
    this.recorder = null
    this.chunks = []
    this.startedAt = 0
    this.stoppedPromise = null
  }

  get isRecording() {
    return this.recording
  }

  // isBusy covers the pending window as well, so the UI can treat a press as
  // "stop" from the instant recording was requested.
  get isBusy() {
    return this.recording || this.starting
  }

  async start() {
    if (this.isBusy) return
    this.starting = true
    this.cancelRequested = false

    try {
      await this.acquire()
    } finally {
      this.starting = false
    }
  }

  async acquire() {
    if (!micAvailable()) {
      this.onNotice?.(micUnavailableReason(), 'error')
      return
    }

    let stream
    try {
      stream = await navigator.mediaDevices.getUserMedia({
        audio: {
          echoCancellation: true,
          noiseSuppression: true,
          autoGainControl: true,
        },
      })
    } catch (err) {
      const denied = err?.name === 'NotAllowedError' || err?.name === 'SecurityError'
      this.onNotice?.(
        denied ? 'microphone permission denied' : 'no microphone available',
        'error',
      )
      return
    }

    const mimeType = pickMimeType()
    let recorder
    try {
      recorder = mimeType
        ? new MediaRecorder(stream, { mimeType })
        : new MediaRecorder(stream)
    } catch {
      stream.getTracks().forEach((t) => t.stop())
      this.onNotice?.('this browser cannot record audio', 'error')
      return
    }

    // Stopped before the device finished granting: release it and stay idle.
    if (this.cancelRequested) {
      stream.getTracks().forEach((t) => t.stop())
      this.onStateChange?.('idle')
      return
    }

    this.stream = stream
    this.recorder = recorder
    this.chunks = []
    this.startedAt = Date.now()

    recorder.ondataavailable = (e) => {
      if (e.data && e.data.size > 0) this.chunks.push(e.data)
    }

    // Resolve when the recorder has flushed everything, so stop() can await it.
    this.stoppedPromise = new Promise((resolve) => {
      recorder.onstop = () => resolve()
    })

    recorder.start()
    this.recording = true
    // State first: it establishes the recording start time that the level
    // callback compares against, and the meter starts reporting immediately.
    this.onStateChange?.('recording')
    this.onStream?.(stream)
  }

  // stop ends the recording and returns the audio, or null if there was none.
  async stop() {
    // Pressed again while the microphone was still being acquired: abandon the
    // pending start rather than leaving a recording nobody asked for.
    if (this.starting) {
      this.cancelRequested = true
      return null
    }
    if (!this.recording) return null

    // Deliberately no state change here: the caller decides whether the next
    // state is "transcribing" or "idle", and emitting "idle" first would make
    // the indicator flicker off and back on.
    this.recording = false

    const recorder = this.recorder
    const stream = this.stream
    this.recorder = null
    this.stream = null

    try {
      if (recorder && recorder.state !== 'inactive') recorder.stop()
      await this.stoppedPromise
    } finally {
      stream?.getTracks().forEach((t) => t.stop())
    }

    const durationMs = Date.now() - this.startedAt
    if (!this.chunks.length) return null

    const type = recorder?.mimeType || this.chunks[0]?.type || 'audio/webm'
    const blob = new Blob(this.chunks, { type })
    this.chunks = []

    // Anything this short is a mis-tap, not speech; sending it wastes a request
    // and usually transcribes as noise.
    if (durationMs < 350 || blob.size < 1024) return null

    return { blob, filename: `recording.${extensionFor(type)}`, durationMs }
  }

  cancel() {
    if (this.starting) {
      this.cancelRequested = true
      return
    }
    if (!this.recording) return
    this.recording = false
    this.onStateChange?.('idle')

    try {
      if (this.recorder && this.recorder.state !== 'inactive') this.recorder.stop()
    } catch {
      // already stopped
    }
    this.stream?.getTracks().forEach((t) => t.stop())

    this.recorder = null
    this.stream = null
    this.chunks = []
  }
}

// transcribe posts the recording and returns the recognised text.
// signal: aborting it cancels the transcription. The server types the text in
// only after the transcription comes back, so an abort before then means
// nothing is written.
export async function transcribe(sessionId, blob, filename, signal) {
  const form = new FormData()
  form.append('file', blob, filename)

  const res = await fetch(`/api/transcribe?session=${encodeURIComponent(sessionId)}`, {
    method: 'POST',
    body: form,
    signal,
  })

  if (!res.ok) {
    let msg = `transcription failed (${res.status})`
    try {
      const data = await res.json()
      if (data?.error) msg = data.error
    } catch {
      // keep the status-based message
    }
    const err = new Error(msg)
    err.status = res.status
    throw err
  }

  return res.json()
}
