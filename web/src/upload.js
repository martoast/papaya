import { upload } from './api.js'

// wireUploads installs drag-drop and paste handling.
//
// The listeners are on the window, not just the terminal area. A file dropped
// anywhere in the app uploads to the active session, and — more importantly — a
// drop that lands on the tab bar, the status bar, or a few pixels off the
// terminal never reaches the browser's default handler, which would navigate
// the page to the dropped file and tear down every open session.
export function wireUploads({ overlayEl, progressEl, barEl, getActiveTab, getTabAt, onNotice }) {
  let depth = 0

  const show = () => {
    overlayEl.dataset.on = 'true'
  }
  const hide = () => {
    depth = 0
    overlayEl.dataset.on = 'false'
  }

  const hasFiles = (e) => Array.from(e.dataTransfer?.types || []).includes('Files')

  // dragleave fires whenever the pointer crosses into a child element, so a
  // naive show/hide flickers. A depth counter keeps enter and leave balanced.
  window.addEventListener('dragenter', (e) => {
    if (!hasFiles(e)) return
    e.preventDefault()
    depth += 1
    if (depth === 1) show()
  })

  window.addEventListener('dragover', (e) => {
    if (!hasFiles(e)) return
    e.preventDefault() // required, or the drop event never fires
    e.dataTransfer.dropEffect = 'copy'
  })

  window.addEventListener('dragleave', (e) => {
    if (!hasFiles(e)) return
    depth -= 1
    if (depth <= 0) hide()
  })

  window.addEventListener('drop', (e) => {
    // Prevent the default even when there is no active session: navigating away
    // is far worse than a "no session" notice.
    if (!hasFiles(e)) return
    e.preventDefault()
    hide()

    const files = Array.from(e.dataTransfer.files || [])
    // In grid view, drop on the panel you meant: the file goes to that session
    // rather than to whichever one happened to be focused.
    if (files.length) sendAll(files, getTabAt?.(e.target) || null)
  })

  // Paste is the path used daily: screenshot to clipboard, then paste.
  document.addEventListener('paste', (e) => {
    const items = Array.from(e.clipboardData?.items || [])
    const images = items.filter((i) => i.kind === 'file' && i.type.startsWith('image/'))
    if (!images.length) return

    e.preventDefault()

    const files = images
      .map((item) => {
        const file = item.getAsFile()
        if (!file) return null
        const ext = (item.type.split('/')[1] || 'png').replace(/[^a-z0-9]/gi, '')
        // Clipboard images usually arrive as "image.png" or nameless.
        return { file, name: file.name || `pasted.${ext}` }
      })
      .filter(Boolean)

    if (files.length) sendAll(files)
  })

  // sendAll uploads sequentially. Concurrent uploads would race over the single
  // progress bar, and their injected paths would interleave at the shell cursor.
  async function sendAll(entries, target = null) {
    const tab = target || getActiveTab()
    if (!tab) {
      onNotice?.('open a session before uploading', 'error')
      return
    }

    const list = entries.map((entry) =>
      entry.file ? entry : { file: entry, name: entry.name },
    )

    progressEl.classList.remove('hidden')
    let failed = 0

    for (const [i, { file, name }] of list.entries()) {
      barEl.style.width = '0%'
      try {
        const res = await upload(tab.id, file, name, {
          onProgress: (ratio) => {
            barEl.style.width = `${Math.round(ratio * 100)}%`
          },
        })
        barEl.style.width = '100%'
        onNotice?.(
          list.length > 1
            ? `uploaded ${res.name} (${i + 1}/${list.length})`
            : `uploaded ${res.name}`,
        )
      } catch (err) {
        failed += 1
        onNotice?.(err.message || 'upload failed', 'error')
      }
    }

    setTimeout(() => {
      progressEl.classList.add('hidden')
      barEl.style.width = '0%'
    }, 600)

    // Return focus so the user can hit enter on the injected path immediately.
    if (failed < list.length) tab.focus()
  }

  return { sendAll }
}
