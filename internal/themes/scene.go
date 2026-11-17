package themes

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// SceneCSP is the policy for the scene frame. The frame is also sandboxed by
// the page (sandbox="allow-scripts", no allow-same-origin), so it has an opaque
// origin: no cookies, no storage, no way into the parent's DOM. This policy
// takes away the network as well, so a scene can draw and nothing else.
//
// nonce authorises exactly the three inline scripts this document carries.
func SceneCSP(nonce string) string {
	return "default-src 'none'; " +
		"script-src 'nonce-" + nonce + "'; " +
		"style-src 'unsafe-inline'; " +
		"img-src data: blob:; " +
		"font-src data:; " +
		"connect-src 'none'; " +
		"worker-src 'none'; " +
		"frame-src 'none'; " +
		"form-action 'none'; " +
		"base-uri 'none'; " +
		"frame-ancestors 'self'"
}

// NewNonce returns a fresh CSP nonce.
func NewNonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawStdEncoding.EncodeToString(b)
}

// SceneDocument renders the frame's HTML: the runtime, the theme's scene and
// the call that starts it. The scene has already been checked by Validate for
// sequences that would end the <script> element. Theme data goes in as JSON,
// which encoding/json escapes for HTML (<, > and & become < and so on).
func SceneDocument(t *Theme, nonce string, image []byte) []byte {
	return sceneDocument(t, nonce, image, false)
}

// PosterDocument is the scene drawn for about a second and then held still:
// the theme picker's preview of an animated theme, without twenty animations
// running at once on a phone.
func PosterDocument(t *Theme, nonce string, image []byte) []byte {
	return sceneDocument(t, nonce, image, true)
}

func sceneDocument(t *Theme, nonce string, image []byte, poster bool) []byte {
	head := sceneHead(t, nonce, image, poster)
	scene := t.Scene
	if scene == "" && len(image) > 0 {
		scene = WallpaperScene
	}
	var b strings.Builder
	b.WriteString(head)
	b.WriteString(scene)
	b.WriteString("\n</script>\n<script nonce=\"" + nonce + "\">window.__papayaStart()</script></body></html>")
	return []byte(b.String())
}

// sceneHead is everything in the document before the scene's first line.
func sceneHead(t *Theme, nonce string, image []byte, poster bool) string {
	meta, _ := json.Marshal(map[string]any{
		"name":    t.Name,
		"version": t.Version(),
		"colors":  t.Colors,
		"poster":  poster,
	})
	var b strings.Builder
	b.WriteString(`<!doctype html><html><head><meta charset="utf-8">` +
		`<style>html,body{margin:0;height:100%;overflow:hidden;background:transparent}` +
		`canvas{position:fixed;inset:0;width:100%;height:100%;display:block}</style></head>` +
		`<body><canvas id="c"></canvas>`)
	b.WriteString(`<script nonce="` + nonce + `">window.__PAPAYA_THEME__=`)
	b.Write(meta)
	b.WriteString(";")
	if len(image) > 0 && t.ImageType != "" {
		// On the same line as the theme data, so scene line numbers do not move.
		b.WriteString(`window.__PAPAYA_IMAGE__="data:` + t.ImageType + `;base64,` + base64.StdEncoding.EncodeToString(image) + `";`)
	}
	b.WriteString("\n")
	b.WriteString(runtimeJS)
	b.WriteString("</script>\n<script nonce=\"" + nonce + "\">\n")
	return b.String()
}

var stackLineRE = regexp.MustCompile(`/theme/scene/[^\s:)]+:(\d+):(\d+)`)

// SceneError rewrites a browser's error message so line numbers point into
// scene.js rather than into the frame document, which the agent never sees.
func SceneError(t *Theme, msg string) string {
	offset := strings.Count(sceneHead(t, "x", nil, false), "\n")
	lines := strings.Count(t.Scene, "\n") + 1
	return stackLineRE.ReplaceAllStringFunc(msg, func(m string) string {
		sub := stackLineRE.FindStringSubmatch(m)
		n, _ := strconv.Atoi(sub[1])
		if n-offset >= 1 && n-offset <= lines {
			return "scene.js:" + strconv.Itoa(n-offset) + ":" + sub[2]
		}
		return "papaya-runtime:" + sub[1] + ":" + sub[2]
	})
}

// WallpaperScene draws a theme's wallpaper when it has no scene of its own:
// a still, cover-fitted, centred frame. It used to drift and zoom, which made
// people dizzy on a phone. The picture is drawn once per size; it is only
// redrawn while an event's moment (a cue, a message, a bell) plays over it.
const WallpaperScene = `// Papaya's wallpaper scene.
papaya.scene({
  setup(s) { s.state.flash = 0; s.state.sweep = -1; s.state.glow = 0; s.state.sparks = [] },
  on(s, ev) {
    if (ev.type === 'cue') {
      s.state.flash = 1
      for (let i = 0; i < 90; i++) s.state.sparks.push({ x: Math.random() * s.w, y: s.h + Math.random() * 40, v: 40 + Math.random() * 120, life: 1.5 + Math.random() * 1.5 })
    }
    if (ev.type === 'say') s.state.sweep = 0
    if (ev.type === 'bell') s.state.glow = 1
  },
  draw(s) {
    const { ctx, w, h, t, dt } = s
    const img = s.image
    if (!img) return
    const st = s.state
    const busy = st.sweep >= 0 || st.glow > 0 || st.flash > 0 || st.sparks.length > 0
    const size = w + 'x' + h
    if (!busy && st.drawn === size) return
    st.drawn = size
    const scale = Math.max(w / img.naturalWidth, h / img.naturalHeight)
    const iw = img.naturalWidth * scale, ih = img.naturalHeight * scale
    ctx.drawImage(img, (w - iw) / 2, (h - ih) / 2, iw, ih)
    if (st.sweep >= 0) {
      const x = st.sweep * (w + 600) - 300
      const g = ctx.createLinearGradient(x - 200, 0, x + 200, h)
      g.addColorStop(0, 'rgba(255,255,255,0)'); g.addColorStop(0.5, 'rgba(255,255,255,0.10)'); g.addColorStop(1, 'rgba(255,255,255,0)')
      ctx.fillStyle = g; ctx.fillRect(0, 0, w, h)
      st.sweep += dt * 0.7
      if (st.sweep > 1) st.sweep = -1
    }
    if (st.glow > 0) {
      ctx.strokeStyle = s.color('accent', 0.5 * st.glow); ctx.lineWidth = 6 * st.glow
      ctx.strokeRect(3, 3, w - 6, h - 6)
      st.glow = Math.max(0, st.glow - dt * 0.8)
    }
    if (st.flash > 0) {
      ctx.fillStyle = s.color('accent', 0.18 * st.flash); ctx.fillRect(0, 0, w, h)
      st.flash = Math.max(0, st.flash - dt * 0.6)
    }
    ctx.fillStyle = s.color('accent')
    st.sparks = st.sparks.filter((p) => {
      p.y -= p.v * dt; p.life -= dt
      ctx.globalAlpha = Math.max(0, Math.min(1, p.life)); ctx.fillRect(p.x, p.y, 2, 2)
      return p.life > 0
    })
    ctx.globalAlpha = 1
    // A moment just ended: one clean redraw of the still picture next frame.
    if (busy && !(st.sweep >= 0 || st.glow > 0 || st.flash > 0 || st.sparks.length > 0)) st.drawn = ''
  },
})
`

// runtimeJS is the small library a scene is written against. It is documented
// for agents in skills/papaya-theme/SKILL.md; keep the two in step.
const runtimeJS = `(function () {
  'use strict';
  var THEME = window.__PAPAYA_THEME__ || { colors: {} };
  var canvas = document.getElementById('c');
  var def = null, dead = false, started = false, frames = 0, statsSent = false, t0 = 0, last = 0;
  var colors = {};
  for (var k in THEME.colors) colors[k] = THEME.colors[k];

  function post(m) { m.papaya = 'scene'; m.version = THEME.version; try { parent.postMessage(m, '*'); } catch (e) {} }
  function fail(err) {
    if (dead) return;
    dead = true;
    var msg = err && err.stack ? String(err.stack) : String(err);
    post({ type: 'error', message: msg.slice(0, 2000) });
  }
  window.addEventListener('error', function (e) {
    fail(e.error || (e.message + ' (line ' + e.lineno + ')'));
  });
  window.addEventListener('unhandledrejection', function (e) { fail(e.reason); });

  // Deterministic random numbers: s.rand(seed) returns a function giving 0..1.
  function rand(seed) {
    var a = (seed === undefined ? Math.random() * 4294967296 : seed) >>> 0;
    return function () {
      a = (a + 0x6D2B79F5) >>> 0;
      var t = Math.imul(a ^ (a >>> 15), 1 | a);
      t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
      return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
    };
  }
  // Smooth 2D value noise, 0..1.
  var perm = (function () { var r = rand(1337), p = []; for (var i = 0; i < 512; i++) p[i] = Math.floor(r() * 256); return p; })();
  function hash(x, y) { return perm[(perm[x & 255] + y) & 511] / 255; }
  function fade(t) { return t * t * (3 - 2 * t); }
  function noise(x, y) {
    var xi = Math.floor(x), yi = Math.floor(y), xf = x - xi, yf = y - yi;
    var a = hash(xi, yi), b = hash(xi + 1, yi), c = hash(xi, yi + 1), d = hash(xi + 1, yi + 1);
    var u = fade(xf), v = fade(yf);
    return a + (b - a) * u + (c - a) * v + (a - b - c + d) * u * v;
  }
  // A theme colour (or any CSS colour) with an alpha, as an rgba() string.
  var probe = document.createElement('canvas').getContext('2d');
  function color(name, alpha) {
    var c = colors[name] || name;
    probe.fillStyle = '#000'; probe.fillStyle = c;
    var v = probe.fillStyle;
    if (alpha === undefined) return v;
    if (v[0] === '#') {
      var n = parseInt(v.slice(1), 16);
      return 'rgba(' + (n >> 16 & 255) + ',' + (n >> 8 & 255) + ',' + (n & 255) + ',' + alpha + ')';
    }
    return v.replace(/rgba?\(([^)]+?)(,\s*[\d.]+)?\)/, 'rgba($1,' + alpha + ')');
  }

  var IMG = null;
  if (window.__PAPAYA_IMAGE__) { IMG = new Image(); IMG.src = window.__PAPAYA_IMAGE__; }
  var s = {
    image: IMG,
    canvas: canvas, ctx: null, w: 0, h: 0, dpr: 1, t: 0, dt: 0, frame: 0,
    theme: THEME.name, colors: colors, tabs: [], state: {},
    reducedMotion: false,
    rand: rand, noise: noise, color: color,
    lerp: function (a, b, t) { return a + (b - a) * t; },
    clamp: function (v, lo, hi) { return v < lo ? lo : v > hi ? hi : v; }
  };
  var rm = window.matchMedia ? matchMedia('(prefers-reduced-motion: reduce)') : null;
  s.reducedMotion = !!(rm && rm.matches);

  window.papaya = {
    scene: function (d) { def = d || {}; },
    version: 1
  };

  function call(fn, arg) {
    if (!def || typeof def[fn] !== 'function' || dead) return;
    try { def[fn](s, arg); } catch (e) { fail(e); }
  }

  function resize() {
    s.dpr = Math.min(window.devicePixelRatio || 1, 2);
    s.w = window.innerWidth; s.h = window.innerHeight;
    canvas.width = Math.max(1, Math.round(s.w * s.dpr));
    canvas.height = Math.max(1, Math.round(s.h * s.dpr));
    if (s.ctx && s.ctx.setTransform) s.ctx.setTransform(s.dpr, 0, 0, s.dpr, 0, 0);
    if (s.ctx && s.ctx.viewport) s.ctx.viewport(0, 0, canvas.width, canvas.height);
    if (started) call('resize');
  }

  function tick(now) {
    if (dead) return;
    requestAnimationFrame(tick);
    // With reduced motion asked for, a scene still draws, but twice a second.
    if (s.reducedMotion && now - last < 500 && s.frame > 0) return;
    s.dt = last ? Math.min(0.1, (now - last) / 1000) : 0;
    last = now;
    s.t += s.dt;
    call('draw');
    s.frame++; frames++;
    // A poster (the picker's preview) holds a frame once the scene has settled.
    if (THEME.poster && s.t > 1.2) { dead = true; return; }
    if (!statsSent && now - t0 > 3000) {
      statsSent = true;
      post({ type: 'running', fps: Math.round(frames * 1000 / (now - t0)) });
    }
  }

  window.addEventListener('message', function (e) {
    if (e.source !== parent) return;
    var m = e.data;
    if (!m || m.papaya !== 'host') return;
    if (m.type === 'init') {
      s.tabs = Array.isArray(m.tabs) ? m.tabs : [];
      call('init');
      return;
    }
    if (m.type === 'event' && m.event && typeof m.event.type === 'string') {
      var ev = m.event;
      if (ev.type === 'tab-open' && !s.tabs.some(function (x) { return x.id === ev.id; })) {
        s.tabs.push({ id: ev.id, name: ev.tab, host: ev.host });
      } else if (ev.type === 'tab-close') {
        s.tabs = s.tabs.filter(function (x) { return x.id !== ev.id; });
      } else if (ev.type === 'tab-rename') {
        s.tabs.forEach(function (x) { if (x.id === ev.id) x.name = ev.tab; });
      }
      call('on', ev);
    }
  });
  window.addEventListener('resize', resize);

  window.__papayaStart = function () {
    if (dead) return;
    // A wallpaper is decoded before the scene starts, so s.image is ready in setup.
    if (IMG && !IMG.complete) {
      var go = function () { window.__papayaStart(); };
      IMG.decode().then(go, function () { fail(new Error('the wallpaper image could not be decoded')); });
      return;
    }
    if (!def) { fail(new Error('scene.js never called papaya.scene({ ... })')); return; }
    var kind = def.context || '2d';
    try {
      s.ctx = canvas.getContext(kind, kind === '2d' ? { alpha: true } : { alpha: true, premultipliedAlpha: true, antialias: true });
    } catch (e) { fail(e); return; }
    if (!s.ctx) { fail(new Error('this browser gave no "' + kind + '" canvas context')); return; }
    resize();
    call('setup');
    started = true;
    call('resize');
    t0 = performance.now();
    requestAnimationFrame(tick);
    post({ type: 'ready' });
  };
})();
`
