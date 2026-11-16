package themes

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
)

// FromOmarchy builds a theme from an Omarchy theme's colors.toml.
//
// Omarchy (github.com/omacom/omarchy, MIT) ships a few dozen well-loved
// palettes in one small format: background and foreground shades, an accent,
// a selection colour, and the eight colours with bright variants. Papaya does
// not bundle them; papaya-theme fetches the file from Omarchy on request and
// this maps it onto Papaya's tokens.
func FromOmarchy(name, title string, toml []byte) (*Theme, error) {
	kv := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(toml))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = stripComment(strings.TrimSpace(v))
		kv[strings.TrimSpace(k)] = strings.Trim(v, `"'`)
	}
	need := []string{"background", "foreground", "accent", "red", "green", "yellow", "blue", "magenta", "cyan"}
	for _, k := range need {
		if kv[k] == "" {
			return nil, fmt.Errorf("colors.toml has no %q; is this an Omarchy theme?", k)
		}
	}
	pick := func(keys ...string) string {
		for _, k := range keys {
			if v := kv[k]; v != "" {
				return v
			}
		}
		return ""
	}
	light := kv["mode"] == "light"

	c := map[string]string{
		"surface-0":   kv["background"],
		"surface-1":   pick("dark_background", "background"),
		"surface-2":   pick("lighter_background", "background"),
		"surface-3":   pick("muted", "selection", "lighter_background"),
		"text-hi":     pick("bright_foreground", "foreground"),
		"text-lo":     pick("dark_foreground", "muted", "foreground"),
		"accent":      kv["accent"],
		"state-ok":    kv["green"],
		"state-warn":  kv["yellow"],
		"state-err":   kv["red"],
		"term-bg":     kv["background"],
		"term-fg":     kv["foreground"],
		"term-cursor": pick("cursor", "accent"),
		"term-sel":    pick("selection", "lighter_background"),

		"ansi-black":   pick("muted", "lighter_background"),
		"ansi-red":     kv["red"],
		"ansi-green":   kv["green"],
		"ansi-yellow":  kv["yellow"],
		"ansi-blue":    kv["blue"],
		"ansi-magenta": kv["magenta"],
		"ansi-cyan":    kv["cyan"],
		"ansi-white":   pick("light_foreground", "foreground"),

		"ansi-bright-black":   pick("dark_foreground", "muted"),
		"ansi-bright-red":     pick("bright_red", "red"),
		"ansi-bright-green":   pick("bright_green", "green"),
		"ansi-bright-yellow":  pick("bright_yellow", "yellow"),
		"ansi-bright-blue":    pick("bright_blue", "blue"),
		"ansi-bright-magenta": pick("bright_magenta", "magenta"),
		"ansi-bright-cyan":    pick("bright_cyan", "cyan"),
		"ansi-bright-white":   pick("bright_foreground", "foreground"),
	}
	if light {
		// On a light background, "white" text must still be readable: map the
		// white slots to the theme's own dark foregrounds, as light terminal
		// palettes do.
		c["ansi-white"] = pick("light_foreground", "foreground")
		c["ansi-bright-white"] = pick("bright_foreground", "foreground")
		c["ansi-black"] = pick("foreground")
	}
	for k, v := range c {
		if v == "" {
			delete(c, k) // keep Papaya's own colour for anything the palette lacks
		}
	}
	if title == "" {
		title = name
	}
	t := &Theme{
		Name:        name,
		Description: title + ", from Omarchy (github.com/omacom/omarchy, MIT)",
		Colors:      c,
	}
	if err := t.Validate(); err != nil {
		return nil, err
	}
	return t, nil
}

// stripComment drops a trailing # comment that is outside quotes.
func stripComment(v string) string {
	in := byte(0)
	for i := 0; i < len(v); i++ {
		switch c := v[i]; {
		case in != 0 && c == in:
			in = 0
		case in == 0 && (c == '"' || c == '\''):
			in = c
		case in == 0 && c == '#':
			return strings.TrimSpace(v[:i])
		}
	}
	return v
}
