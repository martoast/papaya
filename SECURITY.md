# Security

Papaya hands out shells on other people's machines, so security reports are the
most important issues this project gets.

## Reporting a vulnerability

Please **do not open a public issue**. Report it privately through GitHub:
**Security → Report a vulnerability** on this repository (a private security
advisory). Include what you found, how to reproduce it, and what an attacker
gains.

We aim to acknowledge a report within 3 days and to ship a fix or a mitigation
for a confirmed issue within 30 days, and we will credit you in the release
notes unless you would rather we did not.

## Scope

In scope: anything that lets someone reach a shell, a session, a file or a
clipboard they should not — for example bypassing the tailnet identity check or
the host and account allow lists, cross-site requests from a browser, escape
sequences that read or exfiltrate data, upload path traversal, or one user
reaching another user's sessions.

Out of scope: attacks that require already controlling the machine Papaya runs
on, a target host, or the tailnet's admin console; and deployments that put
Papaya behind a reverse proxy, a tunnel or Tailscale Funnel, which the README
tells you not to do.

## How Papaya is meant to be secure

- It binds no host port. It joins your tailnet in-process and listens only
  there.
- Every request is identified by its tailnet address (`WhoIs`) and checked
  against `PAPAYA_ALLOWED_USERS` / `PAPAYA_ALLOWED_TAGS`. There are no
  passwords or cookies to steal.
- Hosts and remote accounts are enforced server-side, and root is refused unless
  named explicitly.
- SSH host keys are verified against a pinned `known_hosts`.
