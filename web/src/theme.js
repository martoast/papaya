// xterm.js renders to canvas/WebGL, so Tailwind cannot style terminal content.
// Colours must be handed to the Terminal constructor as a plain object.
//
// To avoid two divergent palettes, the tokens are declared once in CSS and read
// back here at runtime. Changing a token in style.css changes both the chrome
// and the terminal.

const page = (name) =>
  getComputedStyle(document.documentElement).getPropertyValue(name).trim()

// While a theme's scene runs behind the terminals, xterm paints no background
// at all; the readable tint behind the text comes from CSS (.term-host in
// style.css), at the theme's opacity, the same whichever renderer is in use.
//
// The colour keeps the theme's own red, green and blue at zero alpha, because
// xterm answers programs that ask for the background colour (OSC 11) with it:
// a light theme must say "light", or CLI tools pick dark-mode colours.
const probe = document.createElement('canvas').getContext('2d')
function termBackground(bg) {
  if (document.documentElement.dataset.backdrop !== 'on') return bg
  probe.fillStyle = '#000'
  probe.fillStyle = bg
  const c = probe.fillStyle
  if (c.startsWith('#')) {
    const n = parseInt(c.slice(1), 16)
    return `rgba(${(n >> 16) & 255}, ${(n >> 8) & 255}, ${n & 255}, 0)`
  }
  return 'rgba(0, 0, 0, 0)'
}

// xtermTheme builds a terminal's palette from tokens (a tab's own theme, see
// colorsForTab); a missing token falls back to the page's.
export function xtermTheme(tokens = {}) {
  const v = (name) => tokens[name.replace(/^--color-/, '')] || page(name)
  return {
    background: termBackground(v('--color-term-bg')),
    foreground: v('--color-term-fg'),
    cursor: v('--color-term-cursor'),
    cursorAccent: v('--color-term-bg'),
    selectionBackground: v('--color-term-sel'),

    black: v('--color-ansi-black'),
    red: v('--color-ansi-red'),
    green: v('--color-ansi-green'),
    yellow: v('--color-ansi-yellow'),
    blue: v('--color-ansi-blue'),
    magenta: v('--color-ansi-magenta'),
    cyan: v('--color-ansi-cyan'),
    white: v('--color-ansi-white'),

    brightBlack: v('--color-ansi-bright-black'),
    brightRed: v('--color-ansi-bright-red'),
    brightGreen: v('--color-ansi-bright-green'),
    brightYellow: v('--color-ansi-bright-yellow'),
    brightBlue: v('--color-ansi-bright-blue'),
    brightMagenta: v('--color-ansi-bright-magenta'),
    brightCyan: v('--color-ansi-bright-cyan'),
    brightWhite: v('--color-ansi-bright-white'),
  }
}

export function monoFont() {
  return page('--font-mono') || 'monospace'
}
