const token = new URLSearchParams(location.search).get("token") || "";
const viewport = document.getElementById("viewport");
const world = document.getElementById("world");
const edges = document.getElementById("edges");
const nodes = document.getElementById("nodes");
const details = document.getElementById("detail");
const detailBody = document.getElementById("detail-body");
const sessionList = document.getElementById("session-list");
const notice = document.getElementById("notice");
const empty = document.getElementById("empty");

const state = { runs: [], session: "", selected: "", autoSelect: true, scale: 1, x: 0, y: 0, width: 0, height: 0, drag: null };
const icons = { codex: "/assets/openai.png", claude: "/assets/claude.png", fallback: "/assets/orca.png" };

function el(tag, className = "", text = "") {
  const item = document.createElement(tag);
  if (className) item.className = className;
  if (text) item.textContent = text;
  return item;
}

function nameFor(source) {
  return source === "codex" ? "Codex" : source === "claude" ? "Claude Code" : "Agent session";
}

function iconFor(source) { return icons[source] || icons.fallback; }
function shortID(id) { return id.length > 12 ? id.slice(-8) : id; }
function titleFor(run) { return run.task || run.role || (run.worker === "claude" ? "Claude Code run" : "Codex run"); }
function statusFor(run) {
  const label = run.state === "done" ? "finished" : run.state;
  return run.unread ? `${label}, unread` : label;
}
function timeFor(ms) {
  const seconds = Math.max(0, Math.floor(ms / 1000));
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ${String(seconds % 60).padStart(2, "0")}s`;
  return `${Math.floor(minutes / 60)}h ${String(minutes % 60).padStart(2, "0")}m`;
}

function sessionKey(run) { return `${run.root_source}:${run.root_id}`; }
function sessions() {
  const map = new Map();
  for (const run of state.runs) {
    const key = sessionKey(run);
    const current = map.get(key);
    if (!current || run.created_at > current.latest) map.set(key, { key, source: run.root_source, id: run.root_id, latest: run.created_at });
  }
  return [...map.values()].sort((a, b) => b.latest.localeCompare(a.latest));
}

function showNotice(message, persistent = false) {
  if (!notice.hidden && notice.textContent === message && notice.dataset.persistent === String(persistent)) return;
  notice.textContent = message;
  notice.hidden = false;
  notice.dataset.persistent = String(persistent);
  clearTimeout(showNotice.timer);
  if (!persistent) showNotice.timer = setTimeout(() => { notice.hidden = true; }, 4000);
}

async function api(path, body) {
  const options = { headers: { "X-Orca-Token": token } };
  if (body) {
    options.method = "POST";
    options.headers["Content-Type"] = "application/json";
    options.body = JSON.stringify(body);
  }
  const response = await fetch(path, options);
  if (!response.ok) throw new Error(`${response.status} ${response.statusText}`);
  return response.json();
}

async function refresh() {
  try {
    const fresh = await api("/api/runs");
    state.runs = Array.isArray(fresh) ? fresh : [];
    if (notice.dataset.persistent === "true") notice.hidden = true;
    render();
  } catch (error) {
    showNotice(error instanceof TypeError ? "View disconnected. Run orca ui again." : "Could not load runs.", true);
  }
}

function renderSessions(list) {
  sessionList.replaceChildren();
  if (!list.length) {
    sessionList.append(el("p", "session-empty", "No sessions"));
    return;
  }
  for (const session of list) {
    const row = el("button", `session-row${state.session === session.key ? " selected" : ""}`);
    row.type = "button";
    row.setAttribute("aria-current", state.session === session.key ? "true" : "false");
    const icon = el("img");
    icon.src = iconFor(session.source);
    icon.alt = "";
    const copy = el("span");
    copy.append(el("strong", "", nameFor(session.source)), el("small", "", shortID(session.id)));
    row.append(icon, copy);
    row.addEventListener("click", () => {
      if (state.session === session.key) return;
      state.session = session.key;
      state.selected = "";
      state.autoSelect = true;
      render(true);
    });
    sessionList.append(row);
  }
}

function makeTree(runs) {
  const root = { id: "root", children: [], data: null };
  const map = new Map(runs.map(run => [run.id, { id: run.id, children: [], data: run }]));
  for (const run of runs) {
    const parentID = run.parent_id || run.previous_id;
    const parent = map.get(parentID);
    const before = parent && parent.data.created_at < run.created_at;
    (before ? parent : root).children.push(map.get(run.id));
  }
  const sortChildren = node => {
    node.children.sort((a, b) => a.data.created_at.localeCompare(b.data.created_at));
    node.children.forEach(sortChildren);
  };
  root.children.sort((a, b) => a.data.created_at.localeCompare(b.data.created_at));
  root.children.forEach(sortChildren);
  return root;
}

function layoutTree(root) {
  const stepY = 170, top = 135, side = 36, gap = 24;
  const baseWidth = Math.max(880, root.children.length * 270 + 70);
  const rows = [];
  function place(node, center, depth) {
    node.depth = depth;
    node.width = depth === 0 ? 270 : depth === 1 ? 250 : 210;
    node.height = depth === 0 ? 98 : depth === 1 ? 92 : 90;
    node.x = center - node.width / 2;
    node.y = top + depth * stepY;
    (rows[depth] ||= []).push(node);
    const spacing = depth === 0 ? 270 : 195;
    const offset = depth > 0 && node.children.length === 1 ? 50 : 0;
    node.children.forEach((child, index) => {
      place(child, center + (index - (node.children.length - 1) / 2) * spacing + offset, depth + 1);
    });
  }
  function shift(node, distance) {
    node.x += distance;
    node.children.forEach(child => shift(child, distance));
  }
  place(root, baseWidth / 2, 0);
  for (const row of rows) {
    row.sort((a, b) => a.x - b.x);
    let right = -Infinity;
    for (const node of row) {
      if (node.x < right + gap) shift(node, right + gap - node.x);
      right = node.x + node.width;
    }
  }
  const placed = rows.flat();
  const left = Math.min(...placed.map(node => node.x));
  if (left < side) placed.forEach(node => { node.x += side - left; });
  const right = Math.max(...placed.map(node => node.x + node.width));
  return { width: Math.max(baseWidth, right + side), height: top + (rows.length - 1) * stepY + 110 };
}

function card(node, session, runs) {
  const root = node.data === null;
  const data = node.data;
  const selected = !root && state.selected === data.id;
  const button = el(root ? "div" : "button", `node${root ? " root" : node.depth > 1 ? " depth-deep" : ""}${selected ? " selected" : ""}${!root && data.unread ? " unread" : ""}`);
  if (!root) button.type = "button";
  button.style.left = `${node.x}px`;
  button.style.top = `${node.y}px`;
  if (!root) button.dataset.runId = data.id;
  const head = el("div", "node-head");
  const icon = el("img");
  icon.src = root ? iconFor(session.source) : iconFor(data.worker);
  icon.alt = "";
  const copy = el("div");
  copy.style.minWidth = "0";
  copy.append(el("div", "node-title", root ? nameFor(session.source) : titleFor(data)));
  if (!root) {
    const meta = [data.role, data.model || nameFor(data.worker)].filter(Boolean).join(" / ");
    copy.append(el("div", "node-meta", meta));
  }
  head.append(icon, copy);
  button.append(head);
  const bottom = el("div", "node-bottom");
  if (root) {
    const active = runs.filter(r => ["starting", "running", "stopping"].includes(r.state)).length;
    const finished = runs.length - active;
    const counts = [];
    if (active) counts.push(`${active} running`);
    if (finished) counts.push(`${finished} finished`);
    bottom.append(el("span", "node-status", counts.join(", ")));
  } else {
    const status = el("span", `node-status${data.unread ? " unread" : ["starting", "running", "stopping"].includes(data.state) ? " running" : ""}`, statusFor(data));
    bottom.append(status, el("span", "node-time", timeFor(data.duration_ms)));
    button.setAttribute("aria-label", `${titleFor(data)}, ${statusFor(data)}, ${nameFor(data.worker)}`);
    button.addEventListener("keydown", event => {
      if (event.key === "Enter" || event.key === " ") {
        event.preventDefault();
        state.selected = data.id;
        state.autoSelect = false;
        renderDetails();
        renderNodesOnly();
      }
    });
  }
  button.append(bottom);
  return button;
}

function drawTree(root, session, runs) {
  const size = layoutTree(root);
  state.width = size.width;
  state.height = size.height;
  world.style.width = `${size.width}px`;
  world.style.height = `${size.height}px`;
  edges.setAttribute("viewBox", `0 0 ${size.width} ${size.height}`);
  edges.replaceChildren();
  nodes.replaceChildren();
  function draw(node) {
    nodes.append(card(node, session, runs));
    for (const child of node.children) {
      const fromX = node.x + node.width / 2;
      const toX = child.x + child.width / 2;
      const fromY = node.y + node.height;
      const toY = child.y;
      const mid = (fromY + toY) / 2;
      const path = document.createElementNS("http://www.w3.org/2000/svg", "path");
      path.setAttribute("d", `M ${fromX} ${fromY} C ${fromX} ${mid}, ${toX} ${mid}, ${toX} ${toY}`);
      edges.append(path);
      draw(child);
    }
  }
  draw(root);
}

function renderNodesOnly() {
  for (const node of nodes.querySelectorAll("[data-run-id]")) {
    node.classList.toggle("selected", node.dataset.runId === state.selected);
  }
}

function detailRow(list, label, value) {
  if (!value) return;
  list.append(el("dt", "", label), el("dd", "", value));
}

function renderDetails() {
  const run = state.runs.find(item => item.id === state.selected && sessionKey(item) === state.session);
  details.hidden = !run;
  detailBody.replaceChildren();
  if (!run) return;
  const head = el("div", "detail-head");
  const icon = el("img");
  icon.src = iconFor(run.worker);
  icon.alt = "";
  const copy = el("div");
  copy.append(el("h2", "", titleFor(run)), el("small", "", `Run ${shortID(run.id)}`));
  head.append(icon, copy);
  detailBody.append(head);
  const list = el("dl");
  detailRow(list, "Task", run.task);
  detailRow(list, "Role", run.role);
  detailRow(list, "Worker", nameFor(run.worker));
  detailRow(list, "Requested model", run.model);
  detailRow(list, "Mode", run.mode);
  detailRow(list, "Directory", run.cwd);
  detailRow(list, "Elapsed time", timeFor(run.duration_ms));
  detailRow(list, "Outcome", run.error);
  detailBody.append(list);
  const actions = el("div", "detail-actions");
  if (!["done", "failed", "stopped", "interrupted"].includes(run.state)) {
    const stop = el("button", "stop", "Stop run");
    stop.type = "button";
    stop.addEventListener("click", async () => {
      if (!confirm("Stop this run? Its child runs will keep going.")) return;
      try { await api("/api/stop", { id: run.id }); await refresh(); }
      catch { showNotice("Could not stop run"); }
    });
    actions.append(stop);
  }
  const clear = el("button", "clear", "Clear from view");
  clear.type = "button";
  clear.addEventListener("click", async () => {
    try { await api("/api/clear", { id: run.id }); state.selected = ""; await refresh(); }
    catch { showNotice("Could not clear run"); }
  });
  actions.append(clear);
  detailBody.append(actions);
}

function applyTransform() { world.style.transform = `translate(${state.x}px, ${state.y}px) scale(${state.scale})`; }
function fit() {
  if (!state.width || !state.height) return;
  const bounds = viewport.getBoundingClientRect();
  state.scale = Math.min(1, (bounds.width - 60) / state.width, (bounds.height - 120) / state.height);
  state.x = (bounds.width - state.width * state.scale) / 2;
  state.y = 40;
  applyTransform();
}
function zoom(factor, clientX, clientY) {
  const box = viewport.getBoundingClientRect();
  const px = clientX - box.left, py = clientY - box.top;
  const next = Math.min(2, Math.max(Math.min(.35, state.scale), state.scale * factor));
  const wx = (px - state.x) / state.scale, wy = (py - state.y) / state.scale;
  state.scale = next;
  state.x = px - wx * next;
  state.y = py - wy * next;
  applyTransform();
}

function render(forceFit = false) {
  const list = sessions();
  const prior = state.session;
  if (!list.some(item => item.key === state.session)) {
    state.session = list[0]?.key || "";
    state.selected = "";
    state.autoSelect = true;
  }
  renderSessions(list);
  const session = list.find(item => item.key === state.session);
  const runs = state.runs.filter(run => sessionKey(run) === state.session);
  empty.hidden = runs.length > 0;
  document.getElementById("clear-finished").hidden = !state.runs.some(run => ["done", "failed", "stopped", "interrupted"].includes(run.state));
  if (!session || !runs.length) {
    nodes.replaceChildren(); edges.replaceChildren(); state.width = 0; state.height = 0;
    state.selected = ""; renderDetails(); return;
  }
  if (!runs.some(run => run.id === state.selected) && state.autoSelect) {
    state.selected = runs.find(run => ["starting", "running", "stopping"].includes(run.state))?.id || runs[0].id;
    state.autoSelect = false;
  }
  if (!runs.some(run => run.id === state.selected)) state.selected = "";
  drawTree(makeTree(runs), session, runs);
  renderDetails();
  if (forceFit || prior !== state.session || !state.scale || !state.x && !state.y) fit();
  else applyTransform();
}

viewport.addEventListener("pointerdown", event => {
  if (event.button !== 0) return;
  state.drag = { pointer: event.pointerId, startX: event.clientX, startY: event.clientY,
    baseX: state.x, baseY: state.y, moved: false,
    runID: event.target.closest("[data-run-id]")?.dataset.runId || "" };
  viewport.setPointerCapture(event.pointerId);
});
viewport.addEventListener("pointermove", event => {
  const drag = state.drag;
  if (!drag || drag.pointer !== event.pointerId) return;
  const dx = event.clientX - drag.startX, dy = event.clientY - drag.startY;
  if (Math.hypot(dx, dy) > 5) drag.moved = true;
  if (drag.moved) {
    viewport.classList.add("dragging");
    state.x = drag.baseX + dx; state.y = drag.baseY + dy;
    applyTransform();
  }
});
function endPointer(event) {
  const drag = state.drag;
  if (!drag || drag.pointer !== event.pointerId) return;
  if (event.type === "pointerup" && !drag.moved && drag.runID) {
    state.selected = drag.runID;
    state.autoSelect = false;
    renderNodesOnly();
    renderDetails();
  }
  state.drag = null;
  viewport.classList.remove("dragging");
  if (viewport.hasPointerCapture(event.pointerId)) viewport.releasePointerCapture(event.pointerId);
}
viewport.addEventListener("pointerup", endPointer);
viewport.addEventListener("pointercancel", endPointer);
viewport.addEventListener("wheel", event => {
  event.preventDefault();
  zoom(Math.exp(-event.deltaY * .0012), event.clientX, event.clientY);
}, { passive: false });
viewport.addEventListener("keydown", event => {
  if (event.target !== viewport) return;
  if (event.key === "+" || event.key === "=") zoom(1.15, viewport.getBoundingClientRect().left + viewport.clientWidth / 2, viewport.getBoundingClientRect().top + viewport.clientHeight / 2);
  else if (event.key === "-") zoom(1 / 1.15, viewport.getBoundingClientRect().left + viewport.clientWidth / 2, viewport.getBoundingClientRect().top + viewport.clientHeight / 2);
  else if (event.key === "0") fit();
  else if (event.key.startsWith("Arrow")) {
    state.x += event.key === "ArrowRight" ? -50 : event.key === "ArrowLeft" ? 50 : 0;
    state.y += event.key === "ArrowDown" ? -50 : event.key === "ArrowUp" ? 50 : 0;
    applyTransform();
  } else return;
  event.preventDefault();
});
document.addEventListener("keydown", event => {
  if (event.key === "Escape" && state.selected) {
    state.selected = ""; state.autoSelect = false; renderNodesOnly(); renderDetails();
    viewport.focus();
  }
});
document.getElementById("detail-close").addEventListener("click", () => {
  state.selected = ""; state.autoSelect = false; renderNodesOnly(); renderDetails(); viewport.focus();
});
document.getElementById("zoom-in").addEventListener("click", () => zoom(1.2, viewport.getBoundingClientRect().left + viewport.clientWidth / 2, viewport.getBoundingClientRect().top + viewport.clientHeight / 2));
document.getElementById("zoom-out").addEventListener("click", () => zoom(1 / 1.2, viewport.getBoundingClientRect().left + viewport.clientWidth / 2, viewport.getBoundingClientRect().top + viewport.clientHeight / 2));
document.getElementById("zoom-fit").addEventListener("click", fit);
document.getElementById("clear-finished").addEventListener("click", async () => {
  try { await api("/api/clear", { finished: true }); state.selected = ""; await refresh(); }
  catch { showNotice("Could not clear finished runs"); }
});
const toggle = document.getElementById("theme-toggle");
function setTheme(theme) {
  document.documentElement.dataset.theme = theme;
  toggle.setAttribute("aria-pressed", theme === "dark" ? "true" : "false");
  toggle.setAttribute("aria-label", `Switch to ${theme === "dark" ? "light" : "dark"} mode`);
  localStorage.setItem("orca-theme", theme);
}
toggle.addEventListener("click", () => setTheme(document.documentElement.dataset.theme === "dark" ? "light" : "dark"));
setTheme(localStorage.getItem("orca-theme") || (matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light"));
window.addEventListener("resize", () => { if (state.width) fit(); });
refresh();
setInterval(() => { if (!state.drag && document.visibilityState === "visible") refresh(); }, 2000);
