// Gerrymander desktop frontend. Talks to the Go backend via Wails bindings.
import {
  GetStatus, GetTree, GetPorts, GetProcesses, GetProjects, GetSettings, SaveSettings,
  Claim, CreateZone, Release, Rename, Availability, KillPort, StartProcess, StopProcess,
  ProcessLogs, OpenURL, DaemonUp, DaemonDown,
} from "../wailsjs/go/main/App";
import { renderMap } from "./map";
import iconUrl from "./assets/gerry-icon.png";

type View = "map" | "districts" | "projects" | "ports" | "processes";

interface TreeNode {
  id?: number;
  label: string;
  fqdn: string;
  kind?: string;
  state?: string;
  source?: string;
  owner?: string;
  wildcard?: boolean;
  routes?: string[];
  children?: TreeNode[];
}

let currentView: View = "map";
let apiUp = false;

(document.getElementById("brand-icon") as HTMLImageElement).src = iconUrl;

/* ---------- dialog helpers (WKWebView has no reliable confirm/prompt) ---------- */

interface AskOpts {
  title: string;
  body?: string;
  input?: { placeholder?: string; value?: string };
  okLabel?: string;
  danger?: boolean;
}

function ask(opts: AskOpts): Promise<string | null> {
  const dlg = document.getElementById("ask-dialog") as HTMLDialogElement;
  const input = document.getElementById("ask-input") as HTMLInputElement;
  const inputLabel = document.getElementById("ask-input-label") as HTMLElement;
  const ok = document.getElementById("ask-ok") as HTMLButtonElement;
  (document.getElementById("ask-title") as HTMLElement).textContent = opts.title;
  (document.getElementById("ask-body") as HTMLElement).textContent = opts.body ?? "";
  inputLabel.hidden = !opts.input;
  input.value = opts.input?.value ?? "";
  input.placeholder = opts.input?.placeholder ?? "";
  ok.textContent = opts.okLabel ?? "OK";
  ok.className = opts.danger ? "btn btn-primary danger" : "btn btn-primary";
  return new Promise((resolve) => {
    const done = () => {
      dlg.removeEventListener("close", done);
      if (dlg.returnValue !== "ok") return resolve(null);
      resolve(opts.input ? input.value.trim() : "ok");
    };
    dlg.addEventListener("close", done);
    dlg.showModal();
    if (opts.input) input.focus();
  });
}

const confirmAsk = async (title: string, body: string, okLabel = "Confirm") =>
  (await ask({ title, body, okLabel, danger: true })) !== null;
const notify = (title: string, body: string) => ask({ title, body, okLabel: "Close" });

const $ = <T extends HTMLElement>(sel: string) => document.querySelector(sel) as T;
const view = $("#view");

function el(tag: string, cls?: string, text?: string): HTMLElement {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

/* ---------- header status ---------- */

async function refreshStatus() {
  const pill = $("#daemon-pill");
  const counts = $("#counts");
  const toggle = $("#daemon-toggle") as HTMLButtonElement;
  try {
    const st = await GetStatus();
    apiUp = st.api_reachable;
    if (apiUp) {
      pill.className = "pill pill-ok";
      pill.textContent = "daemon up";
      counts.innerHTML = "";
      counts.append(`${st.zones} zones · ${st.allocations} hostnames`);
      if (st.conflicts > 0) {
        const c = el("span", "pill pill-conflict", `${st.conflicts} conflicts`);
        counts.append(" ", c);
      }
      toggle.hidden = false;
      toggle.textContent = "Stop daemon";
      toggle.onclick = () => daemonAction(DaemonDown, "Stopping…");
    } else {
      pill.className = "pill pill-down";
      pill.textContent = "daemon unreachable";
      counts.textContent = st.api_base;
      toggle.hidden = false;
      toggle.textContent = "Start daemon";
      toggle.onclick = () => daemonAction(DaemonUp, "Starting…");
    }
  } catch {
    apiUp = false;
    pill.className = "pill pill-down";
    pill.textContent = "daemon unreachable";
  }
}

async function daemonAction(fn: () => Promise<string>, label: string) {
  const toggle = $("#daemon-toggle") as HTMLButtonElement;
  toggle.disabled = true;
  toggle.textContent = label;
  try {
    await fn();
  } catch (e) {
    alertError(String(e));
  } finally {
    toggle.disabled = false;
    setTimeout(() => { refreshStatus(); render(); }, 1200);
  }
}

function alertError(msg: string) {
  const pill = $("#daemon-pill");
  pill.className = "pill pill-down";
  pill.textContent = msg.slice(0, 80);
}

/* ---------- districts ---------- */

async function renderDistricts() {
  if (!apiUp) {
    renderEmpty(
      "The registry is offline",
      "Start the local daemon to see your districts, or point Settings at another gerry instance.",
    );
    return;
  }
  let tree: TreeNode[];
  try {
    tree = (await GetTree()) ?? [];
  } catch (e) {
    renderEmpty("Could not load districts", String(e));
    return;
  }
  view.replaceChildren();
  const toolbar = el("div", "toolbar");
  const addZone = el("button", "btn btn-quiet", "+ New zone") as HTMLButtonElement;
  addZone.onclick = async () => {
    const name = await ask({
      title: "New zone",
      body: "A zone is a namespace of hostnames, usually a .test domain. Projects can also create zones implicitly via gerry up.",
      input: { placeholder: "myproject.test" },
      okLabel: "Create zone",
    });
    if (!name) return;
    try { await CreateZone(name, "dev"); render(); } catch (e) { await notify("Zone creation failed", String(e)); }
  };
  toolbar.append(addZone);
  view.append(toolbar);
  if (tree.length === 0) {
    renderEmpty("No zones yet", "Create one above, or run gerry up in a project — manifests create their zone automatically.");
    return;
  }
  for (const zone of tree) {
    const box = el("div", "zone");
    const head = el("div", "zone-head");
    head.append(
      el("span", "zone-name", zone.fqdn),
      el("span", "zone-profile", zone.source ?? ""),
    );
    const claimBtn = el("button", "btn btn-primary", "Claim hostname") as HTMLButtonElement;
    claimBtn.onclick = () => openClaimDialog(zone.fqdn);
    head.append(claimBtn);
    box.append(head);
    for (const child of zone.children ?? []) box.append(renderNode(child));
    if (!zone.children?.length) {
      box.append(el("div", "node-row muted", "No hostnames claimed in this zone yet."));
    }
    view.append(box);
  }
}

function renderNode(n: TreeNode): HTMLElement {
  const wrap = el("div", "node");
  const row = el("div", "node-row");
  row.append(el("span", `blob blob-${n.state ?? "active"}`));

  const fqdn = el("span", "fqdn");
  if (n.wildcard && !n.label.startsWith("*.")) {
    fqdn.append(n.fqdn, " ");
    fqdn.append(el("span", "wild", "+ *"));
  } else if (n.label.startsWith("*.")) {
    const star = el("span", "wild", "*.");
    fqdn.append(star, n.fqdn.replace(/^\*\./, ""));
  } else {
    fqdn.textContent = n.fqdn;
  }
  row.append(fqdn);

  if (n.kind && n.kind !== "zone") row.append(el("span", `badge badge-${n.kind}`, n.kind));
  if (n.routes?.length) row.append(el("span", "routes", n.routes.join("  ·  ")));
  if (n.owner) row.append(el("span", "owner", n.owner));

  const actions = el("span", "node-actions");
  if (!n.label.startsWith("*.")) {
    const open = el("button", "btn btn-quiet", "Open") as HTMLButtonElement;
    open.onclick = () => OpenURL(`https://${n.fqdn.replace(/^\*\./, "")}`);
    actions.append(open);
  }
  if (n.id) {
    const ren = el("button", "btn btn-quiet", "Rename") as HTMLButtonElement;
    ren.onclick = async () => {
      const base = n.label.startsWith("*.") ? n.label.slice(2) : n.label;
      const next = await ask({
        title: `Rename ${n.fqdn}`,
        body: "The allocation keeps its owner, routes, and history. Systems that store this hostname themselves (app database, bookmarks) are not updated.",
        input: { value: base === "@" ? "" : base, placeholder: "new-label" },
        okLabel: "Rename",
      });
      if (!next || next === base) return;
      try {
        await Rename(n.id!, next);
        render();
      } catch (e) {
        await notify("Rename failed", String(e));
      }
    };
    actions.append(ren);
    const rel = el("button", "btn btn-quiet btn-danger", "Release") as HTMLButtonElement;
    rel.onclick = async () => {
      if (!(await confirmAsk(`Release ${n.fqdn}?`, "Traffic to this hostname stops immediately and the label becomes claimable by anyone.", "Release"))) return;
      try { await Release(n.id!); render(); } catch (e) { alertError(String(e)); }
    };
    actions.append(rel);
  }
  row.append(actions);
  wrap.append(row);
  for (const c of n.children ?? []) wrap.append(renderNode(c));
  return wrap;
}

/* ---------- claim dialog ---------- */

const claimDialog = $("#claim-dialog") as HTMLDialogElement;
const claimForm = $("#claim-form") as HTMLFormElement;
let availTimer: number | undefined;

function openClaimDialog(zone: string) {
  claimForm.reset();
  (claimForm.elements.namedItem("zone") as HTMLInputElement).value = zone;
  $("#avail-hint").textContent = "";
  $("#claim-error").textContent = "";
  claimDialog.showModal();
}

claimForm.addEventListener("input", (ev) => {
  const target = ev.target as HTMLInputElement;
  if (target.name !== "label") return;
  window.clearTimeout(availTimer);
  availTimer = window.setTimeout(async () => {
    const zone = (claimForm.elements.namedItem("zone") as HTMLInputElement).value;
    const label = target.value.trim();
    const hint = $("#avail-hint");
    if (!label) { hint.textContent = ""; return; }
    try {
      const res = await Availability(zone, label);
      if (res.available) {
        hint.className = "hint ok";
        hint.textContent = `${label}.${zone} is free`;
      } else {
        hint.className = "hint bad";
        let msg = res.reason as string;
        if (res.suggestions?.length) msg += ` — try ${res.suggestions.join(", ")}`;
        hint.textContent = msg;
      }
    } catch { hint.textContent = ""; }
  }, 250);
});

claimForm.addEventListener("submit", async (ev) => {
  const submitter = (ev as SubmitEvent).submitter as HTMLButtonElement | null;
  if (submitter?.value !== "claim") return;
  ev.preventDefault();
  const f = new FormData(claimForm);
  const listenRaw = String(f.get("listen") ?? "").trim();
  const listen = listenRaw
    ? [0, ...listenRaw.split(",").map((s) => parseInt(s.trim(), 10)).filter((n) => n > 0)]
    : [];
  try {
    await Claim({
      zone: String(f.get("zone")),
      label: String(f.get("label")).trim(),
      kind: "platform",
      wildcard: f.get("wildcard") === "on",
      backend: String(f.get("backend") ?? "").trim(),
      listen,
      owner: "desktop",
    } as any);
    claimDialog.close();
    render();
  } catch (e) {
    $("#claim-error").textContent = String(e);
  }
});

/* ---------- projects ---------- */

async function renderProjects() {
  if (!apiUp) {
    renderEmpty("The registry is offline", "Start the local daemon to see your projects.");
    return;
  }
  let projects: any[];
  try {
    projects = (await GetProjects()) ?? [];
  } catch (e) {
    renderEmpty("Could not load projects", String(e));
    return;
  }
  view.replaceChildren();
  if (!projects.length) {
    renderEmpty(
      "No projects yet",
      "A project appears here when a gerrymander.yaml is applied — by `gerry up`, or automatically when a dev server using @gerrymander/vite starts.",
    );
    return;
  }
  for (const p of projects) {
    const box = el("div", "zone");
    const head = el("div", "zone-head");
    head.append(el("span", "zone-name", p.name), el("span", "zone-profile", p.zone));
    box.append(head);
    for (const s of p.services ?? []) {
      const row = el("div", "node-row");
      row.append(el("span", `blob blob-${s.state ?? "active"}`));
      row.append(el("span", "cmd", s.name));
      const hosts = el("span", "fqdn");
      hosts.textContent = (s.hostnames ?? []).join("  ·  ");
      row.append(hosts);
      if (s.routes?.length) row.append(el("span", "routes", s.routes.join("  ·  ")));
      if (s.port) {
        const port = el("span", "tag-gerry", `port ${s.port}`);
        row.append(port);
      }
      const actions = el("span", "node-actions");
      const primary = (s.hostnames ?? [])[0];
      if (primary) {
        const open = el("button", "btn btn-quiet", "Open") as HTMLButtonElement;
        open.onclick = () => OpenURL(`https://${primary.replace(/^\*\./, "")}`);
        actions.append(open);
      }
      row.append(actions);
      box.append(row);
    }
    view.append(box);
  }
}

/* ---------- ports ---------- */

async function renderPorts() {
  let data: any;
  try {
    data = await GetPorts();
  } catch (e) {
    renderEmpty("Could not scan ports", String(e));
    return;
  }
  view.replaceChildren();

  view.append(el("h3", "section-title", "Listening now"));
  const table = el("table", "table") as HTMLTableElement;
  table.innerHTML = `<thead><tr>
    <th>Port</th><th>Command</th><th>PID</th><th>User</th><th>Bound to</th><th>Registry</th><th></th>
  </tr></thead>`;
  const tbody = el("tbody");
  for (const l of data.listeners ?? []) {
    const tr = el("tr") as HTMLTableRowElement;
    tr.append(
      td(String(l.port)),
      tdCls(l.command, "cmd"),
      td(String(l.pid)),
      tdCls(l.user, "muted"),
      tdCls(l.addr, "muted"),
      tdCls(l.gerry_owner ? `⌘ ${l.gerry_owner}` : "", l.gerry_owner ? "tag-gerry" : "muted"),
    );
    const actions = el("td");
    const kill = el("button", "btn btn-quiet btn-danger", "Kill") as HTMLButtonElement;
    kill.title = "SIGTERM, then SIGKILL after 2s. Hold ⌥ for immediate SIGKILL.";
    kill.onclick = async (ev) => {
      const force = (ev as MouseEvent).altKey;
      const how = force ? "SIGKILL immediately" : "SIGTERM first, SIGKILL after 2s";
      if (!(await confirmAsk(`${force ? "Force kill" : "Kill"} ${l.command}?`, `pid ${l.pid} on port ${l.port} — ${how}.`, force ? "Force kill" : "Kill"))) return;
      kill.disabled = true;
      try { await KillPort(l.pid, force); } catch (e) { alertError(String(e)); }
      setTimeout(render, 500);
    };
    actions.append(kill);
    tr.append(actions);
    tbody.append(tr);
  }
  table.append(tbody);
  view.append(table);

  view.append(el("h3", "section-title", "Registry grants"));
  const grants = el("table", "table") as HTMLTableElement;
  grants.innerHTML = `<thead><tr><th>Port</th><th>Pool</th><th>Owner</th><th>State</th></tr></thead>`;
  const gbody = el("tbody");
  const list = data.grants ?? [];
  for (const g of list) {
    const tr = el("tr");
    tr.append(td(String(g.value)), tdCls(g.pool ?? "", "muted"), td(g.owner_ref), tdCls(g.state, g.state === "active" ? "muted" : "tag-gerry"));
    gbody.append(tr);
  }
  if (!list.length) {
    const tr = el("tr");
    tr.append(tdCls("No sticky ports granted yet — claim one with `gerry port --owner myproj` or via MCP.", "muted"));
    gbody.append(tr);
  }
  grants.append(gbody);
  view.append(grants);
}

function td(text: string): HTMLTableCellElement {
  const c = el("td") as HTMLTableCellElement;
  c.textContent = text;
  return c;
}
function tdCls(text: string, cls: string): HTMLTableCellElement {
  const c = td(text);
  c.className = cls;
  return c;
}

/* ---------- processes ---------- */

async function renderProcesses() {
  if (!apiUp) {
    renderEmpty("The registry is offline", "Supervised processes live in the daemon — start it to see them.");
    return;
  }
  let procs: any[];
  try {
    procs = (await GetProcesses()) ?? [];
  } catch (e) {
    renderEmpty("Could not load processes", String(e));
    return;
  }
  view.replaceChildren();
  if (!procs.length) {
    renderEmpty(
      "No supervised services",
      "Declare one in a gerrymander.yaml with a `supervised:` backend and apply it with gerry up. The proxy will boot it on first request and sleep it when idle.",
    );
    return;
  }
  const table = el("table", "table") as HTMLTableElement;
  table.innerHTML = `<thead><tr><th>Service</th><th>State</th><th>Port</th><th>PID</th><th></th></tr></thead>`;
  const tbody = el("tbody");
  for (const p of procs) {
    const tr = el("tr");
    tr.append(tdCls(p.name, "cmd"), td(p.state), td(p.port ? String(p.port) : "—"), td(p.pid ? String(p.pid) : "—"));
    const actions = el("td");
    const isRunning = p.state === "running" || p.state === "starting";
    const toggle = el("button", "btn btn-quiet", isRunning ? "Stop" : "Start") as HTMLButtonElement;
    toggle.onclick = async () => {
      try { await (isRunning ? StopProcess(p.name) : StartProcess(p.name)); } catch (e) { alertError(String(e)); }
      setTimeout(render, 600);
    };
    const logs = el("button", "btn btn-quiet", "Logs") as HTMLButtonElement;
    logs.onclick = () => openLogs(p.name);
    actions.append(toggle, " ", logs);
    tr.append(actions);
    tbody.append(tr);
  }
  table.append(tbody);
  view.append(table);
}

/* ---------- logs dialog ---------- */

const logsDialog = $("#logs-dialog") as HTMLDialogElement;
let logsTimer: number | undefined;

async function openLogs(name: string) {
  $("#logs-title").textContent = `Logs — ${name}`;
  logsDialog.showModal();
  const tick = async () => {
    try {
      const lines = await ProcessLogs(name, 300);
      const body = $("#logs-body");
      body.textContent = (lines ?? []).join("\n") || "(no output captured)";
      body.scrollTop = body.scrollHeight;
    } catch { /* daemon hiccup; keep last */ }
  };
  await tick();
  logsTimer = window.setInterval(tick, 2000);
}

$("#logs-close").addEventListener("click", () => logsDialog.close());
logsDialog.addEventListener("close", () => window.clearInterval(logsTimer));

/* ---------- settings ---------- */

const settingsDialog = $("#settings-dialog") as HTMLDialogElement;
const settingsForm = $("#settings-form") as HTMLFormElement;

$("#settings-btn").addEventListener("click", async () => {
  const s = await GetSettings();
  (settingsForm.elements.namedItem("api") as HTMLInputElement).value = s.api ?? "";
  (settingsForm.elements.namedItem("api_key") as HTMLInputElement).value = s.api_key ?? "";
  (settingsForm.elements.namedItem("compose_dir") as HTMLInputElement).value = s.compose_dir ?? "";
  settingsDialog.showModal();
});

settingsForm.addEventListener("submit", async (ev) => {
  const submitter = (ev as SubmitEvent).submitter as HTMLButtonElement | null;
  if (submitter?.value !== "save") return;
  ev.preventDefault();
  const f = new FormData(settingsForm);
  await SaveSettings({
    api: String(f.get("api")),
    api_key: String(f.get("api_key")),
    compose_dir: String(f.get("compose_dir")),
  } as any);
  settingsDialog.close();
  refreshStatus();
  render();
});

/* ---------- shell ---------- */

function renderEmpty(title: string, body: string) {
  view.replaceChildren();
  const box = el("div", "empty");
  box.append(el("h3", "", title), el("p", "", body));
  view.append(box);
}

function render() {
  switch (currentView) {
    case "map":
      if (apiUp) renderMap(view, renderEmpty);
      else renderEmpty("The registry is offline", "Start the local daemon to draw the district map.");
      break;
    case "districts": renderDistricts(); break;
    case "projects": renderProjects(); break;
    case "ports": renderPorts(); break;
    case "processes": renderProcesses(); break;
  }
}

document.querySelectorAll<HTMLButtonElement>(".tab").forEach((tab) => {
  tab.addEventListener("click", () => {
    document.querySelectorAll(".tab").forEach((t) => t.classList.remove("active"));
    tab.classList.add("active");
    currentView = tab.dataset.view as View;
    render();
  });
});

refreshStatus().then(render);
window.setInterval(refreshStatus, 3000);
window.setInterval(() => {
  // Passive refresh only for data views; dialogs left alone.
  const askDialog = document.getElementById("ask-dialog") as HTMLDialogElement;
  if (!claimDialog.open && !settingsDialog.open && !logsDialog.open && !askDialog.open) render();
}, 5000);
