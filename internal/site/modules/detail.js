// @ts-check
import { $, app, state, esc, html, bytes, rate, pct, dur, ago, meter, ip, api, every } from "./core.js";
import { renderChart } from "./charts.js";
import { icon } from "./icons.js";
import { openModal, closeModal } from "./ui.js";
import { route } from "./nav.js";
import { openTerm } from "./term.js";
import { openScreen } from "./screen.js";
import { openProcs } from "./procs.js";
import { openFiles } from "./files/index.js";
import { latestAgent, outdated, canUpgrade, upgradeOne, RUNNERS } from "./upgrade.js";

let latest = "";

const C = { blue: "#3b82f6", green: "#22c55e", cyan: "#06b6d4", violet: "#8b5cf6", orange: "#f97316", pink: "#ec4899", yellow: "#eab308", faint: "#71717a" };
const RANGES = [["1h", "1 小时"], ["6h", "6 小时"], ["24h", "24 小时"], ["7d", "7 天"], ["30d", "30 天"]];
const SPAN = { "1h": 3600e3, "6h": 6 * 3600e3, "24h": 86400e3, "7d": 7 * 86400e3, "30d": 30 * 86400e3 };

/** @param {string} id */
export async function detail(id) {
  app.innerHTML = `<a class="back" href="#/">${icon("arrow-left")}返回全部主机</a><div id="d"><div class="empty">加载中…</div></div>`;
  /** @type {SystemView} */
  let sys;
  try {
    sys = await api("/monitor/systems/" + encodeURIComponent(id));
    latest = await latestAgent();
  } catch (e) {
    if (e.message !== "unauthorized") $("#d").innerHTML = `<div class="empty">${esc(e.message)}</div>`;
    return;
  }
  $("#d").innerHTML = `
    <section class="card hero" id="hero"></section>
    <section class="grid2" id="charts">
      ${panel("cpu", "CPU 使用率", "全部核心平均值；虚线为区间峰值")}
      ${panel("mem", "内存使用", "已用物理内存（总量 - 可用）与交换区")}
      ${panel("disk", "磁盘使用", "系统盘已用空间")}
      ${panel("dio", "磁盘 I/O", "全部物理磁盘读写吞吐")}
      ${panel("net", "网络带宽", "物理网卡收发速率（已排除回环/虚拟网卡）")}
      ${panel("load", "系统负载", "")}
      ${panel("procs", "进程数", "运行中的进程总数")}
      ${panel("temp", "温度", "传感器最高温度", true)}
    </section>
    <section class="grid2">
      <div class="card panel"><h3>磁盘分区</h3><p class="desc">最新一次采样</p><div id="disks"></div></div>
      <div class="card panel"><h3>网络接口</h3><p class="desc">带 IPv4 的物理网卡</p><div id="nics"></div></div>
      <div class="card panel ev-panel"><div class="seg ev-seg" id="evSeg">${EV_CATS.map(([k, n]) => `<button data-c="${k}" class="${state.evCat === k ? "on" : ""}">${n}</button>`).join("")}</div>
        <h3>事件记录</h3><p class="desc">上下线、重启、配置变化与运维操作</p><div class="ev-box"><ul class="events" id="events"></ul></div></div>
      <div class="card panel"><h3>主机信息</h3><p class="desc">探针上报的静态信息</p><div id="info"></div></div>
    </section>`;
  renderHero(sys);
  renderInfo(sys);
  renderEvents(sys.events || []);
  $("#events").addEventListener("click", copyEvent);
  $("#evSeg").querySelectorAll("button").forEach((b) => b.addEventListener("click", () => {
    state.evCat = b.dataset.c;
    $("#evSeg").querySelectorAll("button").forEach((x) => x.classList.toggle("on", x === b));
    loadEvents(sys);
  }));
  await loadSeries(sys);
  every(Math.max(5, state.config.intervalSec) * 1000, async () => {
    try {
      sys = await api("/monitor/systems/" + encodeURIComponent(id));
      latest = await latestAgent();
      renderHero(sys);
      renderInfo(sys);
      if (state.evCat) loadEvents(sys); else renderEvents(sys.events || []);
      if (state.range === "1h" || state.range === "6h") await loadSeries(sys);
    } catch (e) { /* keep last view */ }
  });
  every(60000, () => { if (!(state.range === "1h" || state.range === "6h")) loadSeries(sys); });
}

const EV_CATS = [["", "全部"], ["lifecycle", "生命周期"], ["change", "配置变化"], ["audit", "运维操作"]];
/** @type {Record<string, string>} */
const EV_NAMES = {
  online: "机器上线", offline: "机器离线", upgrade: "探针升级", reboot: "系统重启", rekey: "标识迁移", conflict: "指纹冲突",
  remote: "来源 IP 变化", hostname: "主机名变更", os: "系统版本变更", kernel: "内核变更", cpu: "CPU 变更", memory: "内存变更", disk: "磁盘变更", nic: "网卡变更",
  "term.open": "打开终端", "term.close": "关闭终端", "display.open": "查看画面", "display.close": "关闭画面", exec: "执行命令",
  "power.reboot": "重启主机", "power.shutdown": "关闭主机", "proc.kill": "结束进程", "proc.start": "启动进程", "proc.stop": "停止进程",
  "service.start": "启动服务", "service.stop": "停止服务", "service.restart": "重启服务",
  "file.upload": "上传文件", "file.download": "下载文件", "file.mkdir": "新建目录", "file.rename": "重命名", "file.delete": "永久删除", "file.trash": "移入回收站",
  "trash.restore": "还原文件", "trash.purge": "彻底删除", "trash.empty": "清空回收站", meta: "编辑信息",
  "agent.upgrade": "下发升级", "upgrade.failed": "升级失败",
};

/** @param {SystemView} s */
async function loadEvents(s) {
  const cat = state.evCat;
  try {
    const list = /** @type {SystemEvent[]} */ (await api(`/monitor/systems/${encodeURIComponent(s.id)}/events?limit=100${cat ? "&category=" + cat : ""}`));
    if (cat === state.evCat) renderEvents(list);
  } catch (e) { /* keep last view */ }
}

/** @type {SystemEvent[]} */
let shownEvents = [];

/** @param {SystemEvent} e */
function eventText(e) {
  return [new Date(e.ts).toLocaleString([], { hour12: false }), EV_NAMES[e.kind] || e.kind, e.detail,
    e.ip && "探针 IP " + e.ip, e.version && "版本 " + e.version, e.actor && "操作者 " + e.actor].filter(Boolean).join(" · ");
}

/** @param {MouseEvent} ev */
async function copyEvent(ev) {
  const btn = /** @type {HTMLElement | null} */ (/** @type {HTMLElement} */ (ev.target).closest(".ev-copy"));
  const e = btn && shownEvents[Number(btn.dataset.i)];
  if (!btn || !e) return;
  if (await copyText(eventText(e))) {
    btn.innerHTML = icon("check");
    btn.classList.add("done");
    setTimeout(() => { btn.innerHTML = icon("copy"); btn.classList.remove("done"); }, 1200);
  }
}

/** @param {string} text */
async function copyText(text) {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    // Plain-http LAN access has no async clipboard API.
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.style.cssText = "position:fixed;opacity:0";
    document.body.append(ta);
    ta.select();
    const ok = document.execCommand("copy");
    ta.remove();
    return ok;
  }
}

/** @param {SystemEvent[]} ev */
function renderEvents(ev) {
  const box = $("#events");
  if (!box) return;
  shownEvents = ev;
  if (!ev.length) { box.innerHTML = `<li class="faint">暂无事件</li>`; return; }
  /** @param {"network" | "package" | "user"} ic @param {string} cls @param {string | undefined} v @param {string} tip */
  const chip = (ic, cls, v, tip) => (v ? `<span class="ev-chip ${cls}" title="${tip}">${icon(ic)}${esc(v)}</span>` : "");
  box.innerHTML = ev.map((e, i) => {
    const cls = e.kind === "online" ? "up" : e.kind === "offline" ? "down" : e.level === "warn" ? "warn" : e.category;
    const t = new Date(e.ts);
    return `<li class="ev ev-${cls}"><time class="ev-time" datetime="${e.ts}"><b>${t.toLocaleTimeString([], { hour12: false })}</b>${t.toLocaleDateString()}</time><i class="ev-dot"></i>
      <div class="ev-main"><span class="ev-kind">${esc(EV_NAMES[e.kind] || e.kind)}</span>${e.detail ? `<span class="ev-detail" title="${esc(e.detail)}">${esc(e.detail)}</span>` : ""}</div>
      <button class="ev-copy" data-i="${i}" title="复制事件">${icon("copy")}</button>
      <div class="ev-meta">${chip("network", "ip", e.ip, "探针来源 IP")}${chip("package", "ver", e.version, "探针版本")}${chip("user", "actor", e.actor, "操作者 IP")}</div></li>`;
  }).join("");
}

/** @param {string} id @param {string} title @param {string} desc @param {boolean} [hidden] */
function panel(id, title, desc, hidden) {
  return `<div class="card panel" id="p-${id}" ${hidden ? "hidden" : ""}><span class="now" id="now-${id}"></span><h3>${title}</h3><p class="desc" id="desc-${id}">${desc}</p><div class="chart" id="c-${id}"></div><div class="legend" id="lg-${id}"></div></div>`;
}

/** @param {SystemView} s */
function renderHero(s) {
  const i = s.info || {}, l = s.latest;
  const up = s.online && i.bootTime ? Date.now() / 1000 - i.bootTime : 0;
  const fact = (icon, v) => (v ? `<span>${icon} <b>${esc(v)}</b></span>` : "");
  $("#hero").innerHTML = `
    <div class="hero-main">
      <h1>${esc(s.name || s.hostname)} <span class="badge ${s.online ? "up" : "down"}"><span class="dot ${s.online ? "up" : ""}"></span>${s.online ? "在线" : "离线"}</span>
        <span class="edit" id="editBtn" title="编辑名称和标签">${icon("pencil")} 编辑</span>
        <span class="hero-tags">${(s.tags || []).map((t) => `<span class="tag">${esc(t)}</span>`).join("")}</span></h1>
      <div class="facts">
        ${fact("主机名", s.hostname)}${fact("IP", ip(s))}${s.remoteAddr && s.remoteAddr !== ip(s) ? fact("来源", s.remoteAddr) : ""}
        ${fact("系统", [i.platform, i.platformVersion].filter(Boolean).join(" "))}
        ${fact("CPU", i.cpuModel ? `${i.cpuModel} (${i.cores || "?"}C/${i.threads || "?"}T)` : "")}
        ${fact("内存", l ? bytes(l.memTotal) : "")}
        ${s.online ? fact("运行", dur(up)) : fact("最后在线", ago(s.lastSeen))}
        ${fact("探针", s.version)}${outdated(s, latest) && !canUpgrade(s) && s.online ? `<span class="faint">可更新至 ${esc(latest)}，当前版本需手动更新一次</span>` : ""}
        ${l && s.online ? fact("延迟", l.latencyMs + " ms") : ""}
      </div>
    </div>
    <div class="hero-actions">
      <div class="seg" id="rangeSeg">${RANGES.map(([k, n]) => `<button data-r="${k}" class="${state.range === k ? "on" : ""}">${n}</button>`).join("")}</div>
      <button class="btn" id="termBtn" ${s.online ? "" : "disabled"}>${icon("square-terminal")}终端</button>
      <button class="btn" id="screenBtn" ${s.online ? "" : "disabled"}>${icon("monitor")}实时画面</button>
      <button class="btn" id="procBtn" ${s.online ? "" : "disabled"}>${icon("list")}进程</button>
      <button class="btn" id="filesBtn" ${s.online ? "" : "disabled"}>${icon("folder-open")}文件</button>
      ${canUpgrade(s) && outdated(s, latest) ? `<button class="btn primary" id="upBtn">${icon("circle-arrow-up")}升级到 ${esc(latest)}</button>` : ""}
      ${s.online ? "" : `<button class="btn danger" id="delBtn">${icon("trash-2")}删除</button>`}
    </div>`;
  $("#rangeSeg").querySelectorAll("button").forEach((b) => b.addEventListener("click", () => {
    state.range = b.dataset.r;
    localStorage.setItem("probeHubRange", state.range);
    renderHero(s);
    loadSeries(s);
  }));
  $("#editBtn").addEventListener("click", () => editSystem(s));
  $("#termBtn").addEventListener("click", () => openTerm(s));
  $("#screenBtn").addEventListener("click", () => openScreen(s));
  $("#procBtn").addEventListener("click", () => openProcs(s));
  $("#filesBtn").addEventListener("click", () => openFiles(s));
  const upBtn = /** @type {HTMLButtonElement | null} */ ($("#upBtn"));
  if (upBtn) upBtn.addEventListener("click", () => upgradeOne(s, latest));
  const del = $("#delBtn");
  if (del) del.addEventListener("click", async () => {
    if (!confirm(`删除 ${s.name} 及其全部历史数据？`)) return;
    try { await api("/monitor/systems/" + encodeURIComponent(s.id), { method: "DELETE" }); location.hash = "#/"; }
    catch (e) { alert(e.message); }
  });
}

/** @param {SystemView} s */
function renderInfo(s) {
  const l = s.latest || /** @type {Partial<Sample>} */ ({}), i = s.info || {};
  const { disks } = poolDisks(l);
  $("#disks").innerHTML = disks.length ? `<table class="kv-table disk-table"><tr><th>挂载点</th><th>文件系统</th><th>已用 / 总量</th><th style="width:38%">使用率</th></tr>
    ${disks.map((d) => { const p = d.total ? (d.used / d.total) * 100 : 0; return `<tr><td>${esc(d.mount)}${d.mount === l.rootMount ? ' <span class="tag">系统</span>' : ""}${d.merged.length ? `<div class="faint" title="同一存储池中的卷共享容量，只计一次">同一存储池：${d.merged.map(esc).join("、")}</div>` : ""}</td><td>${esc(d.fs || "—")}</td><td>${bytes(d.used)} / ${bytes(d.total)}</td><td>${meter(p)}</td></tr>`; }).join("")}</table>` : `<div class="faint">暂无</div>`;
  const nics = l.nics || [];
  $("#nics").innerHTML = nics.length ? `<table class="kv-table nic-table"><tr><th>名称</th><th>IPv4</th><th>MAC</th><th>状态</th></tr>
    ${nics.map((n) => `<tr><td>${esc(n.name)}</td><td>${esc(n.ipv4 || "—")}</td><td>${esc(n.mac || "—")}</td><td>${n.up ? '<span style="color:var(--green)">up</span>' : '<span class="faint">down</span>'}</td></tr>`).join("")}</table>` : `<div class="faint">暂无</div>`;
  const kv = [["设备 ID", s.id], ["BIOS UUID", s.biosUuid], ["操作系统", [i.os, i.platform, i.platformVersion].filter(Boolean).join(" / ")], ["内核", i.kernel], ["架构", i.arch],
    ["CPU", i.cpuModel], ["核心/线程", i.cores ? `${i.cores} / ${i.threads}` : ""], ["进程数", l.procs],
    ["运行方式", s.online && s.runner ? RUNNERS[s.runner] || s.runner : ""], ["首次接入", s.firstSeen && new Date(s.firstSeen).toLocaleString()],
    ["本次连接", s.online && s.connectedAt ? new Date(s.connectedAt).toLocaleString() : ""], ["最近采样", l.ts && new Date(l.ts).toLocaleString()]];
  const hint = !s.online || s.runner !== "console" ? ""
    : i.os === "windows" ? `<p class="faint run-hint">${icon("triangle-alert")}<span>以前台进程运行，重启或注销后不会自动启动。建议执行 <code>probe-agent.exe install</code> 设为登录自启的托盘模式；无人登录的服务器以管理员执行 <code>probe-agent.exe service install</code>。</span></p>`
    : i.os === "darwin" ? `<p class="faint run-hint">${icon("triangle-alert")}<span>在终端里前台运行，重启或注销后不会自动启动。建议执行 <code>./probe-agent install</code> 设为登录自启，在菜单栏后台运行。</span></p>` : "";
  $("#info").innerHTML = `<table class="kv-table info-table">${kv.filter(([, v]) => v !== "" && v != null).map(([k, v]) => `<tr><th style="width:110px">${k}</th><td>${esc(v)}</td></tr>`).join("")}</table>${hint}`;
}

/** @param {SystemView} s */
async function loadSeries(s) {
  if (!state.config.history) {
    ["cpu", "mem", "disk", "dio", "net", "load"].forEach((k) => chart(k, { t: [], series: [], fmt: (v) => String(v), empty: "未配置 PROBE_HUB_DSN，无历史数据" }));
    return;
  }
  /** @type {Series} */
  let d;
  try {
    d = await api(`/monitor/systems/${encodeURIComponent(s.id)}/series?range=${state.range}`);
  } catch (e) { return; }
  const to = Date.now(), from = to - SPAN[state.range];
  const base = { t: d.t, stepSec: d.stepSec, from, to };
  const l = s.latest || /** @type {Partial<Sample>} */ ({});
  const memMax = Math.max(0, ...d.memTotal) || l.memTotal || null;
  const diskMax = Math.max(0, ...d.diskTotal) || l.diskTotal || null;

  chart("cpu", { ...base, max: 100, fmt: pct, series: [
    { label: "平均", color: C.blue, data: d.cpu },
    { label: "峰值", color: C.faint, data: d.cpuMax, area: false, dash: true, width: 1 }] }, s.online && l.cpuPct != null ? pct(l.cpuPct) : "");
  chart("mem", { ...base, max: memMax, fmt: bytes, series: [
    { label: "已用", color: C.green, data: d.mem },
    ...(Math.max(0, ...d.swap) > 0 ? [{ label: "交换区", color: C.orange, data: d.swap, area: false }] : [])] },
    s.online && l.memTotal ? `${bytes(l.memUsed)} / ${bytes(l.memTotal)}` : "");
  const parts = diskParts(d, l);
  $("#desc-disk").textContent = parts ? "各分区已用空间（堆积）" : "系统盘已用空间";
  if (parts) {
    chart("disk", { ...base, stacked: true, totalLabel: "合计", capacity: parts.total || null, fmt: bytes, series: parts.series },
      parts.total ? `${bytes(parts.used)} / ${bytes(parts.total)} · ${parts.series.length} 个分区` : "");
  } else {
    chart("disk", { ...base, capacity: diskMax, fmt: bytes, series: [{ label: "已用", color: C.violet, data: d.disk }] },
      l.diskTotal ? `${bytes(l.diskUsed)} / ${bytes(l.diskTotal)} · ${l.rootMount || ""}` : "");
  }
  chart("dio", { ...base, fmt: rate, series: [
    { label: "读", color: C.cyan, data: d.diskRead },
    { label: "写", color: C.orange, data: d.diskWrite }] }, s.online ? `读 ${rate(l.diskReadBps)} · 写 ${rate(l.diskWriteBps)}` : "");
  chart("net", { ...base, fmt: rate, series: [
    { label: "下行", color: C.cyan, data: d.netRx },
    { label: "上行", color: C.violet, data: d.netTx }] }, s.online ? `↓ ${rate(l.netRxBps)} · ↑ ${rate(l.netTxBps)}` : "");

  const hasLoad = d.load1.some((v) => v > 0);
  const winLoad = s.info?.os === "windows";
  const hasTemp = d.temp.some((v) => v != null);
  $("#p-load").hidden = !hasLoad;
  $("#p-temp").hidden = !hasTemp;
  const shown = [...$("#charts").children].filter((p) => !(/** @type {HTMLElement} */ (p).hidden));
  shown.forEach((p, i) => p.classList.toggle("span-row", shown.length % 2 === 1 && i === shown.length - 1));
  $("#p-load").querySelector("h3").textContent = winLoad ? "系统负载（估算）" : "系统负载";
  $("#desc-load").textContent = winLoad
    ? "Windows 无原生 load average，由 CPU 使用率与处理器队列长度折算"
    : "1 / 5 / 15 分钟平均负载";
  if (hasLoad) chart("load", { ...base, fmt: (v) => (v == null ? "—" : v.toFixed(2)), series: [
    { label: "load1", color: C.pink, data: d.load1 },
    { label: "load5", color: C.violet, data: d.load5, area: false, width: 1.3 },
    { label: "load15", color: C.cyan, data: d.load15, area: false, width: 1.3 }] },
    s.online && l.load ? l.load.map((x) => x.toFixed(2)).join(" / ") : "");
  chart("procs", { ...base, fmt: (v) => (v == null ? "—" : Math.round(v) + ""), series: [{ label: "进程", color: C.orange, data: d.procs }] },
    s.online && l.procs ? l.procs + " 个" : "");

  if (hasTemp) {
    chart("temp", { ...base, fmt: (v) => (v == null ? "—" : v.toFixed(0) + "°C"), series: [{ label: "最高", color: C.yellow, data: d.temp }] },
      s.online && l.tempMax ? l.tempMax.toFixed(0) + "°C" : "");
    $("#lg-temp").innerHTML = (l.temps || []).slice(0, 8).map((t) => `<span>${esc(t.name)} <b>${t.c.toFixed(0)}°C</b></span>`).join("");
  }
}

// Volumes reporting the same fs/total/used share one pool (APFS containers);
// keep one per pool, system disk first, and remember the merged mounts.
/** @param {Partial<Sample>} l */
function poolDisks(l) {
  const all = (l.disks || []).slice().sort((a, b) => Number(b.mount === l.rootMount) - Number(a.mount === l.rootMount));
  const byKey = new Map();
  for (const x of all) {
    const k = `${x.fs}|${x.total}|${x.used}`;
    if (byKey.has(k)) byKey.get(k).merged.push(x.mount);
    else byKey.set(k, { ...x, merged: [] });
  }
  return { all, disks: [...byKey.values()] };
}

// Mounts follow the latest sample (system disk first), then any that only
// exist in history; totals come from the latest sample.
const PART_COLORS = [C.violet, C.blue, C.cyan, C.green, C.orange, C.pink, C.yellow, "#14b8a6", "#a3e635", "#f43f5e"];
/** @param {Series} d @param {Partial<Sample>} l @returns {{ total: number, used: number, series: ChartSeries[] } | null} */
function diskParts(d, l) {
  const hist = d.diskParts || {};
  const { all, disks } = poolDisks(l);
  const kept = disks.map((x) => x.mount).filter((m) => hist[m]);
  // A history-only mount that tracks a kept one value-for-value is the same pool.
  const twin = (m) => kept.some((k) => {
    let both = 0;
    for (let i = 0; i < hist[m].length; i++) {
      const a = hist[m][i], b = hist[k][i];
      if (a == null || b == null) continue;
      if (Math.abs(a - b) > Math.max(1, b) * 1e-4) return false;
      both++;
    }
    return both > 0;
  });
  const gone = Object.keys(hist).filter((m) => !all.some((x) => x.mount === m) && !twin(m)).sort();
  const mounts = [...kept, ...gone];
  if (!mounts.length) return null;
  const live = disks.filter((x) => hist[x.mount]);
  return {
    total: live.reduce((n, x) => n + (x.total || 0), 0),
    used: live.reduce((n, x) => n + (x.used || 0), 0),
    series: mounts.map((m, i) => ({ label: m === l.rootMount ? `${m}（系统）` : m, color: PART_COLORS[i % PART_COLORS.length], data: hist[m], width: 1.2 })),
  };
}

/** @param {string} id @param {ChartOpts} opts @param {string} [now] */
function chart(id, opts, now) {
  const box = $("#c-" + id);
  if (!box) return;
  renderChart(box, opts);
  if (now !== undefined) $("#now-" + id).textContent = now || "";
  const lg = $("#lg-" + id);
  if (lg && id !== "temp") lg.innerHTML = html`${opts.series.map((s) => html`<span><i style="background:${s.color}"></i>${s.label}</span>`)}`;
}

/** @param {SystemView} s */
function editSystem(s) {
  openModal("编辑主机", `
    <form id="editForm">
      <div class="form-row"><label>显示名称（留空则使用主机名 ${esc(s.hostname)}）</label><input class="input" name="name" value="${esc(s.name)}"></div>
      <div class="form-row"><label>标签（逗号分隔）</label><input class="input" name="tags" value="${esc((s.tags || []).join(", "))}" placeholder="例如：办公室, 测试机"></div>
      <div class="form-actions"><button type="button" class="btn" id="cancelEdit">取消</button><button class="btn primary" type="submit">保存</button></div>
    </form>`, null, { small: true });
  $("#cancelEdit").addEventListener("click", closeModal);
  $("#editForm").addEventListener("submit", async (e) => {
    e.preventDefault();
    const f = e.target;
    try {
      await api("/monitor/systems/" + encodeURIComponent(s.id), { method: "PATCH", json: {
        name: f.name.value, tags: f.tags.value.split(/[,，]/).map((x) => x.trim()).filter(Boolean) } });
      closeModal();
      route();
    } catch (err) { alert(err.message); }
  });
}
