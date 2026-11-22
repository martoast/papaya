# Teams: give a teammate their own machine, and nothing else

Papaya has one **admin**: whoever it was set up for (the account that signed it
in to Tailscale, or everyone in `PAPAYA_ALLOWED_USERS` / `PAPAYA_ALLOWED_TAGS`).
The admin keeps full access to everything, exactly as before.

A **teammate** is someone the admin adds in the **Team** panel (the ⋯ menu →
Team) by their Tailscale login (or a `tag:` for a tagged machine), with the
machines they may use ticked. A teammate:

- opens terminals only on their ticked machines, and only as the account that
  machine was set up with;
- sees only their own tabs — never the admin's, not even the admin's tabs on the
  teammate's own machine;
- can reach only their own tabs with `papaya-say`, `papaya-status` and the
  screen peek;
- is shown no Machines or Team settings, cannot change the voice key, and
  cannot add, change or delete themes (a theme's scene is code that runs in
  every browser that uses it). They can still pick a theme for themselves.

The admin sees every teammate's tabs (the tooltip says whose), can type into
them, talk to them with `papaya-say`, and close them.

Everything above is checked by the server on every request; the app only
hides what the server would refuse anyway. Taking a machine away from a
teammate, removing the machine from setup, or removing the teammate takes
effect on their very next request, including tabs they already have open.

With no team configured, Papaya behaves exactly as it always has.

## What keeps a teammate's machine in a silo

Two layers, and both matter:

1. **Tailscale decides what can reach what.** A teammate's machine must not be
   able to open connections to the admin's other machines at all. Papaya
   cannot enforce that; your tailnet does. Set it up as below.
2. **Papaya decides what a teammate can do through Papaya.** Their requests can
   only ever make Papaya SSH to their own machines. A program running on their
   machine (papaya-say, the join line) is identified by the Tailscale account
   that machine is logged in as, so it gets the teammate's limits and never the
   admin's.

That second point has one rule you must keep: **a teammate's machine must be
logged in to Tailscale as the teammate (or carry a tag listed as a teammate),
never as the admin.** A machine logged in as the admin is the admin, to Papaya
and to Tailscale.

## Recommended: the teammate keeps their own tailnet, and you share

This is the strongest setup, and needs no access-policy editing.

Tailscale's sharing puts a shared machine in quarantine: it can answer
connections from the tailnet it was shared into, but cannot start connections
into it. (See Tailscale's docs: [Share your machines with other
users](https://tailscale.com/kb/1084/sharing) and [Funnel vs.
sharing](https://tailscale.com/kb/1464/funnel-vs-sharing).)

**The teammate** (on their own Tailscale account):

1. Install Tailscale on their machine and sign in with **their own** account.
2. Make sure the machine runs an SSH server (on Arch/Omarchy:
   `sudo pacman -S openssh && sudo systemctl enable --now sshd`).
3. In the Tailscale admin console → Machines → their machine → **Share…**, share
   it with the admin's account, and send the invite link.

**The admin:**

1. Accept the teammate's share. Their machine now appears in your tailnet.
2. In the Tailscale admin console, open the Papaya machine (`papaya`) →
   **Share…**, and share it with the teammate's account. This is how they open
   the Papaya page. Papaya is the only machine of yours they can reach.
3. In Papaya → ⋯ → **Machines**, add the teammate's machine with the
   **Papaya key** method, and send the teammate the one line it shows
   (`curl -fsSL …/join | sh`, or the `authorized_keys` line). Use the key method:
   Tailscale SSH follows the *teammate's* tailnet policy, not yours.
4. In Papaya → ⋯ → **Team**, add the teammate's login and tick their machine.
5. Press **Test** on the machine in Machines. Done: the teammate opens
   `https://papaya.<your-tailnet>.ts.net` and sees only their machine.

What this gives you, in network terms:

- Their machine cannot start a connection to any machine of yours except
  Papaya, because the only machine of yours shared with them is Papaya, and their
  machine is quarantined in your tailnet.
- Papaya and their machine are shared both ways, which lets each start a
  connection to the other. That is intended: Papaya must SSH in, and their
  browser must reach Papaya. On Papaya, everything their machine or browser
  can do is limited to their own machines, as described above. Papaya runs
  in-process on the tailnet (tsnet), so sharing it exposes only Papaya, not
  the host it runs on.
- **Do not share any other machine of yours with the teammate.** Each one you
  share is one their (possibly compromised) machine can reach.

## Alternative: the teammate joins your tailnet as a user

If you invite them into your tailnet instead, their devices become members of
it, and your access policy is what silos them. The default policy allows every
device to reach every other, so you **must** replace it. A minimal policy (put
your own logins, Papaya's address and their machine's name in):

```jsonc
{
  "hosts": {
    "papaya": "100.x.y.z"          // Papaya's Tailscale address
  },
  "grants": [
    // You: everything, as before.
    { "src": ["alex@example.com"], "dst": ["*"], "ip": ["*"] },
    // The teammate's devices: only the Papaya page.
    { "src": ["ariosto@example.com"], "dst": ["papaya"], "ip": ["tcp:443", "tcp:80"] },
    // Papaya: SSH into the teammate's machine.
    { "src": ["papaya"], "dst": ["ariosto-box"], "ip": ["tcp:22"] }
  ]
}
```

(`ariosto-box` must be a host alias or a tag you own on their machine.) Check it
with the policy file's `tests` section before saving. This setup is easier to
get subtly wrong than sharing, which is why sharing is recommended.

## Limits worth knowing

- A teammate's terminal output is shown in your browser when you open their
  tab. Like any terminal, it renders what the remote machine sends.
- The theme library is shared: a teammate picks from the admin's themes.
- Papaya keeps one SSH key for all machines. Only its public half is ever put
  on a teammate's machine; Papaya never forwards an SSH agent.
