// The live dashboard: one page per slice, fed by the slice Hub's WebSocket.

export interface DashboardOptions {
  account: string;
  slice: string;
  gitsliceWeb: string;
  gitsliceGit: string;
}

export function dashboardHtml(o: DashboardOptions): string {
  const config = JSON.stringify(o).replace(/</g, "\\u003c");
  return `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Gitslice agents · ${escapeHtml(o.account)}/${escapeHtml(o.slice)}</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&family=JetBrains+Mono:wght@400;500&display=swap" rel="stylesheet">
<style>
:root {
  --bg: #0b0d12; --panel: #12151d; --panel2: #171b25; --line: #232837; --text: #e7eaf2; --muted: #8a93a8; --dim: #5b6377;
  --forked: #64748b; --pushed: #6366f1; --reviewing: #a855f7; --approved: #14b8a6; --landing: #06b6d4; --merging: #f59e0b;
  --landed: #22c55e; --conflict: #ef4444; --human: #f97316; --changes: #f43f5e; --failed: #b91c1c; --accent: #f6821f;
}
* { box-sizing: border-box; }
html, body { margin: 0; background: var(--bg); color: var(--text); font: 14px/1.4 Inter, system-ui, sans-serif; }
body { height: 100vh; overflow: hidden; display: flex; flex-direction: column; }
header { display: flex; align-items: center; gap: 18px; padding: 16px 28px; border-bottom: 1px solid var(--line); background: linear-gradient(180deg, #10131b, #0b0d12); }
.brand { display: flex; align-items: center; gap: 10px; font-weight: 700; font-size: 18px; letter-spacing: -0.01em; }
.brand .x { color: var(--dim); font-weight: 500; }
.brand .cf { color: var(--accent); }
.slice { font-family: "JetBrains Mono", monospace; font-size: 13px; color: var(--muted); padding: 4px 10px; border: 1px solid var(--line); border-radius: 999px; }
.live { display: flex; align-items: center; gap: 6px; color: var(--muted); font-size: 12px; margin-left: auto; }
.dot { width: 8px; height: 8px; border-radius: 50%; background: var(--dim); }
.live.on .dot { background: var(--landed); box-shadow: 0 0 0 4px rgba(34,197,94,.15); animation: pulse 2s infinite; }
@keyframes pulse { 50% { box-shadow: 0 0 0 7px rgba(34,197,94,.05); } }
.baseline { font-family: "JetBrains Mono", monospace; font-size: 12px; color: var(--dim); }
.kpis { display: grid; grid-template-columns: repeat(10, 1fr); gap: 1px; background: var(--line); border-bottom: 1px solid var(--line); }
.kpi { background: var(--panel); padding: 14px 18px; }
.kpi .v { font-size: 28px; font-weight: 700; letter-spacing: -0.02em; font-variant-numeric: tabular-nums; }
.kpi .l { color: var(--muted); font-size: 11.5px; text-transform: uppercase; letter-spacing: .06em; margin-top: 2px; }
.kpi.good .v { color: var(--landed); } .kpi.warn .v { color: var(--merging); } .kpi.bad .v { color: var(--conflict); } .kpi.human .v { color: var(--human); }
.bar { display: flex; height: 6px; background: var(--panel2); }
.bar span { transition: flex-grow .4s ease; }
main { flex: 1; display: grid; grid-template-columns: minmax(0, 1.55fr) minmax(0, 1fr); min-height: 0; overflow: hidden; }
section { padding: 18px 24px; min-height: 0; display: flex; flex-direction: column; overflow: hidden; }
section + section { border-left: 1px solid var(--line); }
h2 { margin: 0 0 12px; font-size: 12px; text-transform: uppercase; letter-spacing: .08em; color: var(--muted); font-weight: 600; display: flex; align-items: center; gap: 8px; }
h2 .count { color: var(--dim); }
.legend { display: flex; flex-wrap: wrap; gap: 10px 14px; margin: -4px 0 12px; color: var(--muted); font-size: 11.5px; }
.legend i { display: inline-block; width: 8px; height: 8px; border-radius: 2px; margin-right: 5px; vertical-align: middle; }
.grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(196px, 1fr)); gap: 8px; overflow: auto; align-content: start; }
.card { background: var(--panel); border: 1px solid var(--line); border-left: 3px solid var(--forked); border-radius: 8px; padding: 9px 10px; cursor: pointer; transition: transform .15s, border-color .3s, background .3s; }
.card:hover { transform: translateY(-1px); background: var(--panel2); }
.card.flash { animation: flash .9s ease; }
@keyframes flash { 0% { background: #22283a; } 100% { background: var(--panel); } }
.card .top { display: flex; align-items: center; gap: 6px; }
.card .agent { font-weight: 600; font-size: 12.5px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.card .pill { margin-left: auto; font-size: 10px; text-transform: uppercase; letter-spacing: .05em; padding: 2px 6px; border-radius: 4px; font-weight: 600; white-space: nowrap; }
.card .task { color: var(--muted); font-size: 11.5px; margin-top: 4px; height: 31px; overflow: hidden; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; }
.card .path { font-family: "JetBrains Mono", monospace; font-size: 10.5px; color: var(--dim); margin-top: 5px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.card .meta { display: flex; flex-wrap: wrap; gap: 4px 6px; margin-top: 5px; font-size: 10.5px; color: var(--dim); }
.card .meta .tag { padding: 0 5px; border-radius: 3px; background: var(--panel2); border: 1px solid var(--line); white-space: nowrap; }
.grid.compact { grid-template-columns: repeat(auto-fill, minmax(170px, 1fr)); gap: 6px; }
.grid.compact .card { padding: 6px 8px; border-radius: 6px; }
.grid.compact .task, .grid.compact .meta { display: none; }
.grid.compact .path { margin-top: 2px; }
.grid.dense { grid-template-columns: repeat(auto-fill, minmax(146px, 1fr)); gap: 5px; }
.grid.dense .card { padding: 5px 7px; }
.grid.dense .pill { font-size: 9px; padding: 1px 4px; }
.files { overflow: auto; flex: 1; min-height: 0; }
.file { display: flex; align-items: center; gap: 8px; padding: 6px 8px; border-radius: 6px; font-family: "JetBrains Mono", monospace; font-size: 12px; }
.file:nth-child(odd) { background: rgba(255,255,255,.015); }
.file .name { flex: 1; min-width: 0; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; color: var(--text); }
.file.shared .name { color: var(--merging); }
.file .who { display: flex; gap: 3px; }
.file .who b { width: 9px; height: 9px; border-radius: 2px; display: inline-block; }
.file .n { color: var(--dim); font-size: 11px; width: 52px; text-align: right; }
.feed { margin-top: 16px; flex: 0 0 34%; overflow: hidden; display: flex; flex-direction: column; }
.events { overflow: hidden; flex: 1; font-size: 12px; }
.ev { display: flex; gap: 10px; padding: 4px 0; border-bottom: 1px dashed #1c2130; animation: slide .4s ease; }
@keyframes slide { from { opacity: 0; transform: translateY(-6px); } }
.ev time { color: var(--dim); font-family: "JetBrains Mono", monospace; font-size: 11px; flex: 0 0 64px; }
.ev .who { color: var(--text); font-weight: 600; flex: 0 0 auto; }
.ev .what { color: var(--muted); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.ev.landed .what { color: var(--landed); } .ev.conflict .what, .ev.failed .what { color: var(--conflict); }
.ev.merging .what, .ev.auto-merged .what, .ev.overlap .what { color: var(--merging); } .ev.needs-human .what { color: var(--human); }
footer { padding: 10px 28px; border-top: 1px solid var(--line); color: var(--dim); font-size: 12px; display: flex; gap: 18px; }
footer b { color: var(--muted); font-weight: 500; }
#drawer { position: fixed; top: 0; right: 0; bottom: 0; width: 560px; background: #0f121a; border-left: 1px solid var(--line); transform: translateX(100%); transition: transform .25s ease; padding: 24px; overflow: auto; box-shadow: -20px 0 40px rgba(0,0,0,.4); z-index: 5; }
#drawer.open { transform: none; }
#drawer h3 { margin: 0 0 4px; font-size: 18px; }
#drawer .sub { color: var(--muted); margin-bottom: 14px; }
#drawer .kv { display: grid; grid-template-columns: 120px 1fr; gap: 6px 12px; font-size: 12.5px; margin-bottom: 16px; }
#drawer .kv div:nth-child(odd) { color: var(--dim); }
#drawer .mono { font-family: "JetBrains Mono", monospace; font-size: 12px; word-break: break-all; }
#drawer a { color: #8ab4ff; text-decoration: none; }
.sig { border: 1px solid var(--line); border-left: 3px solid var(--dim); border-radius: 6px; padding: 8px 10px; margin-bottom: 8px; background: var(--panel); font-size: 12.5px; }
.sig .k { font-size: 10px; text-transform: uppercase; letter-spacing: .06em; color: var(--muted); margin-bottom: 3px; }
.sig.overlap, .sig.merged { border-left-color: var(--merging); } .sig.review { border-left-color: var(--reviewing); }
.hunk { margin-top: 8px; font-family: "JetBrains Mono", monospace; font-size: 11.5px; border: 1px solid var(--line); border-radius: 6px; overflow: hidden; }
.hunk div { padding: 3px 8px; white-space: pre-wrap; word-break: break-all; }
.hunk .h { color: var(--muted); background: var(--panel2); }
.hunk .b { color: var(--muted); } .hunk .o { color: #fca5a5; background: rgba(239,68,68,.08); } .hunk .t { color: #86efac; background: rgba(34,197,94,.08); }
.sig.conflict, .sig.error { border-left-color: var(--conflict); } .sig.landed { border-left-color: var(--landed); } .sig.escalated { border-left-color: var(--human); }
.review { border: 1px solid #2a2340; background: #151225; border-radius: 8px; padding: 12px; margin-bottom: 16px; }
.review .verdict { font-weight: 700; text-transform: uppercase; font-size: 11px; letter-spacing: .06em; }
.close { position: absolute; top: 16px; right: 18px; color: var(--muted); cursor: pointer; font-size: 20px; }
.empty { color: var(--dim); padding: 40px 0; text-align: center; }
</style>
</head>
<body>
<header>
  <div class="brand"><span>gitslice</span><span class="x">×</span><span class="cf">Cloudflare Artifacts</span></div>
  <span class="slice" id="slice"></span>
  <span class="baseline" id="baseline"></span>
  <div class="live" id="live"><span class="dot"></span><span id="liveText">connecting…</span></div>
</header>
<div class="kpis" id="kpis"></div>
<div class="bar" id="bar"></div>
<main>
  <section>
    <h2>Agents <span class="count" id="agentCount"></span></h2>
    <div class="legend" id="legend"></div>
    <div class="grid" id="grid"><div class="empty">Waiting for agents to open sessions…</div></div>
  </section>
  <section>
    <h2>Files in flight <span class="count" id="fileCount"></span></h2>
    <div class="files" id="files"></div>
    <div class="feed">
      <h2>Activity</h2>
      <div class="events" id="events"></div>
    </div>
  </section>
</main>
<footer>
  <span><b>Isolation</b> — each agent works in its own Artifacts repo, forked from the slice baseline</span>
  <span><b>Integration</b> — every push becomes a Gitslice changeset; disjoint paths land without rebases</span>
</footer>
<div id="drawer"><span class="close" id="close">×</span><div id="drawerBody"></div></div>
<script>
const CONFIG = ${config};
const STATUS = {
  forked: { label: "working", color: "var(--forked)" },
  pushed: { label: "pushed", color: "var(--pushed)" },
  reviewing: { label: "review", color: "var(--reviewing)" },
  approved: { label: "approved", color: "var(--approved)" },
  landing: { label: "landing", color: "var(--landing)" },
  merging: { label: "merging", color: "var(--merging)" },
  landed: { label: "landed", color: "var(--landed)" },
  conflict: { label: "conflict", color: "var(--conflict)" },
  "needs-human": { label: "human", color: "var(--human)" },
  "changes-requested": { label: "changes", color: "var(--changes)" },
  failed: { label: "failed", color: "var(--failed)" },
};
const ORDER = ["forked", "pushed", "reviewing", "approved", "landing", "merging", "needs-human", "changes-requested", "conflict", "failed", "landed"];
const sessions = new Map();
let events = [];
let stats = null;
let baseline = null;
let dirty = true;
let open = null;
const flash = new Set();
const $ = (id) => document.getElementById(id);
const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
const short = (p) => String(p).split("/").slice(3).join("/") || p;
const agentColor = (name) => { let h = 0; for (const c of name) h = (h * 31 + c.charCodeAt(0)) >>> 0; return "hsl(" + (h % 360) + " 70% 62%)"; };
const fmtMs = (ms) => ms == null ? "–" : ms < 1000 ? ms + " ms" : (ms / 1000).toFixed(ms < 10000 ? 1 : 0) + " s";

$("slice").textContent = CONFIG.account + "/" + CONFIG.slice;
$("legend").innerHTML = ORDER.map((k) => '<span><i style="background:' + STATUS[k].color + '"></i>' + STATUS[k].label + "</span>").join("");

function connect() {
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  const ws = new WebSocket(proto + "//" + location.host + "/v1/slices/" + CONFIG.account + "/" + CONFIG.slice + "/stream");
  ws.onopen = () => { $("live").classList.add("on"); $("liveText").textContent = "live"; };
  ws.onclose = () => { $("live").classList.remove("on"); $("liveText").textContent = "reconnecting…"; setTimeout(connect, 1500); };
  ws.onmessage = (m) => {
    const msg = JSON.parse(m.data);
    if (msg.type === "snapshot") {
      sessions.clear();
      for (const s of msg.sessions) sessions.set(s.id, s);
      // Merge rather than replace: a reconnect must not blank the feed.
      const seen = new Set(events.map((e) => e.at + "|" + e.message));
      events = events.concat((msg.events || []).filter((e) => !seen.has(e.at + "|" + e.message))).sort((a, b) => a.at - b.at).slice(-200);
      if (!msg.sessions.length) events = msg.events || [];
      stats = msg.stats;
      baseline = msg.baseline;
    } else if (msg.type === "session") {
      const prev = sessions.get(msg.session.id);
      if (!prev || prev.status !== msg.session.status) flash.add(msg.session.id);
      sessions.set(msg.session.id, msg.session);
    } else if (msg.type === "event") {
      events.push(msg.event);
      if (events.length > 200) events = events.slice(-200);
    } else if (msg.type === "stats") {
      stats = msg.stats;
    } else if (msg.type === "baseline") {
      baseline = msg.baseline;
    }
    dirty = true;
  };
}

function render() {
  if (!dirty) return;
  dirty = false;
  const all = [...sessions.values()];
  const live = all.filter((s) => !s.supersededBy);
  const s = stats || {};
  const kpis = [
    ["Agents", s.agents ?? 0, ""],
    ["Artifacts forks", s.forks ?? 0, ""],
    ["Pushes", s.pushes ?? 0, ""],
    ["Changesets", s.changesets ?? 0, ""],
    ["Reviewed by AI", s.reviewed ?? 0, ""],
    ["Landed", s.landed ?? 0, "good"],
    ["Auto-merged", s.autoMerged ?? 0, "warn"],
    ["Conflicts", s.conflicts ?? 0, "bad"],
    ["Needs human", live.filter((x) => x.status === "needs-human").length, "human"],
    ["Push → land", fmtMs(s.medianLandMs), ""],
  ];
  $("kpis").innerHTML = kpis.map(([l, v, c]) => '<div class="kpi ' + c + '"><div class="v">' + esc(v) + '</div><div class="l">' + l + "</div></div>").join("");
  const counts = {};
  for (const x of live) counts[x.status] = (counts[x.status] || 0) + 1;
  $("bar").innerHTML = ORDER.filter((k) => counts[k]).map((k) => '<span style="flex-grow:' + counts[k] + ";background:" + STATUS[k].color + '"></span>').join("");
  if (baseline) $("baseline").textContent = "baseline " + baseline.nativeCommit.slice(7, 19);

  const cards = live.sort((a, b) => a.createdAt - b.createdAt);
  $("agentCount").textContent = cards.length ? cards.length + " sessions" : "";
  $("grid").classList.toggle("compact", cards.length > 30);
  $("grid").classList.toggle("dense", cards.length > 60);
  $("grid").innerHTML = cards.length ? cards.map(card).join("") : '<div class="empty">Waiting for agents to open sessions…</div>';
  for (const id of flash) { const el = document.querySelector('[data-id="' + CSS.escape(id) + '"]'); if (el) el.classList.add("flash"); }
  flash.clear();

  const files = new Map();
  for (const x of live) {
    if (x.status === "landed" || x.status === "failed") continue;
    for (const p of (x.touched.length ? x.touched : x.intent)) {
      if (!files.has(p)) files.set(p, []);
      files.get(p).push(x);
    }
  }
  const rows = [...files.entries()].sort((a, b) => b[1].length - a[1].length || a[0].localeCompare(b[0])).slice(0, 60);
  $("fileCount").textContent = files.size ? files.size + " files" : "";
  $("files").innerHTML = rows.length
    ? rows.map(([p, who]) => '<div class="file' + (who.length > 1 ? " shared" : "") + '"><span class="name">' + esc(short(p)) + '</span><span class="who">' +
        who.slice(0, 8).map((w) => '<b title="' + esc(w.agent) + '" style="background:' + agentColor(w.agent) + '"></b>').join("") + '</span><span class="n">' + (who.length > 1 ? who.length + " agents" : "") + "</span></div>").join("")
    : '<div class="empty">No changes in flight.</div>';

  $("events").innerHTML = events.slice(-40).reverse().map((e) =>
    '<div class="ev ' + esc(e.kind) + '"><time>' + new Date(e.at).toLocaleTimeString([], { hour12: false }) + '</time><span class="who">' + esc(e.agent || "hub") + '</span><span class="what">' + esc(e.message) + "</span></div>").join("");
  if (open) renderDrawer();
}

function card(x) {
  const st = STATUS[x.status] || STATUS.forked;
  const paths = x.touched.length ? x.touched : x.intent;
  const tags = [];
  if (x.handle) tags.push('<span class="tag">' + esc(x.handle.slice(0, 10)) + "</span>");
  if (x.autoMerged) tags.push('<span class="tag" style="color:var(--merging)">merged</span>');
  if (x.resumedFrom) tags.push('<span class="tag" style="color:var(--conflict)">reworked</span>');
  if (x.fixerFor) tags.push('<span class="tag" style="color:var(--human)">fixes ' + esc(x.fixerFor.split(".")[3] || "") + "</span>");
  else if (x.fixedBy) tags.push('<span class="tag" style="color:var(--human)">fixer assigned</span>');
  if (x.review) tags.push('<span class="tag" style="color:var(--reviewing)">AI ' + esc(x.review.verdict.replace("_", " ")) + "</span>");
  return '<div class="card" data-id="' + esc(x.id) + '" style="border-left-color:' + st.color + '" onclick="openDrawer(\\'' + esc(x.id) + '\\')">' +
    '<div class="top"><span class="agent" style="color:' + agentColor(x.agent) + '">' + esc(x.agent) + '</span><span class="pill" style="background:' + st.color + '22;color:' + st.color + '">' + st.label + "</span></div>" +
    '<div class="task">' + esc(x.task) + "</div>" +
    '<div class="path">' + esc(paths.length ? short(paths[0]) + (paths.length > 1 ? " +" + (paths.length - 1) : "") : "–") + "</div>" +
    (tags.length ? '<div class="meta">' + tags.join("") + "</div>" : "") + "</div>";
}

window.openDrawer = (id) => { open = id; $("drawer").classList.add("open"); renderDrawer(); history.replaceState(null, "", "#s=" + id); };
$("close").onclick = () => { open = null; $("drawer").classList.remove("open"); history.replaceState(null, "", location.pathname); };

function renderDrawer() {
  const x = sessions.get(open);
  if (!x) return;
  const st = STATUS[x.status] || STATUS.forked;
  const r = x.review;
  $("drawerBody").innerHTML =
    "<h3 style=\\"color:" + agentColor(x.agent) + "\\">" + esc(x.agent) + "</h3>" +
    '<div class="sub">' + esc(x.task) + "</div>" +
    '<div class="kv">' +
    "<div>Status</div><div style=\\"color:" + st.color + ";font-weight:600\\">" + st.label + "</div>" +
    "<div>Artifacts repo</div><div class=\\"mono\\">" + esc(x.id) + "</div>" +
    "<div>Baseline</div><div class=\\"mono\\">" + esc(x.baseline.repo) + "</div>" +
    "<div>Changeset</div><div>" + (x.handle ? '<a target="_blank" href="' + CONFIG.gitsliceWeb + "/cs/" + esc(x.handle) + '">' + esc(x.handle) + "</a>" : "–") + "</div>" +
    "<div>Pushes</div><div>" + x.pushes + "</div>" +
    "<div>Paths</div><div class=\\"mono\\">" + (x.touched.length ? x.touched : x.intent).map((p) => esc(short(p))).join("<br>") + "</div>" +
    (x.landedCommit ? "<div>Landed as</div><div class=\\"mono\\">" + esc(x.landedCommit.slice(0, 23)) + "…</div>" : "") +
    (x.resumedFrom ? "<div>Reworks</div><div class=\\"mono\\">" + esc(x.resumedFrom) + "</div>" : "") +
    (x.fixerFor ? "<div>Repairs</div><div class=\\"mono\\">" + esc(x.fixerFor) + "<br>(forked from its repository)</div>" : "") +
    (x.fixedBy && x.fixedBy !== "pending" ? "<div>Fixer</div><div class=\\"mono\\">" + esc(x.fixedBy) + "</div>" : "") +
    "</div>" +
    (r ? '<div class="review"><div class="verdict" style="color:' + (r.verdict === "approve" ? "var(--landed)" : r.verdict === "escalate" ? "var(--human)" : "var(--changes)") + '">Review agent · ' + esc(r.verdict.replace("_", " ")) + " · " + esc(r.model) + " · " + fmtMs(r.ms) + "</div><div style=\\"margin-top:6px\\">" + esc(r.summary) + "</div>" +
      (r.concerns && r.concerns.length ? '<ul style="margin:8px 0 0 18px;padding:0;color:var(--muted)">' + r.concerns.map((c) => "<li>" + esc(c) + "</li>").join("") + "</ul>" : "") + "</div>" : "") +
    "<h2>Signals to the agent</h2>" +
    (x.signals.length ? x.signals.slice().reverse().map((g) => '<div class="sig ' + esc(g.kind) + '"><div class="k">' + esc(g.kind) + " · " + new Date(g.at).toLocaleTimeString([], { hour12: false }) + "</div>" + esc(g.message) + hunks(g) + "</div>").join("") : '<div class="empty">None yet.</div>');
}

// hunks shows the overlapping lines a conflict signal carries.
function hunks(g) {
  const list = g.kind === "conflict" && g.data && Array.isArray(g.data.hunks) ? g.data.hunks : [];
  const lines = (label, cls, xs) => (xs || []).map((l) => '<div class="' + cls + '">' + label + esc(String(l).replace(/\\n$/, "")) + "</div>").join("");
  return list.map((h) => '<div class="hunk"><div class="h">' + esc(short(h.path)) + "</div>" + lines("base   ", "b", h.base) + lines("yours  ", "o", h.ours) + lines("landed ", "t", h.theirs) + "</div>").join("");
}

const hash = new URLSearchParams(location.hash.slice(1));
if (hash.get("s")) open = hash.get("s");
connect();
setInterval(render, 150);
setInterval(() => { if (open) $("drawer").classList.add("open"); }, 500);
</script>
</body>
</html>`;
}

function escapeHtml(text: string): string {
  return text.replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]!);
}
