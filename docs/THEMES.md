# Themes: `papaya-theme`

A person tells their coding agent what they want Papaya to look like. The agent
writes a theme and pushes it; every Papaya window that person has open switches
at once. Nobody clicks, reloads or copies anything.

A theme is two files:

- `theme.json`: name, description, colours for the chrome and the terminal,
  and how solid terminals stay while a backdrop runs.
- `scene.js` (optional): an animated backdrop drawn behind the terminals,
  reacting to what is happening (tabs working, bells, agents messaging each
  other, cues).

The agent-facing guide, with the full format and scene API, is the skill
[`skills/papaya-theme/SKILL.md`](../skills/papaya-theme/SKILL.md). This page
covers how it works, why it is safe, and how to install it.

## Using it

```bash
papaya-theme list                    # themes; * marks the one on
papaya-theme new koi-pond            # ./koi-pond/theme.json + scene.js, a starter
papaya-theme push ./koi-pond         # upload, switch, wait for the browser's report
papaya-theme status                  # running at N fps / the error / waiting
papaya-theme use night-sky           # switch; "default" is the plain look
papaya-theme pull orchard ./mine     # copy a theme to start from (built-ins too)
papaya-theme cue merged              # fire a moment scenes react to
papaya-theme rm koi-pond
```

Built-in: **koi-pond** (a mosaic pond; a koi per tab), **orchard** (every tab a seed that glows while its agent works;
papaya-say messages fly between seeds), **night-sky** and **synthwave**. They
are read-only; pull one and push it under a new name.

## Omarchy themes and wallpapers

`papaya-theme omarchy <name>` imports a theme from
[Omarchy](https://github.com/omacom/omarchy) (MIT): its `colors.toml` becomes
Papaya's palette (`themes.FromOmarchy` maps background shades to the chrome,
foreground shades to text, and the eight colours and their bright variants to
the ANSI slots), and its first wallpaper becomes the backdrop. Nothing from
Omarchy is bundled with Papaya: the command downloads from Omarchy's
repository on the machine it runs on, the way Omarchy itself does, so new
Omarchy themes work the day they ship. Omarchy's code is MIT; its wallpapers
come from many sources, which is one more reason Papaya does not redistribute
them.

A theme's wallpaper (`PUT /api/themes/{name}/image`, WebP/PNG/JPEG recognised
by content, up to 8 MB) is inlined into the sandboxed frame as a `data:` URL,
so the frame still needs no network. Scene URLs carry the theme's version and
are cached by the browser, so a large wallpaper is fetched once.

## The feedback loop

An agent cannot see the screen, so Papaya tells it what happened. The browser
runs the scene and reports back: `running` with the frame rate after about
three seconds, or `error` with the message and stack. The server rewrites the
stack's line numbers to point into the agent's `scene.js`, and `push` waits up
to eight seconds for the report, so the agent gets a verdict in the same
command:

```
pushed koi-pond and switched to it.
theme:   koi-pond (on)
viewers: 2 open Papaya window(s)
scene:   ERROR, the browser stopped it:
         ReferenceError: fishes is not defined
             at Object.draw (scene.js:41:9)
```

A scene that fails is removed from the page, so a mistake never leaves a blank
backdrop behind the terminals. If no Papaya window is open, `status` says
`waiting`, and the theme runs when one opens.

## How it works

```
agent                      papaya server                      every open browser
─────                      ─────────────                      ──────────────────
papaya-theme push ──PUT──▶ /api/themes/{name}         (theme.json, validated)
                  ──PUT──▶ /api/themes/{name}/scene   (scene.js, validated)
                  ──POST─▶ /api/themes/active ── SSE "theme" ──▶ GET /api/theme
                                                                 apply colours
                                                                 <iframe sandbox src=/theme/scene/{name}>
                                                       ◀── POST /api/themes/{name}/status (running | error)
papaya-theme status ─GET─▶ /api/themes/{name}/status
```

- Themes live in `$PAPAYA_STATE_DIR/themes/<name>.json`; each person's choice
  in `active.json` there. Everyone using one Papaya shares the library, and
  each person picks their own theme.
- `/api/events` is a server-sent event stream per person: `theme` (reload it),
  `cue` (a named moment) and `say` (a papaya-say message passed between two
  tabs: names only, never the text).
- In the browser, tab code reports `tab-open`, `tab-close`, `tab-rename`,
  `focus`, `activity` (a tab printing, at most every 1.5 s per tab) and `bell`.
  All of these are forwarded to the scene.

## Why it is safe

A scene is arbitrary JavaScript, often written by an agent and possibly shared
by someone else. Papaya's page holds live shells on every machine you own, so
the scene must never run there. It runs in a frame that:

- is `sandbox="allow-scripts"` **without** `allow-same-origin`: an opaque
  origin, so no access to Papaya's DOM, cookies or storage, and no way to call
  its API as you;
- carries its own Content-Security-Policy: `default-src 'none'`,
  `connect-src 'none'`, scripts only by a per-response nonce. No fetch, no
  WebSocket, no images or fonts from anywhere, no navigation, no forms;
- is `inert`, `tabindex=-1` and `pointer-events: none`: it cannot take focus,
  so it never receives a keystroke or a click;
- is the only document Papaya lets be framed at all (`frame-ancestors 'self'`
  on it, `DENY` everywhere else).

What crosses into the frame is event types and tab names. What comes out is
`ready`, `running` with a frame rate, or an error string; the page checks the
message's source and version and forwards only those to the server.

Colours do reach the page, as CSS custom properties, so each one is checked:
only the known token names, and only hex values or colour functions with plain
numbers (no `url(`, no `;`, no `var(`). The scene is inlined into the frame's
`<script>`, so `</script`, `<script` and `<!--` are refused, with the line.
Theme writes go through the same CSRF guard as every other state change.

## Rendering

xterm's WebGL renderer paints an opaque background, so while a backdrop runs
the terminals use the DOM renderer, with a transparent xterm background and the
readable tint drawn by CSS at the theme's `opacity`. With the plain look they
go back to WebGL. Heavy output scrolls a little slower with a backdrop on.

## Installing on a machine

Same as papaya-say: the command on the agents' PATH, the skill in Claude
Code's skills folder, `PAPAYA_URL` in the environment.

```bash
# from a papaya checkout; H is the machine to install on
H=you@devbox-1
cat bin/papaya-theme | ssh $H 'mkdir -p ~/.local/bin && cat > ~/.local/bin/papaya-theme && chmod 755 ~/.local/bin/papaya-theme'
tar -C skills -cf - papaya-theme | ssh $H 'mkdir -p ~/.claude/skills && tar -C ~/.claude/skills -xf -'
ssh $H 'PAPAYA_URL=https://papaya.your-tailnet.ts.net ~/.local/bin/papaya-theme list'   # must list the themes
```

`pull` needs `python3` on the agent's machine; everything else needs only bash
and curl.
