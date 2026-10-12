// @ts-check
import { $, esc, osLabel, osKey, ip, api, bytes, ago } from "./core.js";
import { openDrawer } from "./ui.js";

/**
 * @typedef {{ name: string, value: number, other?: boolean, tip: string }} MemTile
 * @typedef {MemTile & { x: number, y: number, w: number, h: number }} MemRect
 */

const SOURCE = {
  appstore: "App Store",
  homebrew: "Homebrew",
  store: "Microsoft Store",
  developer: "旁加载",
  flatpak: "Flatpak",
  snap: "Snap",
  deb: "软件包",
  rpm: "软件包",
  apk: "软件包",
  appimage: "AppImage",
};

/** @param {string} os @param {string} [arch] */
function archLabel(os, arch) {
  if (os === "darwin" && arch === "arm64") return "Apple 芯片";
  return { universal: "通用", arm64: "ARM64", x64: "x64", x86: "x86", neutral: "中性" }[arch] || "";
}

/** @param {string} [source] */
const sourceLabel = (source) => SOURCE[source] || "";

/** @param {AppInfo} a */
function statusText(a) {
  if (!a.running) return "";
  return a.memMB ? "运行中 · " + bytes(a.memMB * 1024 * 1024) : "运行中";
}

/** @param {AppInfo} a */
function statusTip(a) {
  if (!a.running) return "";
  const bits = [];
  if (a.procs) bits.push(a.procs + " 个进程");
  if (a.cpuPct) bits.push(a.cpuPct.toFixed(1) + "% CPU");
  if (a.memMB) bits.push(bytes(a.memMB * 1024 * 1024));
  return bits.join(" · ");
}

/** @param {AppInfo} a @param {string} os */
function tip(a, os) {
  return [a.publisher, a.id, a.path, a.installed ? "添加于 " + a.installed.slice(0, 10) : "", archLabel(os, a.arch)].filter(Boolean).join(" · ");
}

/** @type {[key: string, label: string, sortKey: (p: AppInfo, os: string) => string | number, numeric: boolean][]} */
const APP_COLS = [
  ["name", "名称", (a) => (a.name || "").toLowerCase(), false],
  ["version", "版本", (a) => (a.version || "").toLowerCase(), false],
  ["size", "大小", (a) => a.size || 0, true],
  ["status", "状态", (a) => a.running ? a.memMB || 1 : 0, true],
  ["arch", "架构", (a, os) => archLabel(os, a.arch), false],
  ["used", "上次打开", (a) => a.lastUsed || "", false],
  ["source", "来源", (a) => sourceLabel(a.source), false],
  ["path", "路径", (a) => (a.path || "").toLowerCase(), false],
];

/** @param {SystemView} s */
export function openApps(s) {
  let list = /** @type {AppInfo[]} */ ([]), loading = false, sort = { key: "name", dir: 1 }, at = null;
  openDrawer(`应用 · ${s.name}`, `${osLabel(s)} · ${ip(s)}`, `
    <div class="drawer-tools">
      <input class="input" id="appQ" placeholder="搜索名称 / 版本 / 路径 / 来源" autocomplete="off">
      <button class="btn" id="appReload">刷新</button>
    </div>
    <div class="drawer-scroll" id="appList"><div class="empty">加载中…</div></div>
    <div class="app-mem" id="appMem" hidden>
      <div class="app-mem-head"><span title="各应用路径内的进程内存相加。共享页会重复计算，也不含系统进程，所以通常大于主机已用内存">运行中应用的进程内存</span><span id="appMemSum"></span></div>
      <div class="app-mem-map" id="appMemMap"></div>
    </div>
    <div class="drawer-foot" id="appFoot">只列出图形界面里的应用。容器和没有桌面的机器这里是空的。运行状态按当前进程是否落在应用路径里计算。</div>`,
    () => { memApps = []; if (memObs) { memObs.disconnect(); memObs = null; } }, { wide: true });

  const draw = () => {
    const box = $("#appList");
    if (!box) return;
    const q = $("#appQ").value.trim().toLowerCase();
    const tokens = q.split(/\s+/).filter(Boolean);
    const col = APP_COLS.find((c) => c[0] === sort.key);
    const rows = list
      .filter((a) => {
        if (!tokens.length) return true;
        const hay = [a.name, a.version, a.publisher, a.path, a.id, archLabel(osKey(s), a.arch), sourceLabel(a.source), statusText(a), a.lastUsed ? ago(a.lastUsed) : ""].join(" ").toLowerCase();
        return tokens.every((t) => hay.includes(t));
      })
      .sort((a, b) => {
        const x = col[2](a, osKey(s)), y = col[2](b, osKey(s));
        return (x < y ? -1 : x > y ? 1 : 0) * sort.dir || a.name.localeCompare(b.name);
      });
    const shown = rows.slice(0, 500);
    const th = ([k, label, , num]) => `<th data-k="${k}" class="${num ? "num" : ""}">${label}<span class="ar">${sort.key === k ? (sort.dir > 0 ? "▲" : "▼") : "↕"}</span></th>`;
    const keepTop = box.scrollTop, keepLeft = box.scrollLeft;
    box.innerHTML = `<table class="procs apps"><colgroup><col class="c-name"><col class="c-ver"><col class="c-size"><col class="c-status"><col class="c-arch"><col class="c-used"><col class="c-source"><col class="c-path"></colgroup><thead><tr>${APP_COLS.map(th).join("")}</tr></thead><tbody>${shown.map((a) => `<tr>
        <td class="app-name" title="${esc(tip(a, osKey(s)))}"><span class="nm-row"><span class="nm">${esc(a.name)}</span>${a.running ? '<span class="dot up" title="运行中"></span>' : ""}</span></td>
        <td class="cmd">${a.version ? esc(a.version) : '<span class="faint">—</span>'}</td>
        <td class="num">${a.size ? bytes(a.size) : '<span class="faint">—</span>'}</td>
        <td class="status" title="${esc(statusTip(a))}">${statusText(a) ? esc(statusText(a)) : '<span class="faint">—</span>'}</td>
        <td>${esc(archLabel(osKey(s), a.arch)) || '<span class="faint">—</span>'}</td>
        <td title="${esc(a.lastUsed || "")}">${a.lastUsed ? esc(ago(a.lastUsed)) : '<span class="faint">—</span>'}</td>
        <td>${sourceLabel(a.source) ? esc(sourceLabel(a.source)) : '<span class="faint">—</span>'}</td>
        <td class="path" title="${esc(a.path || "")}">${a.path ? esc(a.path) : '<span class="faint">—</span>'}</td>
      </tr>`).join("") || `<tr><td colspan="8" class="empty">没有图形应用</td></tr>`}</tbody></table>`;
    box.scrollTop = keepTop;
    box.scrollLeft = keepLeft;
    box.querySelectorAll("thead th").forEach((h) => h.addEventListener("click", () => {
      const k = h.dataset.k;
      sort = { key: k, dir: sort.key === k ? -sort.dir : (k === "size" || k === "status" || k === "used") ? -1 : 1 };
      draw();
    }));
    const running = list.filter((a) => a.running).length;
    $("#drawerSub").textContent = `${osLabel(s)} · ${ip(s)} · ${list.length} 个应用${running ? ` · ${running} 个运行中` : ""}${rows.length !== list.length ? `，匹配 ${rows.length}` : ""}${rows.length > shown.length ? `，显示前 ${shown.length}` : ""}${at ? ` · ${at.toLocaleTimeString()} 更新` : ""}`;
    memApps = list;
    layoutMem();
  };
  const load = async () => {
    if (loading) return;
    loading = true;
    try {
      list = /** @type {AppInfo[]} */ (await api(`/sessions/${encodeURIComponent(s.id)}/apps`)) || [];
      at = new Date();
      draw();
    } catch (e) {
      const b = $("#appList");
      if (b) b.innerHTML = `<div class="empty err">${esc(e.code === "not_implemented" ? "该探针版本不支持应用列表，请升级到 1.27.2" : e.message)}</div>`;
    } finally {
      loading = false;
    }
  };
  $("#appQ").addEventListener("input", draw);
  $("#appReload").addEventListener("click", load);
  watchMem();
  load();
  setTimeout(() => { const q = $("#appQ"); if (q) q.focus(); }, 50);
}

/** @type {AppInfo[]} */
let memApps = [];
/** @type {ResizeObserver | null} */
let memObs = null;

function watchMem() {
  const map = $("#appMemMap");
  if (!map) return;
  if (memObs) memObs.disconnect();
  memObs = new ResizeObserver(() => layoutMem());
  memObs.observe(map);
}

/** @param {AppInfo[]} apps */
function memGroups(apps) {
  const running = apps.filter((a) => a.running && a.memMB > 0).sort((a, b) => b.memMB - a.memMB);
  const total = running.reduce((s, a) => s + a.memMB, 0);
  if (!(total > 0)) return null;
  /** @type {AppInfo[]} */
  const named = [];
  /** @type {AppInfo[]} */
  const rest = [];
  for (let i = 0; i < running.length; i++) {
    const a = running[i];
    const keep = named.length < 8 && (i < 4 || a.memMB / total >= 0.04);
    (keep ? named : rest).push(a);
  }
  if (rest.length === 1) {
    const last = rest.pop();
    if (last) named.push(last);
  }
  /** @type {MemTile[]} */
  const tiles = named.map((a) => ({
    name: a.name,
    value: a.memMB,
    tip: [a.name, bytes(a.memMB * 1024 * 1024), a.procs ? a.procs + " 个进程" : "", a.cpuPct ? a.cpuPct.toFixed(1) + "% CPU" : ""].filter(Boolean).join(" · "),
  }));
  if (rest.length) {
    const mb = rest.reduce((s, a) => s + a.memMB, 0);
    tiles.push({
      name: "其他",
      value: mb,
      other: true,
      tip: rest.map((a) => a.name + " " + bytes(a.memMB * 1024 * 1024)).join("\n"),
    });
  }
  return { total, tiles };
}

function layoutMem() {
  const wrap = $("#appMem");
  const map = $("#appMemMap");
  if (!wrap || !map) return;
  const groups = memGroups(memApps);
  if (!groups) {
    wrap.hidden = true;
    map.replaceChildren();
    return;
  }
  wrap.hidden = false;
  const sum = $("#appMemSum");
  if (sum) sum.textContent = "共 " + bytes(groups.total * 1024 * 1024);
  const w = map.clientWidth, h = map.clientHeight;
  if (w < 8 || h < 8) return;
  const q = ($("#appQ") && $("#appQ").value || "").trim().toLowerCase();
  const pad = 1.5;
  map.replaceChildren();
  for (const [i, r] of treemap(groups.tiles, w, h).entries()) {
    const tiny = r.w < 64 || r.h < 36;
    const short = r.h < 54;
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = "app-mem-tile" + (r.other ? " other" : "") + (!r.other && q === r.name.toLowerCase() ? " on" : "") + (tiny ? " tiny" : "");
    btn.dataset.i = String(Math.min(i, 7));
    btn.title = r.tip;
    btn.style.left = (r.x + pad) + "px";
    btn.style.top = (r.y + pad) + "px";
    btn.style.width = Math.max(0, r.w - pad * 2) + "px";
    btn.style.height = Math.max(0, r.h - pad * 2) + "px";
    if (!tiny) {
      const nm = document.createElement("span");
      nm.className = "nm";
      nm.textContent = r.name;
      btn.appendChild(nm);
      if (!short) {
        const sz = document.createElement("span");
        sz.className = "sz";
        sz.textContent = bytes(r.value * 1024 * 1024);
        btn.appendChild(sz);
      }
    }
    if (!r.other) btn.addEventListener("click", () => {
      const input = $("#appQ");
      if (!input) return;
      input.value = input.value.trim().toLowerCase() === r.name.toLowerCase() ? "" : r.name;
      input.dispatchEvent(new Event("input"));
    });
    map.appendChild(btn);
  }
}

/**
 * Squarified treemap. `value` stays in the caller's unit; layout uses area.
 * @param {MemTile[]} items
 * @param {number} w
 * @param {number} h
 * @returns {MemRect[]}
 */
function treemap(items, w, h) {
  /** @type {MemRect[]} */
  const out = [];
  const total = items.reduce((s, it) => s + it.value, 0);
  if (!(total > 0) || w < 8 || h < 8) return out;
  const area = w * h;
  /** @type {{ tile: MemTile, area: number }[]} */
  let rest = items.map((tile) => ({ tile, area: (tile.value / total) * area }));
  let x = 0, y = 0, cw = w, ch = h;
  while (rest.length && cw >= 1 && ch >= 1) {
    const side = Math.min(cw, ch);
    /** @type {{ tile: MemTile, area: number }[]} */
    const row = [];
    while (rest.length) {
      const next = row.concat(rest[0]);
      if (row.length && worst(next, side) > worst(row, side)) break;
      const item = rest.shift();
      if (!item) break;
      row.push(item);
    }
    const sum = row.reduce((s, r) => s + r.area, 0);
    // Row sits on the shorter side so tiles stay near square.
    if (cw >= ch) {
      const rw = sum / ch;
      let ry = y;
      for (const r of row) {
        const rh = r.area / rw;
        out.push({ ...r.tile, x, y: ry, w: rw, h: rh });
        ry += rh;
      }
      x += rw;
      cw -= rw;
    } else {
      const rh = sum / cw;
      let rx = x;
      for (const r of row) {
        const rw = r.area / rh;
        out.push({ ...r.tile, x: rx, y, w: rw, h: rh });
        rx += rw;
      }
      y += rh;
      ch -= rh;
    }
  }
  return out;
}

/** @param {{ area: number }[]} row @param {number} side */
function worst(row, side) {
  const s = row.reduce((a, r) => a + r.area, 0);
  let hi = 0, lo = Infinity;
  for (const r of row) {
    if (r.area > hi) hi = r.area;
    if (r.area < lo) lo = r.area;
  }
  const s2 = s * s, w2 = side * side;
  return Math.max((w2 * hi) / s2, s2 / (w2 * lo));
}
