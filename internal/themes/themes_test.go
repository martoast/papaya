package themes

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuiltinsLoadAndValidate(t *testing.T) {
	s, err := Open(t.TempDir())
	require.NoError(t, err)
	list, err := s.List()
	require.NoError(t, err)
	names := map[string]bool{}
	for _, th := range list {
		names[th.Name] = true
		require.True(t, th.Builtin)
		require.True(t, th.HasScene, th.Name)
	}
	for _, want := range []string{"orchard", "night-sky", "synthwave"} {
		require.True(t, names[want], "built-in %s missing", want)
	}
}

// Colours are applied to Papaya's own page as CSS custom properties, so a
// value must never be able to close the declaration or pull in a URL.
func TestColoursThatCouldEscapeAreRefused(t *testing.T) {
	for _, bad := range []string{
		"red; background: url(https://evil/x)",
		"url(https://evil/x)",
		"#fff}body{display:none",
		"rgb(1,2,3);",
		"var(--x)",
		"expression(alert(1))",
		"red",
	} {
		th := &Theme{Name: "x", Colors: map[string]string{"accent": bad}}
		require.Error(t, th.Validate(), bad)
	}
	for _, good := range []string{"#fff", "#ff8a3d", "#ff8a3d80", "rgb(255, 138, 61)", "rgba(0,0,0,.5)", "hsl(20 100% 60%)", "oklch(0.7 0.15 50)"} {
		th := &Theme{Name: "x", Colors: map[string]string{"accent": good}}
		require.NoError(t, th.Validate(), good)
	}
}

func TestOnlyKnownTokensMayBeSet(t *testing.T) {
	th := &Theme{Name: "x", Colors: map[string]string{"font-family": "#fff"}}
	err := th.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "not a theme colour")
}

// The scene is inlined into a <script> element; anything that would end it is
// refused, with the line number so an agent can fix it.
func TestSceneThatWouldBreakOutOfItsScriptIsRefused(t *testing.T) {
	for _, bad := range []string{"const a = '</script>'", "x\ny // <!-- hi", "'<SCRIPT src=x>'"} {
		th := &Theme{Name: "x", Scene: bad}
		require.Error(t, th.Validate(), bad)
	}
	th := &Theme{Name: "x", Scene: "line1\nconst a = '</scr' + 'ipt>'\nline3 </script"}
	err := th.Validate()
	require.ErrorContains(t, err, "line 3")
}

func TestNamesAndLimits(t *testing.T) {
	for _, bad := range []string{"", "Koi", "koi pond", "../x", "default", strings.Repeat("a", 41)} {
		th := &Theme{Name: bad}
		require.Error(t, th.Validate(), bad)
	}
	th := &Theme{Name: "x", Scene: strings.Repeat("a", MaxSceneBytes+1)}
	require.Error(t, th.Validate())
	th = &Theme{Name: "x", Opacity: 0.1}
	require.Error(t, th.Validate())
}

func TestPutGetActiveDelete(t *testing.T) {
	s, err := Open(t.TempDir())
	require.NoError(t, err)

	require.NoError(t, s.Put(&Theme{Name: "koi", Colors: map[string]string{"accent": "#ff8a3d"}, Scene: "papaya.scene({})"}))
	got, err := s.Get("koi")
	require.NoError(t, err)
	require.Equal(t, "#ff8a3d", got.Colors["accent"])

	require.Nil(t, s.Active("alice@example.com"))
	require.NoError(t, s.SetActive("alice@example.com", "koi"))
	require.Equal(t, "koi", s.Active("alice@example.com").Name)
	require.Nil(t, s.Active("bob@example.com"), "a choice is per person")

	require.Error(t, s.SetActive("alice@example.com", "nope"))

	require.NoError(t, s.Delete("koi"))
	require.Nil(t, s.Active("alice@example.com"), "deleting the theme someone has on puts them back on the default")
}

func TestBuiltinsAreReadOnly(t *testing.T) {
	s, err := Open(t.TempDir())
	require.NoError(t, err)
	require.ErrorContains(t, s.Put(&Theme{Name: "orchard"}), "built-in")
	require.Error(t, s.Delete("orchard"))
	require.NoError(t, s.SetActive("alice@example.com", "orchard"))
}

// Browsers report errors with line numbers in the frame document; the agent
// only knows its scene.js.
func TestSceneErrorPointsIntoSceneJS(t *testing.T) {
	th := &Theme{Name: "koi", Scene: "papaya.scene({\n  draw(s) {\n    boom()\n  }\n})"}
	head := strings.Count(sceneHead(th, "x"), "\n")
	msg := "ReferenceError: boom is not defined\n    at Object.draw (https://papaya.ts.net/theme/scene/koi?v=abc:" +
		strconv.Itoa(head+3) + ":5)\n    at call (https://papaya.ts.net/theme/scene/koi?v=abc:40:12)"
	out := SceneError(th, msg)
	require.Contains(t, out, "scene.js:3:5")
	require.Contains(t, out, "papaya-runtime:40:12")
}

func TestSceneDocumentIsSelfContained(t *testing.T) {
	th := &Theme{Name: "koi", Colors: map[string]string{"accent": "#ff8a3d"}, Scene: "papaya.scene({})"}
	doc := string(SceneDocument(th, "NONCE"))
	require.Equal(t, 3, strings.Count(doc, `<script nonce="NONCE">`))
	require.NotContains(t, doc, "<script>")
	require.Contains(t, doc, `"accent":"#ff8a3d"`)
	csp := SceneCSP("NONCE")
	for _, want := range []string{"default-src 'none'", "connect-src 'none'", "script-src 'nonce-NONCE'", "frame-ancestors 'self'"} {
		require.Contains(t, csp, want)
	}
}
