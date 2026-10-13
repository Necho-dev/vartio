// @ts-check
import { $, API, app, state, esc, rate, dur, ago, meter, osLabel, osKey, OS_NAMES, OS_ORDER, virtKey, virtTag, VIRT_NAMES, VIRT_ORDER, ip, api, every } from "./core.js";
import { icon, osIcon } from "./icons.js";
import { latestAgent, outdated, canUpgrade, upgradeAll, upgradeOne, upgradeNotice } from "./upgrade.js";

let latest = "";
/** Refreshes the dashboard data; set while the dashboard is mounted. */
let reload = () => {};

export async function dashboard() {
  app.innerHTML = `
    <section class="stats" id="stats"></section>
    <div class="chips" id="chips"></div>
    <div id="upBanner"></div>
    <section class="card">
      <div class="card-head"><div><h2>全部主机</h2><p>每 ${state.config.intervalSec}s 采样一次，点击行查看详情与历史曲线</p></div>
        <div class="head-side"><select class="m-sort" id="mSort" title="排序">${SORT_NAMES.map(([k, n]) => `<option value="${k}">${n}</option>`).join("")}</select>
        <span class="muted" id="updated"></span></div></div>
      <div class="table-wrap" id="tableWrap"></div>
    </section>`;
  const load = async () => {
    try {
      state.systems = await api("/monitor/systems");
      latest = await latestAgent();
      renderBanner();
      renderStats();
      renderChips();
      renderTable();
      $("#updated").textContent = "更新于 " + new Date().toLocaleTimeString();
    } catch (e) {
      if (e.message !== "unauthorized") $("#updated").textContent = "加载失败：" + e.message;
    }
  };
  const ms = /** @type {HTMLSelectElement} */ ($("#mSort"));
  ms.value = state.sort.key;
  ms.addEventListener("change", () => {
    state.sort = { key: ms.value, dir: ms.value === "name" ? 1 : -1 };
    renderTable();
  });
  reload = load;
  await load();
  every(5000, load);
}

function renderBanner() {
  const box = $("#upBanner");
  const behind = state.systems.filter((s) => s.online && outdated(s, latest));
  const auto = behind.filter(canUpgrade);
  if (!behind.length) { box.innerHTML = ""; return; }
  const manual = behind.length - auto.length;
  box.innerHTML = `<div class="up-banner">${icon("circle-arrow-up")}
    <span><b>${behind.length}</b> 台在线探针落后于 ${esc(latest)}${manual ? `<span class="faint">（其中 ${manual} 台版本过旧，需手动更新一次）</span>` : ""}</span>
    ${auto.length ? `<button class="btn" id="upAll">全部升级（${auto.length}）</button>` : ""}</div>`;
  const btn = /** @type {HTMLButtonElement | null} */ ($("#upAll"));
  if (btn) btn.addEventListener("click", () => upgradeAll(auto, latest, reload));
}

function renderStats() {
  const s = state.systems, on = s.filter((x) => x.online);
  let rx = 0, tx = 0;
  on.forEach((x) => { if (x.latest) { rx += x.latest.netRxBps; tx += x.latest.netTxBps; } });
  $("#stats").innerHTML = `
    <div class="stat"><div class="label">主机总数</div><div class="value">${s.length}</div><div class="sub">${osEntries(countBy(s, osKey)).map(([k, n]) => `${esc(OS_NAMES[k] || k)} ${n}`).join(" · ") || "—"}</div></div>
    <div class="stat ok"><div class="label">在线</div><div class="value">${on.length}</div><div class="sub">WSS 长连接存活</div></div>
    <div class="stat bad"><div class="label">离线</div><div class="value">${s.length - on.length}</div><div class="sub">${s.length - on.length ? "检查探针进程或网络" : "全部正常"}</div></div>
    <div class="stat net"><div class="label">实时总带宽</div><div class="value"><span class="rx">↓ ${rate(rx)}</span><br><span class="tx">↑ ${rate(tx)}</span></div></div>`;
}

/**
 * @template T
 * @param {T[]} list
 * @param {(x: T) => string} fn
 * @returns {Record<string, number>}
 */
function countBy(list, fn) {
  /** @type {Record<string, number>} */
  const m = {};
  list.forEach((x) => { const k = fn(x); m[k] = (m[k] || 0) + 1; });
  return m;
}

/** @param {Record<string, number>} counts */
function osEntries(counts) {
  return Object.entries(counts).sort((a, b) => {
    const ia = OS_ORDER.indexOf(a[0]), ib = OS_ORDER.indexOf(b[0]);
    return (ia < 0 ? 99 : ia) - (ib < 0 ? 99 : ib);
  });
}

function renderChips() {
  const s = state.systems;
  /** @type {[string, string, number][]} */
  const chips = [["all", "全部", s.length], ["online", "在线", s.filter((x) => x.online).length], ["offline", "离线", s.filter((x) => !x.online).length]];
  osEntries(countBy(s, osKey)).forEach(([k, n]) => chips.push(["os:" + k, OS_NAMES[k] || k, n]));
  const virts = countBy(s, virtKey);
  delete virts[""];
  VIRT_ORDER.filter((k) => virts[k]).forEach((k) => chips.push(["virt:" + k, VIRT_NAMES[k] || k, virts[k]]));
  Object.keys(virts).filter((k) => !VIRT_ORDER.includes(k)).sort().forEach((k) => chips.push(["virt:" + k, k, virts[k]]));
  const tags = {};
  s.forEach((x) => (x.tags || []).forEach((t) => (tags[t] = (tags[t] || 0) + 1)));
  Object.entries(tags).sort().forEach(([t, n]) => chips.push(["tag:" + t, "#" + t, n]));
  $("#chips").innerHTML = `<span class="faint" style="font-size:12px;margin-right:2px">筛选</span>` +
    chips.map(([k, l, n]) => `<button class="chip ${state.chip === k ? "on" : ""}" data-k="${esc(k)}">${k.startsWith("os:") ? osIcon(k.slice(3)) : ""}${esc(l)}<span class="n">${n}</span></button>`).join("");
  $("#chips").querySelectorAll(".chip").forEach((b) => b.addEventListener("click", () => {
    state.chip = b.dataset.k;
    renderChips();
    renderTable();
  }));
}

// The phone layout has no table header to click, so sorting moves into a select.
const SORT_NAMES = [["name", "按名称"], ["cpu", "按 CPU"], ["mem", "按内存"], ["disk", "按磁盘"], ["net", "按网络"], ["agent", "按在线"]];

const SORTS = {
  name: (s) => (s.name || s.hostname || "").toLowerCase(),
  cpu: (s) => (s.online && s.latest ? s.latest.cpuPct : -1),
  mem: (s) => (s.online && s.latest ? s.latest.memPct : -1),
  disk: (s) => (s.latest ? s.latest.diskPct : -1),
  net: (s) => (s.online && s.latest ? s.latest.netRxBps + s.latest.netTxBps : -1),
  agent: (s) => (s.online ? 1 : 0),
};

function filtered() {
  const c = state.chip, q = state.q;
  return state.systems.filter((s) => {
    if (c === "online" && !s.online) return false;
    if (c === "offline" && s.online) return false;
    if (c.startsWith("os:") && osKey(s) !== c.slice(3)) return false;
    if (c.startsWith("virt:") && virtKey(s) !== c.slice(5)) return false;
    if (c.startsWith("tag:") && !(s.tags || []).includes(c.slice(4))) return false;
    if (q) {
      const vk = virtKey(s);
      const hay = [s.name, s.hostname, s.id, s.biosUuid, ip(s), s.remoteAddr, osLabel(s), VIRT_NAMES[vk] || vk, ...(s.tags || [])].join(" ").toLowerCase();
      if (!hay.includes(q)) return false;
    }
    return true;
  }).sort((a, b) => {
    const f = SORTS[state.sort.key], x = f(a), y = f(b);
    return (x < y ? -1 : x > y ? 1 : 0) * state.sort.dir;
  });
}

export function renderTable() {
  const wrap = $("#tableWrap");
  if (!wrap) return;
  if (!state.systems.length) {
    wrap.innerHTML = `<div class="empty">还没有探针接入。<br><br>在目标机器上配置 <code>VARTIO_AGENT_CONTROL_URL=ws://${esc(location.host)}${API}/connect</code>（或 <code>GUEST_CONTROL_URL</code>）与 <code>VARTIO_AGENT_TOKEN</code> 后运行 <code>vartio-agent</code>。</div>`;
    return;
  }
  const rows = filtered();
  const th = (k, l) => `<th class="sort" data-k="${k}">${l}<span class="ar">${state.sort.key === k ? (state.sort.dir > 0 ? "▲" : "▼") : "↕"}</span></th>`;
  wrap.innerHTML = `<table class="sys"><thead><tr>
      ${th("name", "主机")}${th("cpu", "CPU")}${th("mem", "内存")}${th("disk", "磁盘")}${th("net", "网络")}<th class="c-up">运行时长</th>${th("agent", "探针")}<th class="c-tags">标签</th>
    </tr></thead><tbody>${rows.map(row).join("") || `<tr><td colspan="8" class="empty">没有匹配的主机</td></tr>`}</tbody></table>`;
  wrap.querySelectorAll("th.sort").forEach((h) => h.addEventListener("click", () => {
    const k = h.dataset.k;
    state.sort = { key: k, dir: state.sort.key === k ? -state.sort.dir : k === "name" ? 1 : -1 };
    const ms = /** @type {HTMLSelectElement | null} */ ($("#mSort"));
    if (ms) ms.value = k;
    renderTable();
  }));
  wrap.querySelectorAll("tbody tr[data-id]").forEach((tr) => tr.addEventListener("click", () => (location.hash = "#/system/" + tr.dataset.id)));
  wrap.querySelectorAll("button.ver-up").forEach((b) => b.addEventListener("click", (e) => {
    e.stopPropagation();
    const s = state.systems.find((x) => x.id === b.dataset.up);
    if (!s) return;
    if (canUpgrade(s)) upgradeOne(s, latest, reload);
    else upgradeNotice(s, latest);
  }));
}

/** @param {SystemView} s */
function osTag(s) {
  const k = osKey(s);
  const name = OS_NAMES[k];
  if (!name || k === "unknown") return "";
  return `<span class="os-tag">${osIcon(k)}<span>${esc(name)}</span></span>`;
}

/** @param {SystemView} s */
function row(s) {
  const l = s.latest, on = s.online && l;
  const up = s.online && s.info && s.info.bootTime ? Date.now() / 1000 - s.info.bootTime : 0;
  const tags = (s.tags || []).map((t) => `<span class="tag">${esc(t)}</span>`).join("");
  return `<tr data-id="${esc(s.id)}" class="${s.online ? "" : "offline"}">
    <td class="c-name"><div class="sys-name"><span class="dot ${s.online ? "up" : ""}"></span><div><div class="nm">${esc(s.name || s.hostname || s.id)}${osTag(s)}${virtTag(s)}</div><div class="os">${esc(osLabel(s) || "—")} · ${esc(ip(s))}</div>${tags ? `<div class="name-tags">${tags}</div>` : ""}</div></div></td>
    <td class="c-cpu" data-l="CPU">${on ? meter(l.cpuPct) : `<span class="faint">—</span>`}</td>
    <td class="c-mem" data-l="内存">${on ? meter(l.memPct) : `<span class="faint">—</span>`}</td>
    <td class="c-disk" data-l="磁盘">${l ? meter(l.diskPct) : `<span class="faint">—</span>`}</td>
    <td class="c-net" data-l="网络">${on ? `<div class="net"><span class="rx">↓ ${rate(l.netRxBps)}</span><br><span class="tx">↑ ${rate(l.netTxBps)}</span></div>` : `<span class="faint">—</span>`}</td>
    <td class="c-up" data-l="运行" style="font-size:12.5px">${s.online ? dur(up) : `<span class="faint">离线 ${ago(s.lastSeen)}</span>`}</td>
    <td class="c-agent"><span class="agent"><span class="dot ${s.online ? "up" : ""}"></span>${esc(s.version || "—")}${outdated(s, latest) ? `<button type="button" class="ver-up" data-up="${esc(s.id)}" title="${canUpgrade(s) ? `升级到 ${esc(latest)}` : `可升级到 ${esc(latest)}（当前版本不支持远程升级）`}">${icon("circle-arrow-up")}</button>` : ""}</span></td>
    <td class="c-tags">${tags}</td>
  </tr>`;
}
