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
func SceneDocument(t *Theme, nonce string) []byte {
	head := sceneHead(t, nonce)
	var b strings.Builder
	b.WriteString(head)
	b.WriteString(t.Scene)
	b.WriteString("\n</script>\n<script nonce=\"" + nonce + "\">window.__papayaStart()</script></body></html>")
	return []byte(b.String())
}

// sceneHead is everything in the document before the scene's first line.
func sceneHead(t *Theme, nonce string) string {
	meta, _ := json.Marshal(map[string]any{
		"name":    t.Name,
		"version": t.Version(),
		"colors":  t.Colors,
	})
	var b strings.Builder
	b.WriteString(`<!doctype html><html><head><meta charset="utf-8">` +
		`<style>html,body{margin:0;height:100%;overflow:hidden;background:transparent}` +
		`canvas{position:fixed;inset:0;width:100%;height:100%;display:block}</style></head>` +
		`<body><canvas id="c"></canvas>`)
	b.WriteString(`<script nonce="` + nonce + `">window.__PAPAYA_THEME__=`)
	b.Write(meta)
	b.WriteString(";\n")
	b.WriteString(runtimeJS)
	b.WriteString("</script>\n<script nonce=\"" + nonce + "\">\n")
	return b.String()
}

var stackLineRE = regexp.MustCompile(`/theme/scene/[^\s:)]+:(\d+):(\d+)`)

// SceneError rewrites a browser's error message so line numbers point into
// scene.js rather than into the frame document, which the agent never sees.
func SceneError(t *Theme, msg string) string {
	offset := strings.Count(sceneHead(t, "x"), "\n")
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

  var s = {
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
