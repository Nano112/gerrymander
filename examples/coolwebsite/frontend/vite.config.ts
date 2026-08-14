import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import gerrymander from "@gerrymander/vite";

// That's the whole integration. The plugin finds ../gerrymander.yaml,
// applies it to the daemon (zone, hostnames, sticky port, pruning), and
// configures port/allowedHosts/origin/HMR for the TLS proxy. Renaming a
// domain = edit the yaml, restart `bun run dev`.
export default defineConfig({
  plugins: [react(), gerrymander()],
});
