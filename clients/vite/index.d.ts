import type { Plugin } from "vite";

export interface GerrymanderOptions {
  /** Service name from gerrymander.yaml this vite instance serves.
   *  Auto-detected when the manifest has one service or one named
   *  frontend/web/app/ui/vite. */
  service?: string;
  /** Path to gerrymander.yaml, relative to the vite root. Auto-discovered
   *  by walking up from the root when omitted. */
  manifest?: string;
}

export default function gerrymander(options?: GerrymanderOptions): Plugin;
