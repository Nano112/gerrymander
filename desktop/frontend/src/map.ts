// The district map: gerrymander drawing its own districts. Deterministic
// two-column SVG — zone territories with hostname nodes on the left,
// deduplicated backends on the right, crayon-dashed beziers between them.
import { GetMap, OpenURL } from "../wailsjs/go/main/App";

const SVG = "http://www.w3.org/2000/svg";

const LAYOUT = {
  hostRow: 40,
  zonePadTop: 46,
  zonePadBottom: 16,
  zoneGap: 26,
  zoneX: 24,
  zoneW: 320,
  backendX: 640,
  backendW: 210,
  backendRow: 58,
  topPad: 16,
};

interface Edge { backend: string; listen: number }
interface Host {
  id: number; label: string; fqdn: string; kind: string; state: string;
  wildcard: boolean; owner?: string; edges?: Edge[];
}
interface Zone { name: string; profile: string; hosts: Host[] }
interface Backend { id: string; kind: string; name: string; sub: string }
interface MapData { zones: Zone[]; backends: Backend[] }

function svgEl<K extends keyof SVGElementTagNameMap>(tag: K, attrs: Record<string, string | number> = {}): SVGElementTagNameMap[K] {
  const e = document.createElementNS(SVG, tag);
  for (const [k, v] of Object.entries(attrs)) e.setAttribute(k, String(v));
  return e;
}

function text(x: number, y: number, content: string, cls: string): SVGTextElement {
  const t = svgEl("text", { x, y, class: cls });
  t.textContent = content;
  return t;
}

export async function renderMap(view: HTMLElement, renderEmpty: (t: string, b: string) => void) {
  let data: MapData;
  try {
    data = (await GetMap()) as MapData;
  } catch (e) {
    renderEmpty("Could not draw the map", String(e));
    return;
  }
  if (!data.zones?.length) {
    renderEmpty("Nothing to map yet", "Claim a hostname or apply a manifest and the districts appear here.");
    return;
  }

  view.replaceChildren();
  const wrap = document.createElement("div");
  wrap.className = "map-wrap";

  // ---- layout: zones stacked, hosts positioned, backends packed ----
  const hostPos = new Map<Host, { x: number; y: number }>();
  let y = LAYOUT.topPad;
  const zoneRects: { z: Zone; y: number; h: number }[] = [];
  for (const z of data.zones) {
    const h = LAYOUT.zonePadTop + Math.max(z.hosts.length, 1) * LAYOUT.hostRow + LAYOUT.zonePadBottom;
    zoneRects.push({ z, y, h });
    z.hosts.forEach((host, i) => {
      hostPos.set(host, { x: LAYOUT.zoneX + LAYOUT.zoneW, y: y + LAYOUT.zonePadTop + i * LAYOUT.hostRow + 14 });
    });
    y += h + LAYOUT.zoneGap;
  }
  const totalZoneHeight = y;

  // Backends: desired y = mean of connected hosts, then packed into slots.
  const wantY = new Map<string, number[]>();
  for (const z of data.zones)
    for (const host of z.hosts)
      for (const e of host.edges ?? []) {
        const arr = wantY.get(e.backend) ?? [];
        arr.push(hostPos.get(host)!.y);
        wantY.set(e.backend, arr);
      }
  const connected = data.backends.filter((b) => wantY.has(b.id));
  const desired = connected
    .map((b) => ({ b, want: avg(wantY.get(b.id)!) }))
    .sort((a, c) => a.want - c.want);
  const backendPos = new Map<string, number>();
  let cursor = LAYOUT.topPad + 10;
  for (const d of desired) {
    const yPos = Math.max(cursor, d.want - LAYOUT.backendRow / 2);
    backendPos.set(d.b.id, yPos);
    cursor = yPos + LAYOUT.backendRow;
  }
  const height = Math.max(totalZoneHeight, cursor) + 8;
  const width = LAYOUT.backendX + LAYOUT.backendW + 24;

  const svg = svgEl("svg", { viewBox: `0 0 ${width} ${height}`, class: "district-map" });
  svg.style.minWidth = `${width}px`;

  // ---- edges (under nodes) ----
  const edgeLayer = svgEl("g");
  const edgesByHost = new Map<Host, SVGPathElement[]>();
  for (const z of data.zones)
    for (const host of z.hosts)
      for (const e of host.edges ?? []) {
        const from = hostPos.get(host)!;
        const toY = (backendPos.get(e.backend) ?? 0) + 24;
        const toX = LAYOUT.backendX;
        const midX = (from.x + toX) / 2;
        const p = svgEl("path", {
          d: `M ${from.x} ${from.y} C ${midX} ${from.y}, ${midX} ${toY}, ${toX} ${toY}`,
          class: `map-edge state-${host.state}`,
        });
        edgeLayer.append(p);
        const arr = edgesByHost.get(host) ?? [];
        arr.push(p);
        edgesByHost.set(host, arr);
        if (e.listen && e.listen !== 443 && e.listen !== 0) {
          const tag = text(midX, (from.y + toY) / 2 - 6, `:${e.listen}`, "map-listen");
          tag.setAttribute("text-anchor", "middle");
          edgeLayer.append(tag);
        }
      }
  svg.append(edgeLayer);

  // ---- zone territories ----
  for (const { z, y: zy, h } of zoneRects) {
    const g = svgEl("g");
    g.append(svgEl("rect", {
      x: LAYOUT.zoneX, y: zy, width: LAYOUT.zoneW, height: h,
      rx: 14, class: "map-zone",
    }));
    g.append(text(LAYOUT.zoneX + 18, zy + 28, z.name, "map-zone-name"));
    const profile = text(LAYOUT.zoneX + LAYOUT.zoneW - 18, zy + 28, z.profile.toUpperCase(), "map-zone-profile");
    profile.setAttribute("text-anchor", "end");
    g.append(profile);

    for (const host of z.hosts) {
      const pos = hostPos.get(host)!;
      const hg = svgEl("g", { class: "map-host" });
      hg.append(svgEl("circle", {
        cx: LAYOUT.zoneX + 22, cy: pos.y, r: 5,
        class: `map-blob state-${host.state}`,
      }));
      const label = host.wildcard && !host.label.startsWith("*.") ? `${labelText(host)} +*` : labelText(host);
      const t = text(LAYOUT.zoneX + 36, pos.y + 4, label, `map-host-label kind-${host.kind}`);
      hg.append(t);
      if (host.kind === "tenant") {
        const chip = text(LAYOUT.zoneX + LAYOUT.zoneW - 16, pos.y + 4, "tenant", "map-kind-chip");
        chip.setAttribute("text-anchor", "end");
        hg.append(chip);
      }
      // hover: spotlight this host's edges
      hg.addEventListener("mouseenter", () => {
        svg.classList.add("dimmed");
        (edgesByHost.get(host) ?? []).forEach((p) => p.classList.add("lit"));
        hg.classList.add("lit");
      });
      hg.addEventListener("mouseleave", () => {
        svg.classList.remove("dimmed");
        (edgesByHost.get(host) ?? []).forEach((p) => p.classList.remove("lit"));
        hg.classList.remove("lit");
      });
      hg.addEventListener("click", () => {
        if (!host.label.startsWith("*.")) OpenURL(`https://${host.fqdn.replace(/^\*\./, "")}`);
      });
      g.append(hg);
    }
    svg.append(g);
  }

  // ---- backend nodes ----
  for (const b of connected) {
    const by = backendPos.get(b.id)!;
    const g = svgEl("g", { class: "map-backend-g" });
    g.append(svgEl("rect", {
      x: LAYOUT.backendX, y: by, width: LAYOUT.backendW, height: 46,
      rx: 10, class: `map-backend bk-${b.kind}`,
    }));
    g.append(text(LAYOUT.backendX + 14, by + 20, backendGlyph(b.kind) + "  " + b.name, "map-backend-name"));
    g.append(text(LAYOUT.backendX + 14, by + 37, b.sub, "map-backend-sub"));
    svg.append(g);
  }

  // legend
  const legend = document.createElement("div");
  legend.className = "map-legend";
  legend.innerHTML =
    `<span><i class="lg lg-green"></i>active</span>` +
    `<span><i class="lg lg-parchment"></i>pending</span>` +
    `<span><i class="lg lg-vermilion"></i>failed</span>` +
    `<span class="lg-note">hover a hostname to trace its route · click to open</span>`;

  wrap.append(svg);
  view.append(legend, wrap);
}

function labelText(h: Host): string {
  if (h.label === "@") return h.fqdn;
  return h.fqdn;
}

function backendGlyph(kind: string): string {
  switch (kind) {
    case "host": return "▖";       // this machine
    case "address": return "◫";    // container / remote
    case "supervised": return "✳";
    case "service": return "⬡";    // k8s
    default: return "•";
  }
}

function avg(ns: number[]): number {
  return ns.reduce((a, b) => a + b, 0) / ns.length;
}
