# AGENTS.md — setting up papaya on this machine

You are setting up **papaya** on an always-on Linux host. It is a web terminal
that lives on a Tailscale tailnet and SSHes out to other machines on that
tailnet.

Read this whole file before running anything. Several steps fail in ways that
look like something else, and the notes below are the difference between twenty
minutes and an afternoon. Each one is a mistake that has already been made.

---

## What you are building

```
Browser ──HTTPS over WireGuard──> papaya (this host) ──SSH over WireGuard──> target hosts
```

- papaya joins the tailnet **in-process**, via `tsnet`. There is no `tailscaled`
  to configure for it, no TUN device, no `NET_ADMIN`, no privileged container.
- Its HTTP server listens on a **userspace network stack**. It never binds a host
  interface. `ss -tlnp` will not show it on :443, and that is correct — do not
  "fix" it.
- Authentication is the tailnet itself. There is no password, cookie, or token.
  Every request is resolved with `WhoIs` against the caller's tailnet address.

**The single most important consequence:** identity comes from the TCP source
address. Put papaya behind a reverse proxy, a Cloudflare Tunnel, or anything else
that rewrites the source address, and every request will 403, because the source
is no longer a tailnet peer. This is not configurable. Do not do it.

---

## The short version

papaya needs no settings. Start it with a persistent data volume, hand the
operator the sign-in link it prints, and let them finish in the setup screen:

```bash
docker run -d --name papaya --restart unless-stopped \
  -v papaya-data:/var/lib/papaya ghcr.io/martoast/papaya
docker logs -f papaya
```

1. The log shows **"Sign in with Tailscale"**, a `https://login.tailscale.com/a/…`
   link, and a QR code. Give the link to the operator; they open it and approve
   the device. Do not try to complete it yourself — it is their Tailscale
   account. If the tailnet requires device approval, the log says so and links
   to the admin page.
2. Once they have signed in, the log prints a box with the address
   (`https://papaya.<tailnet>.ts.net`, or `http://…` while the tailnet's HTTPS
   certificates are off). Only the account that signed in can open it.
3. The operator follows the setup screen: tick machines, type their account on
   each, run the one command shown for each machine (`sudo tailscale set --ssh`,
   or a line that adds papaya's key to `~/.ssh/authorized_keys`), paste the
   policy snippet once, press Test.

Everything chosen is saved in the volume (`settings.json`, papaya's own key
`papaya_ed25519`, and confirmed host keys in `known_hosts`). Losing the volume
means signing in and setting up again.

If the operator is not watching the terminal, run `docker logs papaya | grep -A3
"login.tailscale.com"` to fetch the link again; it is reprinted if it expires.

---

## Automated install (optional): settings instead of the setup screen

For a shared or scripted install — a tagged node, an auth key, fixed hosts —
ask the operator for:

1. **A Tailscale auth key** — reusable, non-ephemeral, pre-approved if device
   approval is on. See the trap below about reusable.
2. **An OpenAI API key** — optional, enables voice input. Omit to disable.
3. **Which hosts and which remote accounts** papaya may reach (`PAPAYA_HOSTS`
   as `user@host`, or `PAPAYA_SSH_USER`).
4. **Who may use it** (`PAPAYA_ALLOWED_USERS` / `PAPAYA_ALLOWED_TAGS`). A
   tagged node belongs to no user, so it refuses to start without one of these.
5. **A Cloudflare (or other DNS) API token** — only if serving a custom domain.

Never accept these on a command line. Put them in `.env`, mode `600`. The process
list is readable by every other user on the machine.

### 1. Prerequisites

```bash
sudo apt update && sudo apt install -y git tmux
git clone https://github.com/martoast/papaya.git && cd papaya
command -v docker || sudo apt install -y docker.io docker-compose-plugin
```

On every **target** machine, enable Tailscale SSH (`sudo tailscale set --ssh`)
and install `tmux`. A macOS target cannot run Tailscale SSH's server; it needs
Remote Login on and papaya's key authorised for the account (the setup screen
shows the line, or use your own key with `PAPAYA_KEY_PATH`).

`tmux` matters: it is what makes sessions survive papaya restarting. Without it
papaya silently falls back to a plain login shell and that feature just does not
work.

### 2. Host keys

Machines running Tailscale SSH need nothing: their host keys come from the
tailnet. For anything else, either confirm the fingerprint once in the setup
screen, or pin keys yourself:

```bash
mkdir -p deploy
ssh-keyscan <target-host-1> <target-host-2> > deploy/known_hosts
```

There is **no** fallback to trusting whatever answers. An unknown host fails
with `unknown host` and its fingerprint. That is working as intended.

### 3. Configuration

```bash
cp .env.example .env && chmod 600 .env
$EDITOR .env
```

`PAPAYA_ALLOWED_HOSTS` and `PAPAYA_ALLOWED_SSH_USERS` are enforced server-side,
not merely used to populate the UI picker — a scripted request can name any
host and any account, and these are what stop it.

With an auth key, set `PAPAYA_EPHEMERAL=false`. An ephemeral node deregisters
when the process stops, which invalidates its stored identity and gives it a
**new address on every restart** — which then breaks any DNS record pointing at
it.

### 4. Start it

```bash
docker compose up -d --build
docker compose logs -f
```

After changing `.env`, use `docker compose up -d --force-recreate`; a plain
restart does not re-read it.

**Expect the first start to take 40–60 seconds.** Control-plane registration is
slow, and the HTTPS certificate is issued during startup. It is not hung.
Killing it early leaves a half-registered node holding the hostname, and the
next start becomes `papaya-1`, then `papaya-2`.

Healthy output ends with a box naming the address to open.

### 5. Verify — do not skip this

```bash
# reachable over the tailnet
curl https://papaya.<tailnet>.ts.net/healthz          # ok

# and nowhere else
curl -k --max-time 5 https://127.0.0.1/healthz        # must fail
ss -tlnp | grep ':443'                                # must not list papaya

# the limits are real
BASE=https://papaya.<tailnet>.ts.net
curl -s -o /dev/null -w '%{http_code}\n' -X POST $BASE/api/sessions \
  -H "Origin: $BASE" -H 'Content-Type: application/json' \
  -d '{"host":"<a-target>","user":"root"}'            # expect 403

curl -s -o /dev/null -w '%{http_code}\n' -X POST "$BASE/upload?session=x" \
  -H 'Origin: https://evil.com' -F file=@/etc/hostname # expect 403
```

If the first succeeds and the rest behave as marked, it is correctly deployed.

---

## Traps

Each of these has already cost someone hours.

**A single-use auth key only ever starts papaya once.** Combined with an
ephemeral node it is worse: stopping deregisters the node, invalidating stored
state, so the next start re-enrols and needs the key again — which is spent. The
symptom is `NeedsLogin` followed by `invalid key`. Use a **reusable** key.

**An ephemeral auth key silently overrides `PAPAYA_EPHEMERAL=false`.** Whether a
node is ephemeral is decided by the key at registration, not by the setting, and
nothing warns you: the config says `false`, the container's environment says
`false`, and the admin console shows the node tagged **Ephemeral** anyway.

That matters because the control plane reaps ephemeral nodes once they have been
offline a while. Ordinary restarts are fine — the node is still registered and
keeps its address — so this looks stable for weeks. Then a power cut or a long
reboot outlasts the grace period, the node is removed, and it comes back with a
**new address**, pointing any DNS record for it at nothing.

Check the console after the first start. If the node says Ephemeral, the key was
ephemeral; the setting cannot fix it after the fact, because the property is
fixed at registration. Re-register with a non-ephemeral key.

**Do not put it behind a tunnel or reverse proxy.** Identity is the TCP source
address. Anything that rewrites it makes `WhoIs` fail and every request 403.

**Do not enable Tailscale Funnel.** It works, and it publishes a web terminal
with shell access to the public internet. The entire security model assumes the
tailnet boundary is doing real work.

**`action: "check"` in the tailnet SSH policy cannot be satisfied.** Check mode
grants access per node after a browser approval. A headless service cannot
complete one, and the server neither accepts nor rejects — it goes silent until
the dial times out. Use `accept` (the setup screen's snippet does; for a tagged
node, scope it to the tag). The setup screen's Test button names this cause.

**DNS-01 fails on a tailnet-joined machine unless you pin public resolvers.** The
system resolver is MagicDNS (`100.100.100.100`), which answers NXDOMAIN for
public records — including the `_acme-challenge` TXT that was just written.
`deploy/setup-domain.sh` already pins `1.1.1.1` and `8.8.8.8`; anything else
doing ACME here needs the same.

**Root is refused by default, even if the tailnet ACL allows it.** `root`,
`toor`, and `admin` are rejected unless named explicitly in
`PAPAYA_ALLOWED_SSH_USERS`. A web terminal handing out uid 0 because the ACL
happened to permit it is not a default worth having.

**Building needs a current Go toolchain.** `tailscale.com` requires a newer Go
than most distributions ship. Every Make target exports `GOTOOLCHAIN=auto` so the
correct version is fetched automatically; if you invoke `go` directly and see
`requires go >= 1.26`, that is why. The Docker build handles it already.

**A command sent in the first milliseconds after a session opens can vanish.**
bash flushes pending terminal input when readline starts, and tmux repaints the
alternate screen on attach. A human never notices; automation must wait for a
prompt, and must wait on output the shell *computed* rather than on the terminal
echoing the command back.

---

## Custom domain (optional)

Only if the operator wants `term.example.com` instead of the `*.ts.net` name.

```bash
LEGO=v5.3.1   # releases are not published under a "latest" filename
curl -fsSL "https://github.com/go-acme/lego/releases/download/$LEGO/lego_${LEGO}_linux_amd64.tar.gz" \
  | sudo tar -xz -C /usr/local/bin lego

echo 'CLOUDFLARE_DNS_API_TOKEN=...' > deploy/.cloudflare && chmod 600 deploy/.cloudflare
# PAPAYA_CERT_GROUP is the uid the container runs as. Without it the
# certificate is issued 0600 owned by you, and the container cannot read it.
PAPAYA_CERT_GROUP=10001 ./deploy/setup-domain.sh term.example.com <node-name>
```

This must run **on this host** — it reads the local papaya node's tailnet address
to write the DNS record. It points an A record at that `100.x` address with
proxying **off**: a proxied record makes Cloudflare try to reach CGNAT space from
its edge, which cannot work. The record being public is harmless, since the
address is unroutable from anywhere but the tailnet.

Then set `PAPAYA_TLS_CERT` / `PAPAYA_TLS_KEY` in `.env` and restart. Renewals are
picked up without a restart, so a monthly cron is enough:

```cron
0 4 1 * * cd /path/to/papaya && PAPAYA_CERT_GROUP=10001 ./deploy/setup-domain.sh term.example.com <node-name> >> /path/to/papaya-cert.log 2>&1
```

---

## Agent messaging (`papaya-say`)

Once papaya is running, install the `papaya-say` command and skill on every
machine whose agents should be able to message each other. Steps, the API and
the rules are in [docs/AGENT-MESSAGING.md](docs/AGENT-MESSAGING.md). Check it
with `papaya-say --list` from each machine.

## Operating it

```bash
docker compose logs -f          # what it is doing
docker compose up -d --force-recreate   # after an .env change (restart does not re-read it)
docker compose down             # stop

make test                       # unit and integration, race detector
make test-browser               # Playwright
make lint                       # vet, staticcheck, govulncheck
```

Sessions run inside tmux on the *target* hosts, so restarting papaya does not
kill anyone's work. Reconnecting with the same tab name reattaches the same
session.

`README.md` covers what the thing does and every setting.
