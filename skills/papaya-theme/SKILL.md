---
name: papaya-theme
description: Make, change and switch Papaya themes — colours plus an optional animated backdrop drawn behind the terminals. Use whenever your person asks for a theme, a look, a vibe, a background, colours, or "make Papaya look like …", and when you want to celebrate a moment (a merge, a passing test run) on their screen. Changes show live in every Papaya window they have open; they do not need to do anything.
---

# papaya-theme — themes your person only has to describe

Your person says what they want ("a koi pond", "synthwave sunset with pink
accents", "calm, low contrast, like paper"). You build it and push it. Every
Papaya window they have open switches immediately. Never ask them to click,
reload or copy anything.

`PAPAYA_URL` must point at the Papaya server (e.g. `https://papaya.your-tailnet.ts.net`).

## The loop

```bash
papaya-theme new koi-pond            # writes ./koi-pond/theme.json + scene.js (a starter)
# … edit both files …
papaya-theme push ./koi-pond         # upload, switch to it, wait for the browser's report
papaya-theme status                  # running at N fps, or the scene's error with scene.js line numbers
```

`push` prints the browser's verdict. If it says **ERROR**, fix scene.js and
push again; the error's line numbers are lines of your scene.js. If it says
**waiting: no Papaya window is open**, the theme is saved and switched; it will
run when they next open Papaya (tell them so). Iterate until it runs, then tell
your person it is on.

Other commands:

```bash
papaya-theme list                    # all themes, * marks the one on
papaya-theme use honu-lagoon         # switch (papaya-theme list shows all the built-ins)
papaya-theme use default             # back to Papaya's plain look
papaya-theme pull orchard ./mine     # copy any theme (built-ins too) to start from
papaya-theme cue merged              # fire a moment the scene can react to
papaya-theme rm koi-pond
```

**Wallpapers.** Put `background.webp` (or .png/.jpg, up to 8 MB) next to
theme.json and `push` uploads it. With no scene.js, Papaya draws it as a
still, cover-fitted frame, with a light sweep on `say`, an edge glow on `bell` and sparks on
`cue`. A scene can draw it itself through `s.image` (an `<img>`, already
decoded).

Built-in themes cannot be overwritten: pull one and push it under a new name.

## theme.json

```json
{
  "name": "koi-pond",
  "description": "Pixel koi drifting under lily pads.",
  "opacity": 0.8,
  "colors": { "surface-0": "#0b2a2a", "accent": "#ff8a3d", "term-bg": "#0b2a2a", "term-fg": "#e8fff7" }
}
```

- `name`: lowercase, digits, dashes; must match what you push.
- `opacity` (0.2–1): how solid terminal backgrounds are while the scene runs.
  0.75–0.85 keeps text readable; lower only for very calm scenes.
- `colors`: any of these tokens, each `#rgb`, `#rrggbb`, `#rrggbbaa`, or
  `rgb()/hsl()/oklch()` with plain numbers. Leave a token out to keep Papaya's.
  - chrome: `surface-0` (page), `surface-1` (tab and status bars), `surface-2`,
    `surface-3` (borders), `text-hi`, `text-lo`, `accent`,
    `state-ok`, `state-warn`, `state-err`
  - terminal: `term-bg`, `term-fg`, `term-cursor`, `term-sel` (selection)
  - the 16 ANSI colours: `ansi-black` … `ansi-white`,
    `ansi-bright-black` … `ansi-bright-white`
  (`red green yellow blue magenta cyan` in between.)

Set `term-bg` close to `surface-0`.

**Light themes** work, but command-line agents choose their own colours: after
switching to a light theme, tell your person to switch their agent to its
light mode too (Claude Code: `/theme`), or its text may be pale. Check contrast: `term-fg` on `term-bg` must
stay easy to read, and so must every ANSI colour, because agents print in them.
A beautiful theme nobody can read is a failed theme.

## scene.js

Optional. An animated backdrop behind the terminals, drawn on a full-window
canvas. It runs in a sandboxed frame: **no network, no storage, no DOM outside
its own canvas, no access to the terminals.** Don't try to fetch images or
fonts; draw everything (pixel art, dithering, gradients, particles).

```js
papaya.scene({
  context: '2d',             // or 'webgl' / 'webgl2'; default '2d'
  setup(s) {},               // once, before the first frame
  resize(s) {},              // after setup and on every resize (s.w, s.h updated)
  init(s) {},                // when the tab list arrives (s.tabs)
  draw(s) {},                // every frame
  on(s, ev) {},              // an event (below)
})
```

`s` (the same object every call):

| field | |
|---|---|
| `s.ctx`, `s.canvas` | the context and canvas; for 2D, already scaled so you draw in CSS pixels |
| `s.w`, `s.h`, `s.dpr` | size in CSS pixels, device pixel ratio |
| `s.t`, `s.dt`, `s.frame` | seconds since start, seconds since last frame, frame count |
| `s.colors`, `s.color(name, alpha?)` | the theme's colours; `color('accent', 0.4)` → `rgba(...)`; any CSS colour works too |
| `s.tabs` | `[{ id, name, host }]` open tabs, kept current |
| `s.state` | an object for your own data |
| `s.rand(seed)` | seeded random: returns a function giving 0..1 |
| `s.noise(x, y)` | smooth 2D noise, 0..1 |
| `s.lerp(a,b,t)`, `s.clamp(v,lo,hi)` | helpers |
| `s.image` | the theme's wallpaper as a decoded `<img>`, or null |
| `s.reducedMotion` | the person asked for less motion: keep it slow and still; Papaya also caps drawing at 2 fps |

Events (`ev.type`):

| type | fields | when |
|---|---|---|
| `tab-open`, `tab-close`, `tab-rename` | `id`, `tab`, `host` | tabs change (`s.tabs` is already updated) |
| `focus` | `id`, `tab` | the person switched to a tab |
| `activity` | `id`, `tab` | a tab is printing (at most every 1.5 s per tab) — an agent at work |
| `bell` | `id`, `tab` | a tab rang the bell — usually an agent that finished or needs them |
| `say` | `from`, `to` (tab names) | one agent messaged another with papaya-say |
| `done` | `id`, `tab` | a stretch of work in a tab ended while nobody was looking: the agent finished or is waiting |
| `cue` | `name` | someone ran `papaya-theme cue <name>` |

Make events visible: a glow where a tab works, something travelling between
tabs on `say`, a celebration on `cue`. That is what makes a Papaya theme feel
alive rather than a wallpaper.

### Rules for a good scene

- **Quiet behind text.** The scene sits under translucent terminals. Keep it
  low-contrast and mostly dark (or mostly light for a light theme); put the
  bright, busy parts near the edges and in moments (events), not everywhere.
- **Cheap.** Aim for 60 fps on a laptop and do not melt a phone: avoid
  per-pixel work at full resolution. For pixel art, draw into a small offscreen
  canvas (or an `ImageData` at ¼ size) and scale it up with
  `ctx.imageSmoothingEnabled = false`. `status` reports the fps; under ~30,
  simplify.
- **No `</script>`, `<script` or `<!--` anywhere in scene.js**, even in strings
  or comments (the push is refused). Write `'<' + '/script'` if you must.
- Max 256 KB.

### Celebrating

When something good happens in your own work — a merge, a deploy, all tests
green — you may run `papaya-theme cue merged` (or `deployed`, `green`). Scenes
that react to `cue` show it. Once per real moment; never in a loop.

## Examples to learn from

`papaya-theme pull orchard`, `pull night-sky`, `pull synthwave` give you three
complete scenes: dithered pixel art straight into ImageData with per-tab seeds
(orchard), dot stars with meteors (night-sky), and a 2D-canvas vector scene
(synthwave).

## Rules

- Do what your person asked for. Don't switch their theme unprompted, and
  don't remove themes you didn't make.
- If they dislike it, `papaya-theme use default` puts things back instantly.
- A theme is visible to everyone who uses this Papaya. Keep names and content
  appropriate for that.
