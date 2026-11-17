// Thin wrappers over the REST API. Identity comes from the tailnet, so there
// are no tokens or credentials to attach here.

async function request(method, path, body) {
  const res = await fetch(path, {
    method,
    headers: body ? { 'Content-Type': 'application/json' } : undefined,
    body: body ? JSON.stringify(body) : undefined,
  })

  if (!res.ok) {
    let msg = `${method} ${path} failed (${res.status})`
    try {
      const data = await res.json()
      if (data?.error) msg = data.error
    } catch {
      // non-JSON error body; the status line is enough
    }
    const err = new Error(msg)
    err.status = res.status
    throw err
  }

  if (res.status === 204) return null
  return res.json()
}

export const api = {
  listSessions: () => request('GET', '/api/sessions'),

  createSession: ({ host, user, name, cols, rows }) =>
    request('POST', '/api/sessions', { host, user, name, cols, rows }),

  patchSession: (id, patch) => request('PATCH', `/api/sessions/${id}`, patch),

  deleteSession: (id) => request('DELETE', `/api/sessions/${id}`),

  listHosts: () => request('GET', '/api/hosts'),

  // Remote workspaces still running that no tab is showing — what papaya left
  // behind when it last stopped.
  listResumable: () => request('GET', '/api/resumable'),

  resumeSession: ({ host, user, name, workspace, cols, rows }) =>
    request('POST', '/api/sessions/resume', { host, user, name, workspace, cols, rows }),

  capabilities: () => request('GET', '/api/capabilities'),
  setVoiceKey: (key) => request('PUT', '/api/voice/key', { key }),
  themes: () => request('GET', '/api/themes'),
  theme: (name) => request('GET', `/api/themes/${encodeURIComponent(name)}`),
  useTheme: (name) => request('POST', '/api/themes/active', { name }),
  omarchyThemes: () => request('GET', '/api/omarchy'),
  useOmarchy: (name, switchAll = true) => request('POST', `/api/omarchy/${encodeURIComponent(name)}${switchAll ? '' : '?switch=0'}`),
  deleteVoiceKey: () => request('DELETE', '/api/voice/key'),

  // First-run setup: connect machines, test them, finish.
  setup: () => request('GET', '/api/setup'),
  saveMachines: (machines) => request('PUT', '/api/setup/machines', { machines }),
  testMachine: ({ host, user, method, trust }) =>
    request('POST', '/api/setup/test', { host, user, method, trust }),
  completeSetup: (complete = true) => request('POST', '/api/setup/complete', { complete }),
  seenMachines: () => request('POST', '/api/setup/seen'),
}

// upload posts a file with progress. XMLHttpRequest rather than fetch, because
// fetch has no upload progress event.
export function upload(sessionId, file, filename, { onProgress } = {}) {
  return new Promise((resolve, reject) => {
    const form = new FormData()
    form.append('file', file, filename || file.name || 'upload')

    const xhr = new XMLHttpRequest()
    xhr.open('POST', `/upload?session=${encodeURIComponent(sessionId)}`)

    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable && onProgress) onProgress(e.loaded / e.total)
    }

    xhr.onload = () => {
      if (xhr.status >= 200 && xhr.status < 300) {
        try {
          resolve(JSON.parse(xhr.responseText))
        } catch {
          reject(new Error('malformed upload response'))
        }
        return
      }
      if (xhr.status === 413) {
        reject(new Error('file is larger than the upload limit'))
        return
      }
      let msg = `upload failed (${xhr.status})`
      try {
        const data = JSON.parse(xhr.responseText)
        if (data?.error) msg = data.error
      } catch {
        // keep the status-based message
      }
      reject(new Error(msg))
    }

    xhr.onerror = () => reject(new Error('upload failed: network error'))
    xhr.send(form)
  })
}
