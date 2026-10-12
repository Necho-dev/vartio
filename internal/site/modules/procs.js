// @ts-check
import { $, esc, osLabel, ip, api, state } from "./core.js";
import { renderChart } from "./charts.js";
import { openDrawer } from "./ui.js";

/** @type {Record<string, number>} */
const SPAN = { "1h": 3600e3, "6h": 6 * 3600e3, "24h": 86400e3, "7d": 7 * 86400e3, "30d": 30 * 86400e3 };
/** @type {Record<string, string>} */
const RANGE_LABEL = { "1h": "最近 1 小时", "6h": "最近 6 小时", "24h": "最近 24 小时", "7d": "最近 7 天", "30d": "最近 30 天" };

/** @type {[key: string, label: string, sortKey: (p: ProcInfo) => string | number, numeric: boolean][]} */
const PROC_COLS = [
  ["pid", "PID", (p) => p.pid, true],
  ["name", "名称", (p) => (p.name || "").toLowerCase(), false],
  ["cpu", "CPU", (p) => p.cpuPct || 0, true],
  ["mem", "内存", (p) => p.memMB || 0, true],
  ["status", "状态", (p) => p.status || "", false],
  ["cmd", "命令行", (p) => p.cmdline || "", false],
];

/** @param {SystemView} s */
export function openProcs(s) {
  let list = [], timer = null, loading = false, sort = { key: "cpu", dir: -1 }, at = null;
  openDrawer(`进程 · ${s.name}`, `${osLabel(s)} · ${ip(s)}`, `
    <div class="drawer-tools">
      <input class="input" id="procQ" placeholder="过滤进程名 / PID / 命令行" autocomplete="off">
      <label><input type="checkbox" id="procAuto" checked> 每 5 秒刷新</label>
      <button class="btn" id="procReload">刷新</button>
    </div>
    <div class="drawer-scroll" id="procList"><div class="empty">加载中…</div></div>
    <div class="proc-hist" id="procHist" hidden>
      <div class="proc-hist-head"><span>进程数</span><span id="procHistRange"></span></div>
      <div class="chart" id="c-procHist"></div>
    </div>
    <div class="drawer-foot" id="procFoot">CPU 为约 0.7 秒内占全部核心的比例；内存为 “—” 表示探针无权读取，以管理员或服务方式运行可看到全部。</div>`,
    () => { if (timer) clearInterval(timer); });

  const draw = () => {
    const box = $("#procList");
    if (!box) return;
    const q = $("#procQ").value.trim().toLowerCase();
    const col = PROC_COLS.find((c) => c[0] === sort.key);
    const rows = list
      .filter((p) => !q || (p.name || "").toLowerCase().includes(q) || String(p.pid) === q || (p.cmdline || "").toLowerCase().includes(q))
      .sort((a, b) => { const x = col[2](a), y = col[2](b); return (x < y ? -1 : x > y ? 1 : 0) * sort.dir || a.pid - b.pid; });
    const shown = rows.slice(0, 500);
    const maxCpu = Math.max(1, ...shown.map((p) => p.cpuPct || 0));
    const th = ([k, label, , num]) => `<th data-k="${k}" class="${num ? "num" : ""}">${label}<span class="ar">${sort.key === k ? (sort.dir > 0 ? "▲" : "▼") : "↕"}</span></th>`;
    const keepScroll = box.scrollTop;
    box.innerHTML = `<table class="procs"><colgroup><col style="width:76px"><col style="width:30%"><col style="width:118px"><col style="width:96px"><col style="width:74px"><col></colgroup><thead><tr>${PROC_COLS.map(th).join("")}</tr></thead><tbody>${shown.map((p) => `<tr>
        <td class="num">${p.pid}</td>
        <td class="clip" title="${esc(p.name || "")}">${p.name ? esc(p.name) : '<span class="faint">—</span>'}</td>
        <td class="num"><span class="cpu-cell">${(p.cpuPct || 0).toFixed(1)}%<span class="mini"><i style="width:${Math.min(100, ((p.cpuPct || 0) / maxCpu) * 100).toFixed(0)}%"></i></span></span></td>
        <td class="num">${p.memMB ? p.memMB.toFixed(1) + " MB" : '<span class="faint" title="探针无权读取">—</span>'}</td>
        <td>${esc(p.status || "")}</td>
        <td class="cmd" title="${esc(p.cmdline || "")}">${esc(p.cmdline || "")}</td>
      </tr>`).join("") || `<tr><td colspan="6" class="empty">没有匹配的进程</td></tr>`}</tbody></table>`;
    box.scrollTop = keepScroll;
    box.querySelectorAll("thead th").forEach((h) => h.addEventListener("click", () => {
      const k = h.dataset.k;
      sort = { key: k, dir: sort.key === k ? -sort.dir : (k === "name" || k === "pid" || k === "status" || k === "cmd") ? 1 : -1 };
      draw();
    }));
    const sub = `${osLabel(s)} · ${ip(s)} · 共 ${list.length} 个进程${rows.length !== list.length ? `，匹配 ${rows.length}` : ""}${rows.length > shown.length ? `，显示前 ${shown.length}` : ""}${at ? ` · ${at.toLocaleTimeString()} 更新` : ""}`;
    $("#drawerSub").textContent = sub;
  };
  const load = async () => {
    if (loading) return;
    loading = true;
    try {
      list = /** @type {ProcInfo[]} */ (await api(`/sessions/${encodeURIComponent(s.id)}/procs`));
      at = new Date();
      draw();
    } catch (e) {
      const b = $("#procList");
      if (b) b.innerHTML = `<div class="empty err">${esc(e.message)}</div>`;
    } finally {
      loading = false;
    }
  };
  $("#procQ").addEventListener("input", draw);
  $("#procReload").addEventListener("click", load);
  const auto = () => {
    if (timer) clearInterval(timer);
    timer = $("#procAuto").checked ? setInterval(load, 5000) : null;
  };
  $("#procAuto").addEventListener("change", auto);
  auto();
  load();
  loadProcHist(s);
  setTimeout(() => { const q = $("#procQ"); if (q) q.focus(); }, 50);
}

/** History for the page's current range. The live total stays in the drawer subtitle. */
async function loadProcHist(s) {
  const wrap = $("#procHist");
  const box = $("#c-procHist");
  if (!wrap || !box || !state.config?.history) return;
  try {
    /** @type {Series} */
    const d = await api(`/monitor/systems/${encodeURIComponent(s.id)}/series?range=${state.range}`);
    const to = Date.now();
    const from = to - (SPAN[state.range] || SPAN["1h"]);
    renderChart(box, {
      t: d.t, stepSec: d.stepSec, from, to,
      fmt: (v) => (v == null ? "—" : Math.round(v) + ""),
      series: [{ label: "进程", color: "#f97316", data: d.procs || [] }],
      empty: "暂无历史",
    });
    const lab = $("#procHistRange");
    if (lab) lab.textContent = RANGE_LABEL[state.range] || "";
    wrap.hidden = !(d.t || []).length;
  } catch {
    wrap.hidden = true;
  }
}
