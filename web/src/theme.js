// xterm.js renders to canvas/WebGL, so Tailwind cannot style terminal content.
// Colours must be handed to the Terminal constructor as a plain object.
//
// To avoid two divergent palettes, the tokens are declared once in CSS and read
// back here at runtime. Changing a token in style.css changes both the chrome
// and the terminal.

const v = (name) =>
  getComputedStyle(document.documentElement).getPropertyValue(name).trim()

export function xtermTheme() {
  return {
    background: v('--color-term-bg'),
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
  return v('--font-mono') || 'monospace'
}
