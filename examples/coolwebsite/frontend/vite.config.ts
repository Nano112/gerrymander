import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The three lines that make Vite happy behind a TLS-terminating dev proxy:
//
// 1. allowedHosts — Vite 6 rejects requests whose Host header isn't
//    localhost; the proxy forwards the original Host (coolwebsite.test).
// 2. hmr — the HMR websocket must connect back through the proxy
//    (wss://coolwebsite.test:443), not to the raw dev-server port.
// 3. strictPort — the port is a sticky gerrymander grant; failing loudly
//    beats silently drifting to port+1 while the proxy points at the grant.
export default defineConfig({
  plugins: [react()],
  server: {
    allowedHosts: ["coolwebsite.test"],
    strictPort: true,
    hmr: {
      protocol: "wss",
      host: "coolwebsite.test",
      clientPort: 443,
    },
  },
});
