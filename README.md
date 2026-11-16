# Papaya

**Your shells and coding agents on all your machines, from any browser on your
tailnet — with nothing exposed to the internet.**

One papaya, many seeds: every machine you own and every agent running on it,
gathered in one place.

Papaya is a single Go binary that joins your [Tailscale](https://tailscale.com)
network in-process, serves a web terminal to devices on that network only, and
SSHes out to your other machines. Start a coding agent on your desktop, close
the laptop, and pick up the same session from your phone on the train.

No open port. No password. No `tailscaled`, TUN device, `NET_ADMIN` or
privileged container. The tailnet *is* the login.

```
Browser ──HTTPS over WireGuard──┐
                                │
                  ┌─────────────▼─────────┐
                  │  papaya (one binary)  │
                  │   tsnet.Server        │
                  │    ├── Listen :443    │  tailnet only, never 0.0.0.0
                  │    └── Dial   :22     │
                  └─────────────┬─────────┘
                                │ WireGuard
                 ┌──────────────▼─┐  ┌────────────┐  ┌────────────┐
                 │   devbox-1     │  │  laptop    │  │  server    │
                 └────────────────┘  └────────────┘  └────────────┘
```

## What it does

- **Sessions that outlive everything.** With tmux on the far side, work survives
  Papaya restarting, your laptop sleeping and your Wi-Fi dropping. Reopen the
  page and every session is where you left it.
- **Every device sees the same sessions.** Open something on the laptop and it
  appears on your phone, live. Two devices on one session share it politely.
- **Grid view.** Every session on screen at once, as resizable panels — a
  control room for a fleet of agents.
- **Drop an image into a session.** It is uploaded to that machine and its path
  typed at the cursor, ready for the agent to read.
- **Voice input** (optional, bring your own OpenAI key). Hold ⌃Space, talk, and
  the transcript lands at the cursor, tuned for shell vocabulary.
- **Agents can message each other** across machines with `papaya-say`.
- **Know which session wants you.** A terminal bell becomes a badge on its tab.
- **Installs as an app** on your phone's home screen.

## Quick start

You need: a Tailscale network, an always-on machine to run Papaya, and Tailscale
SSH (`tailscale up --ssh`) on the machines you want to reach.

**Easiest:** point your coding agent at [AGENTS.md](AGENTS.md). It is a
step-by-step setup guide written for an agent, including every trap we hit.

**By hand, with Docker:**

```bash
git clone https://github.com/martoast/papaya && cd papaya
cp .env.example .env && chmod 600 .env
# Fill in: TS_AUTHKEY, PAPAYA_ALLOWED_USERS (your tailnet login),
#          PAPAYA_SSH_USER (the account to log in as), PAPAYA_HOSTS.
ssh-keyscan devbox-1 laptop > deploy/known_hosts     # pin the hosts you will reach
docker compose up -d
```

Then open `https://papaya.<your-tailnet>.ts.net` from any device on your
tailnet. The first start takes up to a minute while Tailscale issues the HTTPS
certificate.

**Or the binary:** download a release for Linux or macOS from the
[releases page](https://github.com/martoast/papaya/releases), put the same
settings in your environment, and run `papaya`. `make service-install` sets it
up as a launchd service on macOS; `deploy/papaya.service` is a systemd unit.

Use a **reusable, non-ephemeral** auth key and `PAPAYA_EPHEMERAL=false`, so the
node keeps one stable address across restarts (see `.env.example` for why).

## Configuration

All configuration is environment-driven and validated before the node starts.

| Var | Required | Default | Notes |
|---|---|---|---|
| `TS_AUTHKEY` | yes | — | Ephemeral + tagged + pre-authorized |
| `PAPAYA_ALLOWED_USERS` | yes¹ | — | Comma list of tailnet login names |
| `PAPAYA_ALLOWED_TAGS` | yes¹ | — | Comma list, e.g. `tag:ci` |
| `PAPAYA_HOSTNAME` | no | `papaya` | Tailnet node name |
| `PAPAYA_STATE_DIR` | no | `/var/lib/papaya` | Created if absent |
| `PAPAYA_EPHEMERAL` | no | `true` | Node self-deletes on clean exit |
| `PAPAYA_SSH_USER` | yes² | — | Remote account to log in as. No built-in default |
| `PAPAYA_HOSTS` | no | — | Comma list for the picker (suggestion only) |
| `PAPAYA_ALLOWED_HOSTS` | no | — | **Enforced** allow list of hosts. Empty = any host the ACL permits |
| `PAPAYA_ALLOWED_SSH_USERS` | no | — | **Enforced** allow list of remote accounts. Empty = any except root |
| `PAPAYA_TMUX` | no | `false` | Run remote shells in tmux so work survives restarts |
| `PAPAYA_TLS` | no | `false` | Serve HTTPS via a Tailscale cert. Required for voice |
| `PAPAYA_TLS_CERT` | no | — | Your own certificate, for a custom domain |
| `PAPAYA_TLS_KEY` | no | — | Its key. Both or neither |
| `PAPAYA_UPLOAD_DIR` | no | `.papaya-uploads` | Relative to the SSH login dir |
| `PAPAYA_MAX_UPLOAD_MB` | no | `25` | |
| `PAPAYA_MAX_SESSIONS` | no | `10` | Per owner |
| `PAPAYA_USE_TAILSCALE_SSH` | no | `true` | Try tailnet-identity auth first |
| `PAPAYA_KEY_PATH` | no | — | Fallback private key |
| `PAPAYA_KNOWN_HOSTS` | no | `/etc/papaya/known_hosts` | |
| `PAPAYA_INSECURE_SKIP_HOST_KEY` | no | `false` | Dev only; warns per connection |
| `PAPAYA_DETACHED_TTL` | no | `30m` | Reaper for client-less sessions |

¹ At least one of `PAPAYA_ALLOWED_USERS` or `PAPAYA_ALLOWED_TAGS` must be set.
With neither, the process exits 1 rather than defaulting to permissive.

² Required unless every host in `PAPAYA_HOSTS` is written as `user@host`.

Settings from before the rename (`TSTERM_*`) are still read when the matching
`PAPAYA_*` one is unset.

## Tailnet setup

On each target host: `tailscale up --ssh`.

### Trying it out (untagged)

The quickest path. Generate an **ephemeral, pre-authorized, untagged** key at
[login.tailscale.com/admin/settings/keys](https://login.tailscale.com/admin/settings/keys).
The papaya node is then owned by you, so the SSH rule most personal tailnets
already have — `src: autogroup:member`, `dst: autogroup:self` — matches it and
nothing else needs changing.

Two things to know:

- **`check` mode does not work for a headless node, and fails by hanging.** If
  your `ssh` rule's action is `check`, Tailscale issues access as short-lived
  per-node grants after a browser approval. papaya cannot complete one, and an
  ephemeral node gets a new address every start so it never accumulates a grant
  either. The server then neither accepts nor rejects the connection: it goes
  silent until the dial times out. Either switch that rule to `accept`, or use
  the tagged setup below, which is `accept` but narrowly scoped.

  Note a tagged source cannot name a **user** as its destination — Tailscale
  rejects the policy with *"users in dst are only allowed from the same user"* —
  so the destination hosts must be tagged too.

- Ephemeral nodes self-delete shortly after the process exits, so repeated test
  runs will not litter your machine list. The corollary is that the key must be
  **reusable**: deregistering invalidates the stored state, so every restart
  re-enrols, and a single-use key only ever starts once.

If a connection ever hangs, `test/probe` isolates the layer at fault from a real
tsnet node — bulk transfer, SSH banner, manual key exchange, a bare handshake,
then papaya's own dial, each timed:

```bash
TS_AUTHKEY=... go run ./test/probe -host <host> -user <user> \
  -known-hosts deploy/known_hosts
```

### Deploying it (tagged, least privilege)

Restrict what the container can reach. A leaked auth key should yield a node
that can reach exactly one port on one tagged host, and nothing else.

```json
{
  "tagOwners": {
    "tag:papaya": ["you@example.com"],
    "tag:devbox": ["you@example.com"]
  },
  "acls": [
    { "action": "accept", "src": ["tag:papaya"],       "dst": ["tag:devbox:22"] },
    { "action": "accept", "src": ["you@example.com"], "dst": ["tag:papaya:80"] }
  ],
  "ssh": [
    { "action": "accept", "src": ["tag:papaya"], "dst": ["tag:devbox"], "users": ["ubuntu"] }
  ]
}
```

The `ssh` block is the part people miss. A tagged node is **not** a member of
`autogroup:member`, so the default personal-tailnet SSH rule does not cover it:
without an explicit rule naming `tag:papaya` as `src`, every connection is
denied even though the ACL permits the TCP flow. Tag the target hosts with
`tag:devbox` too, since `autogroup:self` does not apply to tagged destinations.

Then generate the auth key with **tag:papaya**, ephemeral and pre-authorized.

## Limiting what the terminal can reach

Two client-supplied fields decide where a session goes: the host and the remote
account. The picker is only a suggestion — a scripted request can name anything —
so both are checked server-side.

```bash
PAPAYA_ALLOWED_HOSTS='devbox-1,devbox-2'   # nothing else is reachable
PAPAYA_ALLOWED_SSH_USERS='ubuntu'          # no other account, and no root
```

- **root is refused unless you name it.** The tailnet SSH policy commonly ends
  `"users": ["autogroup:nonroot", "root"]`, which means a web terminal that
  passes the requested account straight through will happily open a uid 0 shell.
  `root`, `toor`, and `admin` are rejected unless listed explicitly, so widening
  the tailnet policy later cannot silently re-open that door.
- **Host matching accepts any spelling of the same machine** — short MagicDNS
  name, FQDN, or address — so `devbox-1` also matches
  `devbox-1.tailnet.ts.net`. Addresses must be listed literally.
- **The picker only offers what the API will accept.** An entry you cannot
  connect to is worse than one that is absent.
- The default `PAPAYA_SSH_USER` is validated at startup against the allow list,
  so a mismatch fails immediately rather than on every session.

Leaving both unset keeps the tailnet ACL as the only boundary, which is a
legitimate choice — but it means the terminal can reach whatever your ACL
permits, as whichever account it permits.

## Serving it under your own domain

The default name comes from Tailscale: `papaya.<tailnet>.ts.net`. Three ways to
change that, in increasing order of effort.

### Rename the tailnet (easiest, still ts.net)

DNS → **Rename tailnet…** in the admin console gives you
`papaya.yourname.ts.net`. Not your own domain, but short and memorable, and
Tailscale keeps issuing the certificate. Renaming invalidates the old name, so
the certificate is re-issued on next start.

### Your own domain (`term.example.com`)

Two independent pieces, and the second is the one people forget.

**1. DNS.** Point the name at the node's tailnet address:

```
term.example.com.  A  100.64.0.10
```

A public record holding a `100.x` address is harmless — that range is only
routable from inside your tailnet, so the record is unusable to anyone else. It
does reveal that you run Tailscale and what your internal address is; if that
bothers you, use split-horizon DNS instead and publish it only internally.

Keep the address stable with `PAPAYA_EPHEMERAL=false`, or the record needs
updating on every restart.

**2. A certificate for that name.** Tailscale only issues for `*.ts.net`, so a
custom name needs your own certificate, and because the node is not reachable
from the public internet it has to come from a **DNS-01** challenge — HTTP-01
would require Let's Encrypt to connect to you, which it cannot.

```bash
# example, using lego with a DNS provider plugin
lego --dns cloudflare --domains term.example.com --email you@example.com run

PAPAYA_TLS=true
PAPAYA_TLS_CERT=/path/to/term.example.com.crt
PAPAYA_TLS_KEY=/path/to/term.example.com.key
```

papaya serves that certificate and re-reads it when the file changes, so renewals
land without a restart. A half-written pair during renewal leaves the previous
certificate in place rather than dropping TLS.

The listener is still the tsnet one. A certificate changes the name the page
claims, never who can reach it.

### What not to do

**Tailscale Funnel would work and you should not use it.** It publishes the
service to the public internet, which discards the property the whole design
rests on: that the terminal is reachable only from your tailnet. Everything else
here — identity from `WhoIs`, no passwords, ambient auth — is safe *because* the
network boundary is doing real work. Funnel removes it.

## How authentication works

There is no password, cookie, or token anywhere in papaya. The tailnet is the
authentication layer. Every request is resolved with `WhoIs` against the local
API and checked against the allow list; only `/healthz` bypasses it. Sessions
are owned by the identity that created them, and any cross-owner access returns
**404**, not 403, so the API never confirms that another user's session exists.

Outbound SSH tries, in order:

1. `none` — Tailscale SSH in accept mode, authorized by tailnet identity + ACL
2. keyboard-interactive — Tailscale SSH check mode
3. public key from `PAPAYA_KEY_PATH` — hosts not running Tailscale SSH

## Sending an image to a remote session

Drag an image anywhere into the window, or paste one (`⌘⇧4` then `⌘V`). It is
streamed over SFTP to the active tab's host and the remote path is inserted at
the cursor:

```
ubuntu@devbox-1:~$ /home/ubuntu/.papaya-uploads/1738000000-screenshot.png ▊
```

Nothing runs on its own — no newline is injected, so you review first and press
enter yourself. If the tab is running an agent, that is exactly the moment the
path enters its prompt and it can read the file.

Details that matter:

- **The path is unquoted when it can be.** Quoting is only applied if the path
  contains shell metacharacters. A program reading stdin directly never strips
  quotes, so an always-quoted path would reach it as a filename starting with an
  apostrophe and could not be opened.
- **The path is absolute**, so it resolves whatever the shell's working
  directory is.
- Files land in `PAPAYA_UPLOAD_DIR` (default `.papaya-uploads` under the SSH
  login directory), mode `0600`, prefixed with a nanosecond timestamp so repeated
  drops of `screenshot.png` never collide.
- Filenames are sanitized to `[\w.-]` after `filepath.Base`, so a hostile name
  cannot escape the upload directory or mean anything to a shell.
- Uploads reuse the tab's existing pooled SSH connection — no extra handshake.
- Dropping several files at once uploads them one after another.

## Sessions that outlive papaya

With `PAPAYA_TMUX=true` each remote shell runs inside a named tmux session, keyed
on the tab name. The agent runs on the *remote* box and papaya is only a viewer,
so the work survives papaya restarting, the laptop sleeping, or the browser being
closed for a day. Reconnect with the same tab name and you are back in the same
session, mid-flight.

Verified end to end against a real host: a shell variable and a background job
both survived a complete papaya restart — new process, new session id, same
label.

Hosts without tmux fall back to a normal login shell, so enabling it cannot lock
you out of anything. The workspace name is sanitised to `[A-Za-z0-9_-]` and
quoted before it reaches the remote shell: it comes from a tab label you typed,
which makes it the injection surface here. `.` and `:` are stripped too, since
tmux reads them as address syntax and a name containing them attaches somewhere
other than intended.

## A dropped connection does not lose the tab

When the SSH connection under a tab dies — the papaya host's Wi-Fi blinks, a
route goes away for a minute — a tab whose shell runs in tmux keeps its place.
The work never stopped on the far side, so papaya dials the host again and
attaches the same workspace. The tab shows *connecting* meanwhile, retries with
backoff up to 30 s for as long as the tab is open, and then carries on where it
was. A shell that exits on its own still closes its tab.

Before this, one missed keepalive killed the connection, every tab on it went
red, and closing the dead tab then ran `kill-session` on the workspace the blip
had left running. Keepalives now also wait 45 s for an answer, not 15, so a
short drop that TCP survives does not tear anything down.

## Agents talking to each other

Agents in different tabs, on different machines, message each other with one
command:

```bash
papaya-say --list
papaya-say frontend "the API now returns 201 on create; reply with papaya-say backend when the UI is updated"
```

papaya finds the tab by the name in the tab bar, types the message into its
tmux workspace as a bracketed paste, and presses Enter. It refuses when a
person has unsent text in that tab's input box. Full reference, including the
API and how to install it on a machine: [docs/AGENT-MESSAGING.md](docs/AGENT-MESSAGING.md).

## Picking work back up after a restart

Remote shells keep running when papaya stops — that is what tmux is for — but
nothing in papaya remembers them. Reaching one meant recreating a tab with
exactly its original name, which nobody memorises, least of all the `-2` a
second unnamed tab on the same host is silently given.

The empty state now asks the hosts what they are running and lists anything
papaya left behind. Click one to reattach, or **Resume all**. Sessions already
on screen are excluded, and tmux sessions that are not papaya's — your own
`irc`, a stray `build-watch` — are left alone.

It costs one SSH round trip per host, so it runs after your existing tabs are
already on screen, and a host that is unreachable is skipped rather than
reported: one dead machine must not hide every other machine's work.

Resuming is gated exactly like creating a session. The host and the remote
account go through the same allow lists, and the workspace name is checked
against the shape this server could have produced — a list the server handed out
is not a capability.

## The same session on two devices

A tab open on a laptop and on a phone at once is two viewers of one session, not
a fight over it. Output goes to both, either can type, and closing one leaves the
other connected — the session is only detached when nobody is watching.

The shell has a single screen, so its geometry is the largest every viewer can
display, which is the rule tmux uses for multiple clients. A phone attaching
shrinks the shell to fit the phone; the phone leaving gives the laptop its full
terminal back. Letting the most recent resize win instead would have two devices
of different sizes stretching and shrinking each other indefinitely.

## Knowing which session wants you

Programs ring the terminal bell when they finish or need an answer. papaya turns
that into a badge on the tab and a coloured border on the panel, cleared when you
look at the session. With ten sessions open, this is the difference between
watching them and being told.

## Grid view — the control centre

Press **⌘G** or click **Grid** to switch from one-terminal-at-a-time tabs to
every session on screen at once, as resizable panels. Each panel is a live
terminal: several agent sessions stream side by side simultaneously, and the one
you click is the one that receives keystrokes, voice, and dropped files.

- **Resize** — drag the divider between any two panels. One big and three small
  is just a couple of drags; **⤢** evens everything out again.
- **Rearrange** — drag a panel by its header onto another panel to swap them.
- **Focus** — click a panel. It gets an accent ring, and voice transcription and
  keystrokes go there.
- **Drop targeting** — an image dropped *on a panel* uploads to that session,
  not to whichever panel happened to be focused.
- The arrangement and the chosen view are saved, so a reload comes back to the
  same control centre.

Layout is a split tree, the same model tmux and editor split panes use, so panels
always stay rectangular with no gaps or overlap and resizing is a single ratio
change. Switching views **moves** the terminal elements between the two layouts
rather than recreating them — a rebuilt terminal would lose its scrollback and
reconnect its socket.

## Voice input

Hold **⌃Space** to talk, or click **Voice** in the tab bar to toggle. The
recording goes to papaya, papaya sends it to OpenAI, and the transcript is
inserted at the shell cursor — same as an uploaded file's path, and with the same
rule: no newline, so nothing runs until you press enter. **Esc** discards a
recording without sending it.

While recording, a pill above the status bar shows a **live waveform**, the
elapsed time, and what the next keystroke does:

```
  ●  ▁▂▃█▇█▅▂▁▁▁   00:04   release to send · Esc cancels
```

The waveform is read from the capture stream itself, so it is a genuine signal
that audio is arriving — a muted or misrouted microphone shows a flat grey line
instead of a silent success. If nothing is heard for two seconds the hint turns
red and says so, while there is still time to fix it rather than after an empty
transcript comes back.

```bash
PAPAYA_OPENAI_API_KEY=sk-...   # enables the feature; without it the mic is hidden
PAPAYA_TLS=true                # required — see below
```

**TLS is not optional here.** Browsers only expose a microphone in a secure
context, so over plain HTTP `getUserMedia` is unavailable and the button is shown
disabled with the reason. `PAPAYA_TLS=true` serves on the tsnet TLS listener with
a Tailscale-issued certificate, which needs
[HTTPS certificates enabled](https://login.tailscale.com/admin/dns) for the
tailnet.

| Var | Default | Notes |
|---|---|---|
| `PAPAYA_OPENAI_API_KEY` | — | Also reads `OPENAI_API_KEY`. Empty disables voice |
| `PAPAYA_TRANSCRIBE_MODEL` | `gpt-transcribe` | |
| `PAPAYA_TRANSCRIBE_LANGUAGES` | — | Comma list, e.g. `en,es`. Hints only |
| `PAPAYA_TRANSCRIBE_KEYWORDS` | — | Extra literal terms to recognise |
| `PAPAYA_MAX_AUDIO_MB` | `25` | The API's own ceiling |
| `PAPAYA_OPENAI_BASE_URL` | OpenAI | Point at a compatible endpoint |

Two details that make it usable rather than merely working:

- **The API key never reaches the browser.** The page uploads audio to papaya,
  which proxies to OpenAI. Same rule as the SSH private key.
- **Terminal vocabulary is sent as recognition hints.** A general speech model
  hears "git status" as *"get status"* and "devbox-1" as *"dev box one"*. papaya
  passes a built-in list of shell commands plus the session's own host names, via
  the `keywords[]` field the API requires. Measured on the same recording:

  | hints | transcript |
  |---|---|
  | none | "**Get** status … **dev box one**" |
  | shell vocabulary | "**Git** status …" |
  | host names too | "… ssh into **devbox-1**" |

  Add project-specific terms with `PAPAYA_TRANSCRIBE_KEYWORDS`.

## Keyboard shortcuts

| Keys | Action |
|---|---|
| `⌘/Ctrl + T` | New session |
| `⌘/Ctrl + W` | Close active (confirms if it is the last) |
| `⌘/Ctrl + 1…9` | Switch to tab N |
| `⌘/Ctrl + 0` | Switch to the last tab |
| `⌘/Ctrl + ⇧ + [ / ]` | Previous / next tab |
| `⌘/Ctrl + G` | Toggle grid / tab view |
| `⌘/Ctrl + F` | Find |
| `F2` | Rename the active tab |
| `⌃Space` (hold) | Push-to-talk voice input |
| `Esc` | Discard an in-progress recording |

Double-click a tab name to rename it. Names are stored server-side, so they
survive a reload or a different browser.

## Copying text out

Selecting with the mouse and pressing `⌘/Ctrl + C` works the way it does
anywhere else — until a full-screen program takes the mouse for itself. Claude
Code, vim, htop and anything else that draws its own interface turn on mouse
reporting, and from then on a drag belongs to that program. Nothing is selected
in the browser, so there is nothing to copy, which is the usual explanation for
"I highlighted it and the paste came out empty".

Two ways through:

- **Hold Option while dragging** (Shift on Windows and Linux). That takes the
  mouse back for one gesture and makes a real browser selection, so `⌘/Ctrl + C`
  copies it. Nothing on the remote host needs to cooperate.
- **Copy on the remote host.** tmux copy-mode and anything else that emits
  OSC 52 now reaches the system clipboard. papaya turns on tmux's
  `set-clipboard` so its own copy-mode selections are sent, rather than landing
  in a paste buffer on the far side where the local machine cannot see them.
  A short confirmation appears when text arrives.

The reverse direction is deliberately not implemented. OSC 52 also defines a
*read* request, which asks the terminal to send the local clipboard to the
remote host; papaya never answers one. Otherwise any file containing the right
escape sequence would exfiltrate whatever was last copied, the moment it was
`cat`'d.

## Development

```bash
make help              # list targets
make test              # unit + full-stack tests with the race detector
make test-integration  # against a real OpenSSH server in Docker
make test-browser      # Playwright
make fuzz              # 60s per fuzz target
make lint              # vet, staticcheck, govulncheck
make arch              # structural guards
make tools             # (re)install the analysers against this module's toolchain
```

`make lint` needs `staticcheck` and `govulncheck` built with at least the Go
version in `go.mod`; a plain `go install` builds them with the older version
their own `go.mod` declares and they then cannot parse these sources. `make
tools` does it correctly — run it if lint reports `requires newer Go version`.

The frontend dev server proxies the API to a local backend:

```bash
npm --prefix web run dev
go run ./test/devserver     # a fake-identity backend on :8099
```

### Toolchain note

`tailscale.com` requires a newer Go than some machines have installed by
default. Every Make target exports `GOTOOLCHAIN=auto` so the go command fetches
the version pinned in `go.mod`. If you invoke `go` directly and see
`requires go >= 1.26`, prefix it with `GOTOOLCHAIN=auto`.

## Testing approach

The interesting failures here are concurrency races, network partitions, and
rendering, none of which conventional unit tests catch. So:

- **`-race` on everything.** A race detector hit is a build failure.
- **Real SSH, never a mocked `*ssh.Client`.** Fast tests run against a real
  in-process SSH server (genuine handshake, channels, PTY requests, SFTP
  subsystem); integration tests run against a containerized OpenSSH.
- **Correctness is machine-observable.** Resize is verified by asking the remote
  to report its own geometry with `stty size`, not by looking at pixels.
- **Structural guards as tests.** `test/arch` fails the build if a second
  WebSocket writer appears, if `net.Listen` shows up in production code, or if
  an upload handler starts buffering.

## License

MIT — see [LICENSE](LICENSE). Security issues: see [SECURITY.md](SECURITY.md).
