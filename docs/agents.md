# AI agents (MCP)

Every coding agent eventually starts a dev server, and every one of them
guesses port 8000 — into a port that's already taken, on a machine it can't
see. gerrymander ships an [MCP](https://modelcontextprotocol.io) server so
agents allocate instead of guessing.

## Register

```bash
claude mcp add gerry -- gerry mcp
```

(Any MCP-capable client works — `gerry mcp` speaks MCP over stdio and
talks to the local daemon.)

## What the agent gets

- **ports**: claim a sticky port for a named owner — the same owner gets
  the same port next session, from a pool that avoids the crowded defaults.
  No more "is 8000 free?" followed by a crash.
- **hostnames**: check availability, claim `api.myproject.test`, list and
  release — so an agent can set up a working `https://` dev URL end to end,
  including for a container it just started.
- **status**: what's registered, what's listening, what the proxy routes —
  the machine's actual state instead of the agent's assumptions.

## Why this beats "just pick a port"

The failure mode isn't picking a port — it's two agents (or an agent and
you) picking the *same* one, or an agent leaving stale processes on ports
it forgot. Registry semantics fix both: claims are exclusive and owned, so
a second claim by the same owner returns the *same* port (idempotent), a
different owner gets a different one, and `gerry ls` / the desktop app show
exactly who holds what, with supervised processes stoppable by name.

Give a long-running agent a [scoped token](tokens.md) and it can manage its
own project's hostnames without being able to touch anything else.
