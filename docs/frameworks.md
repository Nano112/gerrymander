# Framework recipes

gerry integrates through two primitives, a routed hostname and a sticky
port, so "integration" is mostly *which of two mechanisms hands your tool
its port*:

| Mechanism | For | How |
|---|---|---|
| `gerrymander-vite` plugin | Anything vite-based | One plugin line; applies the manifest, sets port/allowedHosts/origin/HMR itself |
| `gerry run` | Everything else | `gerry run --owner proj/svc -- CMD --port '{PORT}'`: sets `$PORT`, substitutes `{PORT}`, execs |

Verification status is labeled honestly: ✅ ran here end-to-end, 🔶 same
primitives, not yet exercised.

## ✅ Vite (React, Vue, plain): plugin

```ts
// vite.config.ts
import gerrymander from "gerrymander-vite";
export default defineConfig({ plugins: [react(), gerrymander()] });
```
`bun run dev` / `npm run dev` is the whole workflow. Verified end-to-end in
`examples/coolwebsite` (bun runtime, HMR over wss, rename-and-prune).

## ✅ Python (FastAPI / Django / Flask): gerry dev

Declare the command in the manifest and the whole workflow is one word:

```yaml
services:
  api:
    hostnames: [api.myproj.test]
    port_pool: dev
    dev: uv run uvicorn main:app --port {PORT} --reload
```
```sh
gerry dev api    # applies the manifest, grants the sticky port, runs it
```
`gerry run --owner myproj/api -- CMD --port '{PORT}'` remains the ad-hoc
form. Remember CORS when the frontend lives on a sibling hostname; see the
example's `backend/main.py`.

## ✅ Dockerized apps: docker backend (auto-relay)

A container that publishes no ports can still be a backend: gerry keeps
a tiny socat relay on its network, publishing a sticky loopback port on
demand. Zero compose edits.

```yaml
services:
  app:
    hostnames: [myproj.test, "*.myproj.test"]
    docker: { network: myproj_default, host: laravel.test, port: 80 }
```
Verified live: an unpublished `nginx:alpine` served over TLS through the
proxy, relay auto-created on first request (`docker ps` shows it as
`gerry-relay-*`, labeled `app.gerrymander.relay`). Needs the docker CLI on
the daemon's host; first use pulls `alpine/socat`.

## ✅ Bun runtime: either

`bunx --bun vite` works under the plugin (verified). A native server:

```ts
// Bun.serve reads $PORT by convention; gerry run provides it.
Bun.serve({ port: Number(process.env.PORT), fetch: handler });
```
```sh
gerry run --owner myproj/api -- bun run server.ts
```

## 🔶 SvelteKit / Astro / Nuxt: plugin (vite-based)

All three embed vite, so `gerrymander-vite` slots into their vite plugin
arrays:

```js
// svelte.config.js consumers: vite.config.ts as usual
export default defineConfig({ plugins: [sveltekit(), gerrymander()] });
// astro.config.mjs
export default defineConfig({ vite: { plugins: [gerrymander()] } });
```
Caveat to watch: each framework layers its own `server` defaults; if one
overrides the port after plugins run, fall back to `gerry run` + framework
port flag.

## 🔶 Next.js: gerry run

Next isn't vite; the courier does it:

```sh
gerry run --owner myproj/web -- next dev -p '{PORT}'
```
Next 15+ blocks cross-origin dev assets like Vite does. Add your hostname:

```js
// next.config.js
module.exports = { allowedDevOrigins: ["myproj.test"] };
```
HMR: Next's websocket uses the page origin, so it flows through the proxy
without extra config.

## 🔶 Rails / Django / Go / anything

```sh
gerry run --owner myproj/app -- rails server -p '{PORT}'
gerry run --owner myproj/app -- python manage.py runserver '127.0.0.1:{PORT}'
gerry run --owner myproj/app -- go run . --addr ':{PORT}'
```

## The manifest that backs all of these

```yaml
project: myproj
zone: myproj.test
services:
  web: { hostnames: [myproj.test, "*.myproj.test"], port_pool: dev }
  api: { hostnames: [api.myproj.test], port_pool: dev }
```
`gerry init` scaffolds it; `gerry up` (or the vite plugin) applies it;
editing a hostname and restarting re-assigns it (old labels are pruned).
`gerry status` diagnoses the stack when anything misbehaves.
