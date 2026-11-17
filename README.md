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
- **Themes your agent builds.** Say "make Papaya a koi pond" and your agent
  writes the colours and an animated backdrop and switches every open window
  to it, live. Backdrops react to your agents: work, bells, messages between
  tabs. They run sandboxed, with no network and no access to your terminals.
- **Know what every agent is doing.** Each tab shows *working* (a spinner while
  output streams), *your turn* (it finished while you were elsewhere) or
  *needs you* (it rang the bell), and the page title counts the tabs waiting on
  you. Worked out from the output stream, so it works with any agent.
- **Installs as an app** on your phone's home screen.

## Quick start

You need a [Tailscale](https://tailscale.com/download) network (free for
personal use) and an always-on machine to run Papaya on. That's it: no keys to
create, no settings file.

```bash
curl -fsSL https://papaya.fullstacklabs.org/install.sh | sh
```

1. **Sign in.** The installer starts Papaya (in Docker if you have it,
   otherwise as a background service) and shows a *Sign in with Tailscale*
   link. Approve the new device; Papaya joins your tailnet as *your* device,
   so only you can use it.
2. **Open it.** The installer then prints Papaya's address, like
   `https://papaya.your-tailnet.ts.net`. Open it from any device on your
   tailnet.
3. **Connect your machines.** Pick the ones you want, then paste the one line
   the setup screen shows (`curl -fsSL papaya/join | sh`) into a terminal on
   each. It tells Papaya who you are there and lets it in; the machine turns
   green on the setup screen by itself. No usernames, keys or policy edits.

On each machine your agents run on, install the agent tools (`papaya-say`,
`papaya-theme`, `papaya-status` and their skills):

```bash
curl -fsSL https://papaya.fullstacklabs.org/install.sh | sh -s -- --tools --url https://papaya.your-tailnet.ts.net
```

The installer is [one readable script](site/install.sh): it verifies the
release checksum, and re-running it upgrades in place. Prefer Docker by hand?

```bash
docker run -d --name papaya --restart unless-stopped -e PAPAYA_TMUX=true \
  -v papaya-data:/var/lib/papaya ghcr.io/martoast/papaya
docker logs -f papaya
```

On a machine without Docker, you can also download the binary for Linux or macOS from the
[releases page](https://github.com/martoast/papaya/releases) and run `papaya`
with `PAPAYA_STATE_DIR` pointing at a folder it can keep (`deploy/papaya.service`
is a systemd unit; `make service-install` sets up launchd on macOS).

**How Papaya gets into your machines.** The setup screen offers two ways, per
machine:

- **Tailscale SSH (Linux, recommended).** Run `sudo tailscale set --ssh` on the
  machine, and change your tailnet's `ssh` rule from `"check"` to `"accept"`
  once (the screen gives you the snippet to paste). No keys anywhere: the
  tailnet identity is the credential, and the machine's host key comes from the
  tailnet too.
- **Papaya key (Macs, and anything running its own SSH server).** Papaya makes
  its own SSH key and shows you the one line to add to `~/.ssh/authorized_keys`
  there. The first connection shows the machine's fingerprint for you to
  confirm.

**HTTPS** turns on by itself when your tailnet has *HTTPS Certificates* enabled
(Tailscale admin → DNS). Until then Papaya serves plain HTTP over the tailnet's
encrypted connection and the setup screen tells you where to switch it on.
Voice input needs HTTPS.

**Easiest of all:** point your coding agent at [AGENTS.md](AGENTS.md); it walks
through the same steps and knows every trap.

## Advanced configuration

Everything above works with no settings at all; choices made in the setup
screen are kept in `$PAPAYA_STATE_DIR/settings.json`. Environment variables
are for automated or multi-user installs, and they win wherever they overlap
with the setup screen. They are validated before the node starts.

| Var | Required | Default | Notes |
|---|---|---|---|
| `TS_AUTHKEY` | no | — | Join without the browser sign-in (e.g. a tagged key for automation) |
| `PAPAYA_ALLOWED_USERS` | no¹ | owner only | Comma list of tailnet login names |
| `PAPAYA_ALLOWED_TAGS` | no¹ | — | Comma list, e.g. `tag:ci` |
| `PAPAYA_HOSTNAME` | no | `papaya` | Tailnet node name |
| `PAPAYA_STATE_DIR` | no | `/var/lib/papaya` | Created if absent |
| `PAPAYA_EPHEMERAL` | no | `true` with `TS_AUTHKEY`, else `false` | Node self-deletes on clean exit |
| `PAPAYA_SSH_USER` | no² | — | Default remote account. No built-in default; the setup screen sets one per machine |
| `PAPAYA_HOSTS` | no | — | Comma list for the picker (suggestion only) |
| `PAPAYA_ALLOWED_HOSTS` | no | — | **Enforced** allow list of hosts. Empty = any host the ACL permits |
| `PAPAYA_ALLOWED_SSH_USERS` | no | — | **Enforced** allow list of remote accounts. Empty = any account, root included |
| `PAPAYA_TMUX` | no | `false` | Run remote shells in tmux so work survives restarts |
| `PAPAYA_TLS` | no | automatic | Unset: HTTPS as soon as the tailnet offers a cert, HTTP until then. `true`/`false` forces it |
| `PAPAYA_TLS_CERT` | no | — | Your own certificate, for a custom domain |
| `PAPAYA_TLS_KEY` | no | — | Its key. Both or neither |
| `PAPAYA_UPLOAD_DIR` | no | `.papaya-uploads` | Relative to the SSH login dir |
| `PAPAYA_MAX_UPLOAD_MB` | no | `25` | |
| `PAPAYA_MAX_SESSIONS` | no | `10` | Per owner |
| `PAPAYA_USE_TAILSCALE_SSH` | no | `true` | Try tailnet-identity auth first |
| `PAPAYA_KEY_PATH` | no | generated | Private key for non-Tailscale-SSH hosts. Default: `$PAPAYA_STATE_DIR/papaya_ed25519`, made on first start |
| `PAPAYA_KNOWN_HOSTS` | no | `/etc/papaya/known_hosts` | Your own pinned host keys, read-only. Keys confirmed in the setup screen go to `$PAPAYA_STATE_DIR/known_hosts` |
| `PAPAYA_INSECURE_SKIP_HOST_KEY` | no | `false` | Dev only; warns per connection |
| `PAPAYA_DETACHED_TTL` | no | `30m` | Reaper for client-less sessions |

¹ With neither set, only the tailnet user Papaya is signed in as can use it —
never everyone. A *tagged* node belongs to no user, so it must set one of them
or it refuses to start.

² If you list `PAPAYA_HOSTS`, each needs an account: this default, or the host
written as `user@host`.

Settings from before the rename (`TSTERM_*`) are still read when the matching
`PAPAYA_*` one is unset.

## Tailnet setup with an auth key (advanced)

The setup screen covers a personal tailnet. This section is for running Papaya
with a pre-made `TS_AUTHKEY` instead of the browser sign-in, for example as a
tagged node shared by a team.

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
PAPAYA_ALLOWED_SSH_USERS='ubuntu'          # no other account
```

- **Any account, root included, unless you restrict it.** Papaya is reachable
  only from your own Tailscale devices and you choose its machines, so it logs
  in as whatever account you give it (many servers only have root). To pin it
  down, list the only accounts allowed in `PAPAYA_ALLOWED_SSH_USERS`.
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

## Themes

Tell your agent the look you want. It writes a theme (colours, plus an optional
animated backdrop drawn behind the terminals) and pushes it with
`papaya-theme`; every Papaya window you have open switches at once:

```bash
papaya-theme new koi-pond        # a starter theme.json + scene.js
papaya-theme push ./koi-pond     # upload, switch, and report whether it runs
papaya-theme list                # built-ins: koi-pond, honu-lagoon, night-reef, snow-pines, aurora, ember…
papaya-theme use default         # back to the plain look
```

`push` waits for your browser's verdict and prints it: running at N fps, or
the scene's error with line numbers in the agent's own `scene.js`, so the agent
can fix its work without you looking. Backdrops react to what is happening:
a tab printing, a bell, one agent messaging another, or a moment an agent
marks with `papaya-theme cue merged`.

A backdrop is code someone else wrote, so it never runs in Papaya's page. It
runs in a sandboxed frame with an opaque origin and a policy that forbids all
network access; the frame cannot take focus or see keystrokes, and only event
and tab names ever cross into it. Colours are checked value by value before
they touch the page. While a backdrop runs, terminals draw with xterm's DOM
renderer (WebGL paints an opaque background) and go back to WebGL when it is
off.

The format, the scene API and the rules agents follow:
[docs/THEMES.md](docs/THEMES.md) and the skill
[skills/papaya-theme/SKILL.md](skills/papaya-theme/SKILL.md).

## Agent status on every tab

Each tab shows whether its agent is working, finished while you looked
elsewhere (*your turn*), or needs you, worked out from the terminal output
with no setup. Agents can also say it exactly, with their own words on the tab,
through `papaya-status`:

```bash
papaya-status --setup    # prints the hooks for Claude Code and Codex
```

With the Claude Code hooks, submitting a prompt marks the tab working, Claude
stopping marks it done, and a notification ("Claude needs your permission to
use Bash") turns it amber with that message. The command finds its tab from the
tmux session, like papaya-say, and never slows the agent down or fails it.
Programs outside tmux can print OSC 7501 (`ESC ] 7501 ; state=working ESC \`),
the open status code herdr published; tmux drops unknown escape codes, which is
why the command exists.

## Tabs survive restarts and updates

With tmux on (`PAPAYA_TMUX=true`, which the installer sets), Papaya keeps its
tab list in `$PAPAYA_STATE_DIR/sessions.json`. After a restart or an upgrade
every tab comes back with its id, name and order, attached to its workspace
again, and open browsers simply reconnect.

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
