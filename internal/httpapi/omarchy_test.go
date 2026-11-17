package httpapi_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/martoast/papaya/internal/httpapi"
	"github.com/martoast/papaya/test/fakeomarchy"
)

// Omarchy's themes from the app: the list carries each theme's colours for
// the previews, and one call imports colours and wallpaper and switches.
func TestOmarchyThemesFromTheApp(t *testing.T) {
	fake, raw, api := fakeomarchy.New()
	t.Cleanup(fake.Close)
	oldRaw, oldAPI := httpapi.OmarchyRaw, httpapi.OmarchyAPI
	httpapi.OmarchyRaw, httpapi.OmarchyAPI = raw, api
	t.Cleanup(func() { httpapi.OmarchyRaw, httpapi.OmarchyAPI = oldRaw, oldAPI })

	srv := newThemeServer(t)

	code, body, _ := do(t, "GET", srv+"/api/omarchy", "", "")
	require.Equal(t, 200, code, body)
	var list struct {
		Themes []struct {
			Name      string            `json:"name"`
			Title     string            `json:"title"`
			Colors    map[string]string `json:"colors"`
			Installed bool              `json:"installed"`
		} `json:"themes"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &list))
	require.Len(t, list.Themes, 2, "directories only, not README.md")
	require.Equal(t, "starship", list.Themes[0].Name)
	require.Equal(t, "Tokyo Night", list.Themes[1].Title)
	require.Equal(t, "#1a1b26", list.Themes[1].Colors["term-bg"])
	require.False(t, list.Themes[1].Installed)

	code, _, _ = do(t, "POST", srv+"/api/omarchy/../etc", "", "")
	require.NotEqual(t, 200, code)

	code, body, _ = do(t, "POST", srv+"/api/omarchy/tokyo-night", "", "")
	require.Equal(t, 200, code, body)
	code, body, _ = do(t, "GET", srv+"/api/themes", "", "")
	require.Equal(t, 200, code)
	require.Contains(t, body, `"active":"tokyo-night"`)
	require.Contains(t, body, `"hasImage":true`, "its wallpaper came along")

	code, body, _ = do(t, "GET", srv+"/api/omarchy", "", "")
	require.Equal(t, 200, code)
	require.Contains(t, body, `"name":"tokyo-night","title":"Tokyo Night"`)
	require.Contains(t, body, `"installed":true`)
}
