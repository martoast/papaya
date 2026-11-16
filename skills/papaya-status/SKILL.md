---
name: papaya-status
description: Tell Papaya what you (the agent in this tab) are doing, so your person sees it on the tab — working, done, or needs-you with a message. Use when your person asks to set up Papaya status/notifications for their agents, or when you are about to wait on them for an answer and want the tab to say so.
---

# papaya-status: your state, on your Papaya tab

Papaya already guesses from your output whether you are working or finished.
`papaya-status` makes it exact and puts your words on the tab.

```bash
papaya-status working
papaya-status needs-you "Need a decision: keep the old API or break it?"
papaya-status done
papaya-status --setup        # the hook settings for Claude Code and Codex
```

`PAPAYA_URL` (or the address the installer saved) must point at Papaya. The tab
is found from the tmux session you run in; `--tab NAME` picks another.

## Setting it up for your person

If they ask for Papaya notifications or status for Claude Code, add the hooks
`papaya-status --setup` prints to `~/.claude/settings.json`, merging with any
hooks already there rather than replacing them. Show them the diff. For Codex,
add the `notify` line to `~/.codex/config.toml`. Hooks use `--hook`, which never
blocks or fails the agent.

## Rules

- Say `needs-you` only when you are really waiting on your person; it turns the
  tab amber and counts in their page title.
- Keep messages short and specific; never include secrets.
