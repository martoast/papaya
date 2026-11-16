// Package config parses and validates papaya's environment configuration.
//
// Everything is env-driven so the binary is CI-friendly. Validation runs to
// completion before tsnet starts: a misconfigured node that has already joined
// the tailnet is worse than one that never starts.
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config is the fully validated runtime configuration.
type Config struct {
	AuthKey   string
	Hostname  string
	StateDir  string
	Ephemeral bool

	AllowedUsers map[string]bool
	AllowedTags  map[string]bool

	SSHUser   string
	Hosts     []string
	UploadDir string

	// HostSSHUsers maps a host from PAPAYA_HOSTS to the remote account to use
	// there, for entries written as "user@host". SSHUser covers the rest.
	//
	// One default account across every host only holds while the hosts are
	// alike. They are not: a Linux box reached through Tailscale SSH and a Mac
	// running its own sshd rarely share a username, and the mismatch is close to
	// invisible — creating a session works, because the picker sends a user
	// explicitly, while the resumable scan quietly fails against an account that
	// does not exist there and simply never offers that host's work back.
	HostSSHUsers map[string]string

	// Tmux runs each remote shell inside a named tmux session, so the work
	// survives papaya restarting, the machine sleeping, or the browser going
	// away for a day. Falls back to a login shell where tmux is absent.
	Tmux bool

	// AllowedSSHUsers, when non-empty, is the only set of remote accounts that
	// may be connected as. Empty means "any account except root".
	//
	// This is deliberately separate from the tailnet SSH policy. The policy is
	// the real boundary, but it lives somewhere else and is easy to widen by
	// accident; enforcing here means a future ACL edit cannot silently hand the
	// web terminal a root shell.
	AllowedSSHUsers map[string]bool

	// AllowedHosts, when non-empty, is the only set of hosts that may be
	// connected to. Empty means any host the tailnet permits.
	//
	// PAPAYA_HOSTS populates the picker; this restricts what the API accepts.
	// Without it a client can ask for any host on the tailnet regardless of what
	// the picker offers.
	AllowedHosts []string

	MaxUploadBytes int64
	MaxSessions    int

	// Voice transcription. Disabled entirely when the API key is empty; the
	// frontend hides the microphone rather than offering a button that fails.
	OpenAIAPIKey    string
	OpenAIBaseURL   string
	TranscribeModel string
	TranscribeLangs []string
	TranscribeWords []string
	MaxAudioBytes   int64

	// TLSCert and TLSKey serve a certificate you supply instead of a
	// Tailscale-issued one, which is what makes a custom domain possible:
	// Tailscale only issues for names inside your tailnet's ts.net domain.
	TLSCert string
	TLSKey  string

	// TLS serves on the tsnet TLS listener using a Tailscale-issued cert.
	// Browsers only grant microphone access in a secure context, so voice input
	// needs this.
	TLS bool

	// TLSAuto is set when PAPAYA_TLS was left unset. papaya then serves HTTPS
	// on its own as soon as the tailnet offers it a certificate, and plain HTTP
	// until then, so a first run needs no decision about it. An explicit
	// PAPAYA_TLS=true or false always wins.
	TLSAuto bool

	UseTailscaleSSH     bool
	KeyPath             string
	KnownHostsPath      string
	InsecureSkipHostKey bool

	DetachedTTL time.Duration
}

// Load reads configuration from the process environment.
func Load() (*Config, error) {
	return loadFrom(os.Getenv)
}

// legacyPrefix is the settings prefix from before the project was renamed.
// Existing installs keep working: a PAPAYA_ setting that is unset falls back to
// the same name with the old prefix.
const legacyPrefix = "TSTERM_"

// withLegacyNames wraps get so every PAPAYA_* lookup that comes back empty is
// retried under its pre-rename TSTERM_* name. The new name always wins.
func withLegacyNames(get func(string) string) func(string) string {
	return func(key string) string {
		v := get(key)
		if strings.TrimSpace(v) == "" && strings.HasPrefix(key, "PAPAYA_") {
			return get(legacyPrefix + strings.TrimPrefix(key, "PAPAYA_"))
		}
		return v
	}
}

// loadFrom is the testable core of Load.
func loadFrom(get func(string) string) (*Config, error) {
	get = withLegacyNames(get)
	c := &Config{
		AuthKey:        strings.TrimSpace(get("TS_AUTHKEY")),
		Hostname:       def(get("PAPAYA_HOSTNAME"), "papaya"),
		StateDir:       def(get("PAPAYA_STATE_DIR"), "/var/lib/papaya"),
		SSHUser:        strings.TrimSpace(get("PAPAYA_SSH_USER")),
		UploadDir:      def(get("PAPAYA_UPLOAD_DIR"), ".papaya-uploads"),
		KeyPath:        strings.TrimSpace(get("PAPAYA_KEY_PATH")),
		KnownHostsPath: def(get("PAPAYA_KNOWN_HOSTS"), "/etc/papaya/known_hosts"),
		AllowedUsers:   csvSet(get("PAPAYA_ALLOWED_USERS")),
		AllowedTags:    csvSet(get("PAPAYA_ALLOWED_TAGS")),
		Hosts:          nil, // set below: entries may carry a "user@" prefix

		AllowedSSHUsers: csvSet(get("PAPAYA_ALLOWED_SSH_USERS")),
		AllowedHosts:    csvList(get("PAPAYA_ALLOWED_HOSTS")),

		OpenAIAPIKey:    firstNonEmpty(get("PAPAYA_OPENAI_API_KEY"), get("OPENAI_API_KEY")),
		OpenAIBaseURL:   def(get("PAPAYA_OPENAI_BASE_URL"), "https://api.openai.com/v1"),
		TranscribeModel: def(get("PAPAYA_TRANSCRIBE_MODEL"), "gpt-transcribe"),
		TranscribeLangs: csvList(get("PAPAYA_TRANSCRIBE_LANGUAGES")),
		TranscribeWords: csvList(get("PAPAYA_TRANSCRIBE_KEYWORDS")),
		TLSCert:         strings.TrimSpace(get("PAPAYA_TLS_CERT")),
		TLSKey:          strings.TrimSpace(get("PAPAYA_TLS_KEY")),
	}

	c.Hosts, c.HostSSHUsers = splitHostUsers(csvList(get("PAPAYA_HOSTS")))

	var errs []error
	bind := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}

	// An ephemeral node deregisters whenever it stops. That suits a pre-made
	// auth key, which can simply enrol again; a node signed in through the
	// browser would make its owner sign in again on every restart, so it is
	// kept unless asked otherwise.
	var err error
	if c.Ephemeral, err = envBool(get, "PAPAYA_EPHEMERAL", c.AuthKey != ""); err != nil {
		bind(err)
	}
	if c.UseTailscaleSSH, err = envBool(get, "PAPAYA_USE_TAILSCALE_SSH", true); err != nil {
		bind(err)
	}
	if c.InsecureSkipHostKey, err = envBool(get, "PAPAYA_INSECURE_SKIP_HOST_KEY", false); err != nil {
		bind(err)
	}

	mb, err := envInt(get, "PAPAYA_MAX_UPLOAD_MB", 25)
	if err != nil {
		bind(err)
	} else if mb <= 0 {
		bind(fmt.Errorf("PAPAYA_MAX_UPLOAD_MB must be positive, got %d", mb))
	} else {
		c.MaxUploadBytes = int64(mb) << 20
	}

	if strings.TrimSpace(get("PAPAYA_TLS")) == "" {
		// Unset: decided at runtime from what the tailnet offers. A certificate
		// of your own is an explicit choice of HTTPS, though.
		c.TLSAuto = c.TLSCert == ""
		c.TLS = c.TLSCert != ""
	} else if c.TLS, err = envBool(get, "PAPAYA_TLS", false); err != nil {
		bind(err)
	}
	if c.Tmux, err = envBool(get, "PAPAYA_TMUX", false); err != nil {
		bind(err)
	}

	// OpenAI caps audio uploads at 25 MB, so a larger local limit would only
	// buy a slower failure.
	audioMB, err := envInt(get, "PAPAYA_MAX_AUDIO_MB", 25)
	if err != nil {
		bind(err)
	} else if audioMB <= 0 || audioMB > 25 {
		bind(fmt.Errorf("PAPAYA_MAX_AUDIO_MB must be between 1 and 25, got %d", audioMB))
	} else {
		c.MaxAudioBytes = int64(audioMB) << 20
	}

	if c.MaxSessions, err = envInt(get, "PAPAYA_MAX_SESSIONS", 10); err != nil {
		bind(err)
	} else if c.MaxSessions <= 0 {
		bind(fmt.Errorf("PAPAYA_MAX_SESSIONS must be positive, got %d", c.MaxSessions))
	}

	if c.DetachedTTL, err = envDur(get, "PAPAYA_DETACHED_TTL", 30*time.Minute); err != nil {
		bind(err)
	} else if c.DetachedTTL <= 0 {
		bind(errors.New("PAPAYA_DETACHED_TTL must be positive"))
	}

	// No TS_AUTHKEY is not an error any more: papaya prints a "Sign in with
	// Tailscale" link instead, which is the zero-configuration path.
	if c.Hostname == "" {
		bind(errors.New("PAPAYA_HOSTNAME must not be empty"))
	}

	// Never default to permissive. An empty access list does not mean
	// everyone: it means only the person who signed papaya in to the tailnet
	// (see OwnerOnly). A tagged node has no such person, and main refuses to
	// start one with no list rather than guess.

	for tag := range c.AllowedTags {
		if !strings.HasPrefix(tag, "tag:") {
			bind(fmt.Errorf("PAPAYA_ALLOWED_TAGS entry %q must start with %q", tag, "tag:"))
		}
	}

	// There is no built-in remote account: guessing one would log in as the
	// wrong person on someone else's machine. Every host needs an account,
	// either the default or its own "user@host" entry.
	if c.SSHUser == "" {
		var missing []string
		for _, h := range c.Hosts {
			if c.hostUser(h) == "" {
				missing = append(missing, h)
			}
		}
		// With no hosts listed, accounts are chosen per machine in the setup
		// screen. A host listed here without one is a half-finished config.
		switch {
		case len(missing) > 0:
			bind(fmt.Errorf("no remote account for %s: set PAPAYA_SSH_USER, or write each host as "+
				"user@host in PAPAYA_HOSTS", strings.Join(missing, ", ")))
		}
	} else if !c.SSHUserAllowed(c.SSHUser) {
		// The default remote user must itself be permitted, or every session
		// created without an explicit user would be rejected at request time.
		bind(fmt.Errorf("PAPAYA_SSH_USER %q is not permitted by PAPAYA_ALLOWED_SSH_USERS", c.SSHUser))
	}

	// Same for a per-host account. Left to fail at request time this reads as
	// the host being down rather than as a configuration error.
	for host, user := range c.HostSSHUsers {
		if !c.SSHUserAllowed(user) {
			bind(fmt.Errorf("PAPAYA_HOSTS entry %q@%q is not permitted by PAPAYA_ALLOWED_SSH_USERS",
				user, host))
		}
	}

	// Both halves or neither: one alone is a typo that would silently fall back
	// to the Tailscale certificate and serve the wrong name.
	if (c.TLSCert == "") != (c.TLSKey == "") {
		bind(errors.New("PAPAYA_TLS_CERT and PAPAYA_TLS_KEY must be set together"))
	}
	for _, p := range []string{c.TLSCert, c.TLSKey} {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err != nil {
			bind(fmt.Errorf("TLS material %q is not readable: %w", p, err))
		}
	}
	if c.TLSCert != "" && !c.TLS {
		bind(errors.New("PAPAYA_TLS_CERT is set but PAPAYA_TLS is false; nothing would serve it"))
	}
	if c.TLSAuto && c.TLSCert != "" {
		c.TLSAuto = false
	}

	if c.KeyPath != "" {
		if _, err := os.Stat(c.KeyPath); err != nil {
			bind(fmt.Errorf("PAPAYA_KEY_PATH %q is not readable: %w", c.KeyPath, err))
		}
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return c, nil
}

// def returns v trimmed, or fallback when v is empty.
func def(v, fallback string) string {
	if v = strings.TrimSpace(v); v != "" {
		return v
	}
	return fallback
}

func csvList(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// splitHostUsers separates PAPAYA_HOSTS entries of the form "user@host" into
// the bare host list the picker shows and a host-to-account map.
//
// The syntax is ssh's own, so it needs no explaining, and it stays optional: a
// plain "host" entry falls back to PAPAYA_SSH_USER, which is what every
// existing deployment already has.
//
// Split on the LAST "@": a username cannot contain one, but this leaves the
// door open for a host spelling that does.
func splitHostUsers(entries []string) ([]string, map[string]string) {
	if len(entries) == 0 {
		return nil, nil
	}

	hosts := make([]string, 0, len(entries))
	users := make(map[string]string)

	for _, e := range entries {
		host, user := e, ""
		if i := strings.LastIndex(e, "@"); i >= 0 {
			user = strings.TrimSpace(e[:i])
			host = strings.TrimSpace(e[i+1:])
		}
		if host == "" {
			// "user@" with nothing after it: no host to offer, and guessing
			// which one was meant would be worse than dropping it.
			continue
		}
		hosts = append(hosts, host)
		if user != "" {
			users[strings.ToLower(host)] = user
		}
	}

	if len(users) == 0 {
		users = nil
	}
	return hosts, users
}

func csvSet(v string) map[string]bool {
	items := csvList(v)
	if len(items) == 0 {
		return map[string]bool{}
	}
	set := make(map[string]bool, len(items))
	for _, it := range items {
		set[it] = true
	}
	return set
}

func envBool(get func(string) string, key string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(get(key))
	if raw == "" {
		return fallback, nil
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback, fmt.Errorf("%s: %q is not a boolean", key, raw)
	}
	return b, nil
}

func envInt(get func(string) string, key string, fallback int) (int, error) {
	raw := strings.TrimSpace(get(key))
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback, fmt.Errorf("%s: %q is not an integer", key, raw)
	}
	return n, nil
}

func envDur(get func(string) string, key string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(get(key))
	if raw == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return fallback, fmt.Errorf("%s: %q is not a duration (try %q)", key, raw, "30m")
	}
	return d, nil
}

// firstNonEmpty returns the first argument that is not blank.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// OwnerOnly reports whether no access list was configured, in which case only
// the tailnet user who owns the papaya node may use it.
func (c *Config) OwnerOnly() bool {
	return len(c.AllowedUsers) == 0 && len(c.AllowedTags) == 0
}

// SettingsPath is where choices made in the setup screen are kept.
func (c *Config) SettingsPath() string { return filepath.Join(c.StateDir, "settings.json") }

// TrustedHostsPath is the known_hosts file papaya maintains itself: host keys
// confirmed in the setup screen land here. PAPAYA_KNOWN_HOSTS, if present, is
// read as well and is never written.
func (c *Config) TrustedHostsPath() string { return filepath.Join(c.StateDir, "known_hosts") }

// GeneratedKeyPath is the SSH key papaya makes for itself, used for machines
// that do not run Tailscale SSH. PAPAYA_KEY_PATH replaces it when set.
func (c *Config) GeneratedKeyPath() string { return filepath.Join(c.StateDir, "papaya_ed25519") }

// TranscriptionEnabled reports whether voice input is configured.
func (c *Config) TranscriptionEnabled() bool { return c.OpenAIAPIKey != "" }

// SSHUserAllowed reports whether a remote account may be connected as.
//
// root is refused unless it is named explicitly. A web terminal that silently
// grants uid 0 because the tailnet policy happened to permit it is exactly the
// kind of boundary that widens without anyone noticing.
func (c *Config) SSHUserAllowed(user string) bool {
	user = strings.TrimSpace(user)
	if user == "" {
		return false
	}

	if len(c.AllowedSSHUsers) > 0 {
		return c.AllowedSSHUsers[user]
	}
	return !isRootAccount(user)
}

// UserForHost is the remote account to use on host: the one given as
// "user@host" in PAPAYA_HOSTS, or PAPAYA_SSH_USER where none was.
//
// Matching is by first DNS label, so a host configured as a short name still
// resolves when the caller names it by FQDN, and the other way round.
func (c *Config) UserForHost(host string) string {
	if u := c.hostUser(host); u != "" {
		return u
	}
	return c.SSHUser
}

// ExplicitUserForHost is the account given for host as "user@host" in
// PAPAYA_HOSTS, or "" — without falling back to PAPAYA_SSH_USER, so an account
// chosen in the setup screen can sit between the two.
func (c *Config) ExplicitUserForHost(host string) string { return c.hostUser(host) }

func (c *Config) hostUser(host string) string {
	if len(c.HostSSHUsers) == 0 {
		return ""
	}

	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if u, ok := c.HostSSHUsers[host]; ok {
		return u
	}

	label, _, _ := strings.Cut(host, ".")
	for h, u := range c.HostSSHUsers {
		if hl, _, _ := strings.Cut(strings.TrimSuffix(h, "."), "."); hl == label {
			return u
		}
	}
	return ""
}

func isRootAccount(user string) bool {
	switch strings.ToLower(user) {
	case "root", "toor", "admin":
		return true
	}
	return false
}

// HostAllowed reports whether a host may be connected to.
//
// Matching accepts any spelling of the same machine: an entry may be a short
// MagicDNS name, an FQDN, or an address, and a request matches if it is equal to
// an entry or shares its first DNS label. Addresses must be listed literally,
// since they cannot be resolved without a lookup that would itself be a
// side channel.
func (c *Config) HostAllowed(host string) bool {
	if len(c.AllowedHosts) == 0 {
		return true
	}

	want := normalizeHost(host)
	if want == "" {
		return false
	}

	for _, entry := range c.AllowedHosts {
		got := normalizeHost(entry)
		if want == got || firstLabel(want) == firstLabel(got) {
			return true
		}
	}
	return false
}

func normalizeHost(h string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(h), "."))
}

// firstLabel returns the leading DNS label, or the whole string for an address.
func firstLabel(h string) string {
	if net.ParseIP(h) != nil {
		return h
	}
	if i := strings.Index(h, "."); i > 0 {
		return h[:i]
	}
	return h
}
