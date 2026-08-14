# @gerrymander/vite

One plugin line; then `bun run dev` (or `npm run dev`) is the whole workflow.

```ts
import gerrymander from "@gerrymander/vite";

export default defineConfig({
  plugins: [react(), gerrymander()],
});
```

On dev-server start the plugin finds `gerrymander.yaml` (walking up from the
vite root), applies it to the local daemon — zone created if new, hostnames
claimed, sticky port granted, labels that left the file released — and
configures vite for the TLS proxy: `port` (sticky, strict), `allowedHosts`,
`origin`, and HMR over `wss://<your-host>:443`.

Renaming a domain is: edit the yaml, restart the dev server. Nothing else.

- `gerrymander({ service: "frontend" })` when the manifest has several
  services and none is named frontend/web/app/ui/vite.
- `GERRY_API` / `GERRY_API_KEY` env override the daemon address
  (default `http://127.0.0.1:4780`).
- Daemon unreachable → a warning, and vite runs on its defaults. Production
  builds are never touched.
