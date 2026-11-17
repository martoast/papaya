#!/bin/sh
# Papaya installer.
#
#   curl -fsSL https://papaya.fullstacklabs.org/install.sh | sh
#
# On the always-on machine that runs Papaya: starts it (in Docker when Docker
# is available, otherwise as a background service), prints the Tailscale
# sign-in link, waits for you to approve it, and prints the address to open.
#
# On the other machines where your agents run, install only the agent tools
# (papaya-say, papaya-theme, papaya-status and their skills):
#
#   curl -fsSL https://papaya.fullstacklabs.org/install.sh | sh -s -- --tools --url https://papaya.<tailnet>.ts.net
#
# Options: --tools  agent tools only     --url URL  this Papaya's address
#          --native skip Docker          --version vX.Y.Z  a specific release
#
# Re-running it upgrades in place; sessions, settings and themes are kept.
set -eu

REPO=martoast/papaya
BIN_DIR=${PAPAYA_BIN_DIR:-$HOME/.local/bin}
CONF_DIR=${XDG_CONFIG_HOME:-$HOME/.config}/papaya
MODE=full NATIVE=0 URL="" TAG=""

if [ -t 1 ]; then B=$(printf '\033[1m'); D=$(printf '\033[2m'); O=$(printf '\033[38;5;209m'); G=$(printf '\033[32m'); R=$(printf '\033[0m'); else B="" D="" O="" G="" R=""; fi
step() { printf '%s›%s %s\n' "$O" "$R" "$*"; }
ok() { printf '%s✓%s %s\n' "$G" "$R" "$*"; }
note() { printf '  %s%s%s\n' "$D" "$*" "$R"; }
die() { printf '%spapaya install: %s%s\n' "$(if [ -t 2 ]; then printf '\033[31m'; fi)" "$*" "$(if [ -t 2 ]; then printf '\033[0m'; fi)" >&2; exit 1; }

while [ $# -gt 0 ]; do
	case $1 in
		--tools) MODE=tools ;;
		--native) NATIVE=1 ;;
		--url) URL=${2:?--url needs an address}; shift ;;
		--version) TAG=${2:?--version needs a tag}; shift ;;
		-h|--help)
			cat <<'USAGE'
Papaya installer
  curl -fsSL https://papaya.fullstacklabs.org/install.sh | sh                 the Papaya server
  curl -fsSL https://papaya.fullstacklabs.org/install.sh | sh -s -- --tools --url URL   agent machines
Options: --tools  --url URL  --native (skip Docker)  --version vX.Y.Z
USAGE
			exit 0 ;;
		*) die "unknown option $1 (see --help)" ;;
	esac
	shift
done

command -v curl >/dev/null 2>&1 || die "needs curl"
command -v tar >/dev/null 2>&1 || die "needs tar"

case $(uname -s) in
	Linux) OS=linux ;;
	Darwin) OS=darwin ;;
	*) die "Papaya runs on Linux and macOS; this is $(uname -s)" ;;
esac
case $(uname -m) in
	x86_64|amd64) ARCH=amd64 ;;
	arm64|aarch64) ARCH=arm64 ;;
	*) die "no Papaya build for $(uname -m)" ;;
esac

printf '\n  %sPapaya%s  %severy agent, every machine, one browser tab%s\n\n' "$B" "$R" "$D" "$R"

if [ -z "$TAG" ]; then
	TAG=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)
	[ -n "$TAG" ] || die "could not find the latest release (is GitHub reachable?)"
fi
VER=${TAG#v}
ASSET="papaya_${VER}_${OS}_${ARCH}.tar.gz"

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT INT TERM

step "Downloading Papaya $VER for $OS/$ARCH"
curl -fsSL -o "$TMP/$ASSET" "https://github.com/$REPO/releases/download/$TAG/$ASSET" || die "download failed: $ASSET"
curl -fsSL -o "$TMP/checksums.txt" "https://github.com/$REPO/releases/download/$TAG/checksums.txt" || die "download failed: checksums.txt"
WANT=$(grep " $ASSET\$" "$TMP/checksums.txt" | cut -d' ' -f1)
if command -v sha256sum >/dev/null 2>&1; then GOT=$(sha256sum "$TMP/$ASSET" | cut -d' ' -f1); else GOT=$(shasum -a 256 "$TMP/$ASSET" | cut -d' ' -f1); fi
if [ -z "$WANT" ] || [ "$WANT" != "$GOT" ]; then die "checksum mismatch for $ASSET; not installing"; fi
mkdir -p "$TMP/x" && tar -xzf "$TMP/$ASSET" -C "$TMP/x"
ok "Verified $ASSET"

# The agent tools and skills, on every machine.
mkdir -p "$BIN_DIR"
for t in papaya-say papaya-theme papaya-status; do
	if [ -f "$TMP/x/bin/$t" ]; then cp "$TMP/x/bin/$t" "$BIN_DIR/$t" && chmod 755 "$BIN_DIR/$t"; fi
done
if [ -d "$TMP/x/skills" ]; then
	mkdir -p "$HOME/.claude/skills"
	cp -R "$TMP/x/skills/." "$HOME/.claude/skills/"
fi
ok "Agent tools in $BIN_DIR, skills in ~/.claude/skills"
case ":$PATH:" in *":$BIN_DIR:"*) ;; *) note "Add $BIN_DIR to your PATH:  export PATH=\"$BIN_DIR:\$PATH\"" ;; esac

remember_url() {
	mkdir -p "$CONF_DIR" && printf '%s\n' "$1" >"$CONF_DIR/url"
}

if [ "$MODE" = tools ]; then
	if [ -n "$URL" ]; then remember_url "$URL"; ok "Tools will talk to $URL"
	else note "Tell the tools where Papaya is:  --url https://papaya.<tailnet>.ts.net  (or set PAPAYA_URL)"; fi
	printf '\n  Status on tabs, exactly: run %spapaya-status --setup%s for the Claude Code / Codex hooks.\n\n' "$B" "$R"
	exit 0
fi

# ---- the Papaya server itself ----
logs=""
if [ "$NATIVE" = 0 ] && command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
	step "Starting Papaya in Docker"
	IMG="ghcr.io/$REPO:$VER"
	docker pull -q "$IMG" >/dev/null
	docker rm -f papaya >/dev/null 2>&1 || true # an upgrade: the data volume stays
	docker run -d --name papaya --restart unless-stopped \
		-e PAPAYA_TMUX=true -v papaya-data:/var/lib/papaya "$IMG" >/dev/null
	logs="docker logs papaya"
	ok "Running in Docker (container papaya, data in volume papaya-data)"
else
	step "Installing Papaya as a background service"
	cp "$TMP/x/papaya" "$BIN_DIR/papaya" && chmod 755 "$BIN_DIR/papaya"
	if [ "$OS" = darwin ]; then
		STATE="$HOME/Library/Application Support/papaya"
		LOG="$HOME/Library/Logs/papaya.log"
		PLIST="$HOME/Library/LaunchAgents/io.papaya.plist"
		mkdir -p "$STATE" "$(dirname "$PLIST")" "$(dirname "$LOG")"
		cat >"$PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>io.papaya</string>
  <key>ProgramArguments</key><array><string>$BIN_DIR/papaya</string></array>
  <key>EnvironmentVariables</key><dict>
    <key>PAPAYA_STATE_DIR</key><string>$STATE</string>
    <key>PAPAYA_TMUX</key><string>true</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
  <key>StandardOutPath</key><string>$LOG</string>
  <key>StandardErrorPath</key><string>$LOG</string>
</dict></plist>
EOF
		: >"$LOG"
		launchctl unload "$PLIST" 2>/dev/null || true
		launchctl load "$PLIST"
		logs="cat \"$LOG\""
		ok "Running via launchd (io.papaya); logs in $LOG"
	elif command -v systemctl >/dev/null 2>&1 && systemctl --user show-environment >/dev/null 2>&1; then
		UNIT="$HOME/.config/systemd/user/papaya.service"
		mkdir -p "$(dirname "$UNIT")" "$HOME/.local/share/papaya"
		cat >"$UNIT" <<EOF
[Unit]
Description=Papaya: every agent, every machine, one browser tab
After=network-online.target

[Service]
Environment=PAPAYA_STATE_DIR=%h/.local/share/papaya
Environment=PAPAYA_TMUX=true
ExecStart=$BIN_DIR/papaya
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
EOF
		systemctl --user daemon-reload
		systemctl --user enable papaya >/dev/null 2>&1
		systemctl --user restart papaya
		logs="journalctl --user -u papaya --no-pager -o cat --since=-10min"
		ok "Running as a systemd user service (papaya)"
		# Without lingering, a user service stops at logout.
		if command -v loginctl >/dev/null 2>&1 && ! loginctl show-user "$(id -un)" -p Linger 2>/dev/null | grep -q yes; then
			loginctl enable-linger "$(id -un)" 2>/dev/null ||
				note "To keep Papaya running after you log out:  sudo loginctl enable-linger $(id -un)"
		fi
	else
		note "No Docker and no service manager found. Start it yourself:"
		note "  PAPAYA_TMUX=true PAPAYA_STATE_DIR=~/.local/share/papaya $BIN_DIR/papaya"
		exit 0
	fi
fi

# ---- sign in, then the address ----
address() { sh -c "$logs" 2>&1 | grep -A3 'Papaya is running' | grep -o 'https\{0,1\}://[^ │]*' | tail -1; }
signin() { sh -c "$logs" 2>&1 | grep -o 'https://login.tailscale.com/a/[A-Za-z0-9]*' | tail -1; }

step "Waiting for Papaya to join your tailnet"
shown="" i=0
while [ $i -lt 600 ]; do
	a=$(address || true)
	if [ -n "$a" ]; then
		remember_url "$a"
		printf '\n  %sPapaya is ready.%s Now open it on your phone:\n\n' "$B" "$R"
		printf '    1. Install Tailscale   iPhone   %shttps://apps.apple.com/app/tailscale/id1470499037%s\n' "$O" "$R"
		printf '                           Android  %shttps://play.google.com/store/apps/details?id=com.tailscale.ipn%s\n' "$O" "$R"
		printf '    2. Sign in with the same account you just used\n'
		printf '    3. Open  %s%s%s\n\n' "$O" "$a" "$R"
		note "The setup screen there walks you through the rest. Computers you want"
		note "to reach need Tailscale too:  https://tailscale.com/download"
		note "Nothing goes through us: Papaya runs here, on your own private network."
		note "Agent tools on other machines:"
		note "  curl -fsSL https://papaya.fullstacklabs.org/install.sh | sh -s -- --tools --url $a"
		printf '\n'
		exit 0
	fi
	l=$(signin || true)
	if [ -n "$l" ] && [ "$l" != "$shown" ]; then
		shown=$l
		printf '\n  %sSign in with Tailscale%s to put Papaya on your private network:\n\n      %s%s%s\n\n' "$B" "$R" "$O" "$l" "$R"
		note "Tailscale privately connects your own devices to each other. It's free."
		note "No account yet? That link creates one (Google, Apple, GitHub or Microsoft)."
		note "Open it on any device, your phone is fine. This waits for you."
		printf '\n'
	fi
	i=$((i + 1))
	sleep 1
done
note "Still waiting after 10 minutes. Check the log:  $logs"
