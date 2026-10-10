// @ts-check
import { $, API, app, state, esc, rate, dur, ago, meter, osLabel, osKey, OS_NAMES, ip, api, every } from "./core.js";

export async function dashboard() {
  app.innerHTML = `
    <section class="stats" id="stats"></section>
    <div class="chips" id="chips"></div>
    <section class="card">
      <div class="card-head"><div><h2>全部主机</h2><p>每 ${state.config.intervalSec}s 采集一次，点击行查看详情与历史曲线</p></div>
        <span class="muted" id="updated"></span></div>
      <div class="table-wrap" id="tableWrap"></div>
    </section>`;
  const load = async () => {
    try {
      state.systems = await api("/monitor/systems");
      renderStats();
      renderChips();
      renderTable();
      $("#updated").textContent = "更新于 " + new Date().toLocaleTimeString();
    } catch (e) {
      if (e.message !== "unauthorized") $("#updated").textContent = "加载失败：" + e.message;
    }
  };
  await load();
  every(5000, load);
}

function renderStats() {
  const s = state.systems, on = s.filter((x) => x.online);
  let rx = 0, tx = 0;
  on.forEach((x) => { if (x.latest) { rx += x.latest.netRxBps; tx += x.latest.netTxBps; } });
  $("#stats").innerHTML = `
    <div class="stat"><div class="label">主机总数</div><div class="value">${s.length}</div><div class="sub">${Object.entries(countBy(s, osKey)).map(([k, n]) => `${OS_NAMES[k] || k} ${n}`).join(" · ") || "—"}</div></div>
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

function renderChips() {
  const s = state.systems;
  const chips = [["all", "全部", s.length], ["online", "在线", s.filter((x) => x.online).length], ["offline", "离线", s.filter((x) => !x.online).length]];
  Object.entries(countBy(s, osKey)).forEach(([k, n]) => chips.push(["os:" + k, OS_NAMES[k] || k, n]));
  const tags = {};
  s.forEach((x) => (x.tags || []).forEach((t) => (tags[t] = (tags[t] || 0) + 1)));
  Object.entries(tags).sort().forEach(([t, n]) => chips.push(["tag:" + t, "#" + t, n]));
  $("#chips").innerHTML = `<span class="faint" style="font-size:12px;margin-right:2px">筛选</span>` +
    chips.map(([k, l, n]) => `<button class="chip ${state.chip === k ? "on" : ""}" data-k="${esc(k)}">${esc(l)}<span class="n">${n}</span></button>`).join("");
  $("#chips").querySelectorAll(".chip").forEach((b) => b.addEventListener("click", () => {
    state.chip = b.dataset.k;
    renderChips();
    renderTable();
  }));
}

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
    if (c.startsWith("tag:") && !(s.tags || []).includes(c.slice(4))) return false;
    if (q) {
      const hay = [s.name, s.hostname, s.biosUuid, ip(s), s.remoteAddr, osLabel(s), ...(s.tags || [])].join(" ").toLowerCase();
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
    wrap.innerHTML = `<div class="empty">还没有探针接入。<br><br>在目标机器上配置 <code>GUEST_CONTROL_URL=ws://${esc(location.host)}${API}/connect</code> 与 <code>GUEST_TOKEN</code> 后运行 <code>probe-agent</code>。</div>`;
    return;
  }
  const rows = filtered();
  const th = (k, l) => `<th class="sort" data-k="${k}">${l}<span class="ar">${state.sort.key === k ? (state.sort.dir > 0 ? "▲" : "▼") : "↕"}</span></th>`;
  wrap.innerHTML = `<table class="sys"><thead><tr>
      ${th("name", "主机")}${th("cpu", "CPU")}${th("mem", "内存")}${th("disk", "磁盘")}${th("net", "网络")}<th>运行时长</th>${th("agent", "探针")}<th>标签</th>
    </tr></thead><tbody>${rows.map(row).join("") || `<tr><td colspan="8" class="empty">没有匹配的主机</td></tr>`}</tbody></table>`;
  wrap.querySelectorAll("th.sort").forEach((h) => h.addEventListener("click", () => {
    const k = h.dataset.k;
    state.sort = { key: k, dir: state.sort.key === k ? -state.sort.dir : k === "name" ? 1 : -1 };
    renderTable();
  }));
  wrap.querySelectorAll("tbody tr[data-id]").forEach((tr) => tr.addEventListener("click", () => (location.hash = "#/system/" + tr.dataset.id)));
}

/** @param {SystemView} s */
function row(s) {
  const l = s.latest, on = s.online && l;
  const up = s.online && s.info && s.info.bootTime ? Date.now() / 1000 - s.info.bootTime : 0;
  return `<tr data-id="${esc(s.biosUuid)}" class="${s.online ? "" : "offline"}">
    <td><div class="sys-name"><span class="dot ${s.online ? "up" : ""}"></span><div>${esc(s.name || s.hostname || s.biosUuid)}<div class="os">${esc(osLabel(s) || "—")} · ${esc(ip(s))}</div></div></div></td>
    <td>${on ? meter(l.cpuPct) : `<span class="faint">—</span>`}</td>
    <td>${on ? meter(l.memPct) : `<span class="faint">—</span>`}</td>
    <td>${l ? meter(l.diskPct) : `<span class="faint">—</span>`}</td>
    <td>${on ? `<div class="net"><span class="rx">↓ ${rate(l.netRxBps)}</span><br><span class="tx">↑ ${rate(l.netTxBps)}</span></div>` : `<span class="faint">—</span>`}</td>
    <td style="font-size:12.5px">${s.online ? dur(up) : `<span class="faint">离线 ${ago(s.lastSeen)}</span>`}</td>
    <td><span class="agent"><span class="dot ${s.online ? "up" : ""}"></span>${esc(s.version || "—")}</span></td>
    <td>${(s.tags || []).map((t) => `<span class="tag">${esc(t)}</span>`).join("")}</td>
  </tr>`;
}
