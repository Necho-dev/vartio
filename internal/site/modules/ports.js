// @ts-check
import { $, esc, osLabel, ip, api } from "./core.js";
import { openDrawer } from "./ui.js";

// lsof -F writes bytes outside printable ASCII as \xNN. Decode before display
// so a 1.25.0 probe still shows 比特浏览器 instead of the escape text.
function lsofText(s) {
  if (!s || !s.includes("\\x")) return s || "";
  const bytes = [];
  for (let i = 0; i < s.length; i++) {
    if (s[i] === "\\" && s[i + 1] === "n") { bytes.push(10); i += 1; continue; }
    if (s[i] === "\\" && s[i + 1] === "x" && /^[0-9a-fA-F]{2}$/.test(s.slice(i + 2, i + 4))) {
      bytes.push(parseInt(s.slice(i + 2, i + 4), 16));
      i += 3;
      continue;
    }
    const c = s.charCodeAt(i);
    if (c < 128) bytes.push(c);
    else new TextEncoder().encode(s[i]).forEach((b) => bytes.push(b));
  }
  return new TextDecoder().decode(Uint8Array.from(bytes));
}

/** @type {[key: string, label: string, sortKey: (p: PortInfo) => string | number, numeric: boolean][]} */
const PORT_COLS = [
  ["port", "端口", (p) => p.port, true],
  ["proto", "协议", (p) => p.proto || "", false],
  ["ip", "地址", (p) => p.ip || "", false],
  ["pid", "PID", (p) => p.pid || 0, true],
  ["name", "进程", (p) => lsofText(p.name).toLowerCase(), false],
];

/** @param {SystemView} s */
export function openPorts(s) {
  let list = [], timer = null, loading = false, sort = { key: "port", dir: 1 }, at = null;
  openDrawer(`端口 · ${s.name}`, `${osLabel(s)} · ${ip(s)}`, `
    <div class="drawer-tools">
      <input class="input" id="portQ" placeholder="搜索端口 / 协议 / 地址 / 进程 / PID" autocomplete="off">
      <select id="portProto" aria-label="协议">
        <option value="">全部协议</option>
        <option value="tcp">TCP</option>
        <option value="udp">UDP</option>
      </select>
      <label><input type="checkbox" id="portAuto" checked> 每 5 秒刷新</label>
      <button class="btn" id="portReload">刷新</button>
    </div>
    <div class="drawer-scroll" id="portList"><div class="empty">加载中…</div></div>
    <div class="drawer-foot" id="portFoot">TCP 是正在监听的端口，UDP 是已绑定的端口。进程为 “—” 表示探针读不到该进程。</div>`,
    () => { if (timer) clearInterval(timer); });

  const draw = () => {
    const box = $("#portList");
    if (!box) return;
    const q = $("#portQ").value.trim().toLowerCase();
    const proto = $("#portProto").value;
    const col = PORT_COLS.find((c) => c[0] === sort.key);
    const tokens = q.split(/\s+/).filter(Boolean);
    const rows = list
      .filter((p) => {
        if (proto && p.proto !== proto) return false;
        if (!tokens.length) return true;
        const hay = [p.port, p.proto, p.ip, p.pid || "", lsofText(p.name)].join(" ").toLowerCase();
        return tokens.every((t) => hay.includes(t));
      })
      .sort((a, b) => { const x = col[2](a), y = col[2](b); return (x < y ? -1 : x > y ? 1 : 0) * sort.dir || a.port - b.port; });
    const shown = rows.slice(0, 500);
    const th = ([k, label, , num]) => `<th data-k="${k}" class="${num ? "num" : ""}">${label}<span class="ar">${sort.key === k ? (sort.dir > 0 ? "▲" : "▼") : "↕"}</span></th>`;
    const keepScroll = box.scrollTop;
    box.innerHTML = `<table class="procs ports"><colgroup><col style="width:76px"><col style="width:72px"><col><col style="width:76px"><col style="width:28%"></colgroup><thead><tr>${PORT_COLS.map(th).join("")}</tr></thead><tbody>${shown.map((p) => {
      const name = lsofText(p.name);
      return `<tr>
        <td class="num">${p.port}</td>
        <td>${esc((p.proto || "").toUpperCase())}</td>
        <td class="cmd" title="${esc(p.ip || "")}">${esc(p.ip || "")}</td>
        <td class="num">${p.pid ? p.pid : '<span class="faint">—</span>'}</td>
        <td class="clip" title="${esc(name)}">${name ? esc(name) : '<span class="faint">—</span>'}</td>
      </tr>`;
    }).join("") || `<tr><td colspan="5" class="empty">没有匹配的端口</td></tr>`}</tbody></table>`;
    box.scrollTop = keepScroll;
    box.querySelectorAll("thead th").forEach((h) => h.addEventListener("click", () => {
      const k = h.dataset.k;
      sort = { key: k, dir: sort.key === k ? -sort.dir : (k === "name" || k === "proto" || k === "ip") ? 1 : (k === "port" ? 1 : -1) };
      draw();
    }));
    const tcp = list.filter((p) => p.proto === "tcp").length;
    const udp = list.filter((p) => p.proto === "udp").length;
    const sub = `${osLabel(s)} · ${ip(s)} · TCP ${tcp} · UDP ${udp}${rows.length !== list.length ? `，匹配 ${rows.length}` : ""}${rows.length > shown.length ? `，显示前 ${shown.length}` : ""}${at ? ` · ${at.toLocaleTimeString()} 更新` : ""}`;
    $("#drawerSub").textContent = sub;
  };
  const load = async () => {
    if (loading) return;
    loading = true;
    try {
      list = /** @type {PortInfo[]} */ (await api(`/sessions/${encodeURIComponent(s.id)}/ports`));
      at = new Date();
      draw();
    } catch (e) {
      const b = $("#portList");
      if (b) b.innerHTML = `<div class="empty err">${esc(e.code === "not_implemented" ? "该探针版本不支持端口列表，请升级到 1.25.0" : e.message)}</div>`;
    } finally {
      loading = false;
    }
  };
  $("#portQ").addEventListener("input", draw);
  $("#portProto").addEventListener("change", draw);
  $("#portReload").addEventListener("click", load);
  const auto = () => {
    if (timer) clearInterval(timer);
    timer = $("#portAuto").checked ? setInterval(load, 5000) : null;
  };
  $("#portAuto").addEventListener("change", auto);
  auto();
  load();
  setTimeout(() => { const q = $("#portQ"); if (q) q.focus(); }, 50);
}
