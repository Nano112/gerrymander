// @gerrymander/vite — the set-and-forget half of gerrymander's dev story.
//
// On `vite` / `bun run dev` startup the plugin:
//   1. finds gerrymander.yaml (walking up from the project root),
//   2. POSTs it to the local daemon (/v1/manifest/apply): zone ensured,
//      hostnames claimed, sticky port granted, stale labels pruned,
//   3. configures vite with everything a TLS-terminating proxy needs:
//      port (sticky, strict), allowedHosts, origin, and HMR over
//      wss://<your-host>:443.
//
// Renaming a domain = edit gerrymander.yaml, restart dev server. If the
// daemon is unreachable the plugin warns and does nothing — vite still runs
// on its defaults.
import fs from "node:fs";
import path from "node:path";
import { execFileSync } from "node:child_process";

const API = process.env.GERRY_API || "http://127.0.0.1:4780";

function findManifest(startDir, explicit) {
  if (explicit) return path.resolve(startDir, explicit);
  let dir = startDir;
  for (let i = 0; i < 6; i++) {
    const p = path.join(dir, "gerrymander.yaml");
    if (fs.existsSync(p)) return p;
    const parent = path.dirname(dir);
    if (parent === dir) break;
    dir = parent;
  }
  return null;
}

function pickService(services, wanted) {
  const names = Object.keys(services);
  if (wanted) {
    if (services[wanted]) return wanted;
    throw new Error(`service "${wanted}" not in manifest (has: ${names.join(", ")})`);
  }
  if (names.length === 1) return names[0];
  for (const guess of ["frontend", "web", "app", "ui", "vite"]) {
    if (services[guess]) return guess;
  }
  throw new Error(`ambiguous service — pass gerrymander({ service: "<one of ${names.join(", ")}>" })`);
}

/**
 * @param {{ service?: string, manifest?: string, https?: boolean }} [options]
 * @returns {import('vite').Plugin}
 */
// The machine's MagicDNS name (macbook.tailnet.ts.net). Requests proxied by
// `tailscale serve` keep that Host header, so vite must allow it; detection
// is best-effort and silent when tailscale is absent.
function tailnetNames() {
  const bins = ["tailscale", "/Applications/Tailscale.app/Contents/MacOS/tailscale"];
  for (const bin of bins) {
    try {
      const out = execFileSync(bin, ["status", "--json"], { timeout: 3000, stdio: ["ignore", "pipe", "ignore"] });
      const dns = JSON.parse(out.toString())?.Self?.DNSName;
      if (dns) return [dns.replace(/\.$/, "")];
    } catch {}
  }
  return [];
}

export default function gerrymander(options = {}) {
  let primaryHost = null;
  let stickyPort = null;

  return {
    name: "gerrymander",

    async config(userConfig, { command }) {
      if (command !== "serve") return; // production builds are untouched

      const root = path.resolve(userConfig.root || process.cwd());
      const manifestPath = findManifest(root, options.manifest);
      if (!manifestPath) {
        console.warn("[gerrymander] no gerrymander.yaml found — running with vite defaults");
        return;
      }

      let apply;
      try {
        const headers = { "Content-Type": "application/json" };
        if (process.env.GERRY_API_KEY) headers.Authorization = `Bearer ${process.env.GERRY_API_KEY}`;
        const res = await fetch(`${API}/v1/manifest/apply`, {
          method: "POST",
          headers,
          body: JSON.stringify({ yaml: fs.readFileSync(manifestPath, "utf8") }),
          signal: AbortSignal.timeout(4000),
        });
        apply = await res.json();
        if (!res.ok) {
          console.warn(`[gerrymander] ${apply.error ?? res.status}: ${apply.message ?? "apply failed"} — vite defaults`);
          return;
        }
      } catch (e) {
        console.warn(`[gerrymander] daemon unreachable at ${API} (${e.message}) — vite defaults`);
        return;
      }

      const name = pickService(apply.services, options.service);
      const svc = apply.services[name];
      primaryHost = svc.hostnames?.[0] ?? apply.zone;
      stickyPort = svc.port || undefined;

      for (const fqdn of apply.pruned ?? []) {
        console.log(`[gerrymander] released ${fqdn} (left the manifest)`);
      }

      const allowedHosts = [
        ...(svc.hostnames ?? []),
        ...(svc.wildcards ?? []).map((w) => `.${w}`), // leading dot = any subdomain
        ...(options.allowedHosts ?? []),
        ...tailnetNames(), // machine's MagicDNS name — tailscale serve paths just work
      ];

      return {
        server: {
          host: "127.0.0.1",
          ...(stickyPort ? { port: stickyPort, strictPort: true } : {}),
          allowedHosts,
          origin: `https://${primaryHost}`,
          hmr: {
            protocol: "wss",
            host: primaryHost,
            clientPort: 443,
          },
        },
      };
    },

    configureServer(server) {
      if (!primaryHost) return;
      server.httpServer?.once("listening", () => {
        setTimeout(() => {
          server.config.logger.info(
            `  \x1b[32m➜\x1b[0m  \x1b[1mgerrymander\x1b[0m: https://${primaryHost}/  \x1b[2m(sticky port ${stickyPort})\x1b[0m`,
          );
        }, 60);
      });
    },
  };
}
