// @ts-check
// Docker / Podman containers on the detail page: live table, image list,
// history charts, inspect / compose drawers, logs, and exec through the terminal.
import { $, state, esc, bytes, rate, api, ago } from "./core.js";
import { renderChart } from "./charts.js";
import { icon } from "./icons.js";
import { openTerm } from "./term.js";
import { openDrawer } from "./ui.js";

const SPAN = { "1h": 3600e3, "6h": 6 * 3600e3, "24h": 86400e3, "7d": 7 * 86400e3, "30d": 30 * 86400e3 };
const COLORS = ["#3b82f6", "#22c55e", "#f97316", "#8b5cf6", "#06b6d4", "#ec4899", "#eab308", "#14b8a6", "#a3e635", "#f43f5e"];
const STATES = { running: "运行中", exited: "已退出", created: "已创建", paused: "已暂停", restarting: "重启中", removing: "删除中", dead: "异常" };
const HEALTH = { healthy: ["健康", "ok"], unhealthy: ["不健康", "warn"], starting: ["检查中", "info"] };
const LOG_RANGE = [["3600", "最近 1 小时"], ["21600", "最近 6 小时"], ["86400", "最近 24 小时"], ["0", "全部"]];
const LOG_TAIL = [["200", "200 行"], ["500", "500 行"], ["1000", "1000 行"], ["5000", "5000 行"]];

const PAGE = 10;
const UP_UNIT = { second: 1, minute: 60, hour: 3600, day: 86400, week: 604800, month: 2592000, year: 31536000 };

/** @type {SystemView | null} */
let current = null;
let showAll = false;
let showUnused = false;
let ctrPage = 1;
let imgPage = 1;

/** Docker-runner systems treat the page as a container fleet, not a host. */
export function isDockerRunner(/** @type {SystemView} */ s) {
  return s.runner === "docker";
}

/** @param {{ inset?: boolean }} [opts] inset charts stay under the table on host pages. */
export function containersSection(opts) {
  const inset = opts?.inset !== false;
  return `<section class="card panel ctr-card" id="ctrs" hidden>
    <div class="ctr-head"><div><h3>${icon("container")}容器</h3><p class="desc" id="ctrDesc"></p></div>
      <div class="ctr-tools"><div class="pager" id="ctrPager"></div>
      <label class="ctr-all"><input type="checkbox" id="ctrAll"> 显示已停止</label></div></div>
    <div class="ctr-wrap"><table class="kv-table ctr-table" id="ctrTable"></table></div>
    ${inset ? `<div class="grid2 ctr-charts">
      <div class="ctr-chart"><span class="now" id="now-ccpu"></span><h4>CPU</h4><p class="desc">各容器 CPU（堆积），100% 为一个核心，与 docker stats 一致</p><div class="chart" id="c-ccpu"></div><div class="legend" id="lg-ccpu"></div></div>
      <div class="ctr-chart"><span class="now" id="now-cmem"></span><h4>内存</h4><p class="desc">各容器内存（堆积），不含可回收的页缓存</p><div class="chart" id="c-cmem"></div><div class="legend" id="lg-cmem"></div></div>
    </div>` : ""}
  </section>
  <section class="card panel ctr-card" id="imgs" hidden>
    <div class="ctr-head"><div><h3>${icon("package")}镜像</h3><p class="desc" id="imgDesc"></p></div>
      <div class="ctr-tools"><div class="pager" id="imgPager"></div>
      <label class="ctr-all"><input type="checkbox" id="imgAll"> 显示未使用</label></div></div>
    <div class="ctr-wrap"><table class="kv-table ctr-table" id="imgTable"></table></div>
  </section>`;
}

/** @param {SystemView} s */
export function renderContainers(s) {
  current = s;
  const box = $("#ctrs");
  const d = s.latest?.docker;
  if (!box) return;
  box.hidden = !d;
  if (!d) { const imgs = $("#imgs"); if (imgs) imgs.hidden = true; return; }
  const all = $("#ctrAll");
  if (all && !all.dataset.bound) {
    all.dataset.bound = "1";
    all.checked = showAll;
    all.addEventListener("change", () => { showAll = all.checked; ctrPage = 1; if (current) renderContainers(current); });
    $("#ctrTable").addEventListener("click", onAction);
    $("#ctrs").addEventListener("click", (ev) => onPager(ev, "ctr"));
    const imgs = $("#imgs");
    if (imgs && !imgs.dataset.bound) { imgs.dataset.bound = "1"; imgs.addEventListener("click", (ev) => onPager(ev, "img")); }
    const imgAll = $("#imgAll");
    if (imgAll && !imgAll.dataset.bound) {
      imgAll.dataset.bound = "1";
      imgAll.checked = showUnused;
      imgAll.addEventListener("change", () => { showUnused = /** @type {HTMLInputElement} */ (imgAll).checked; imgPage = 1; if (current) renderContainers(current); });
    }
  }
  const live = s.online;
  $("#ctrDesc").innerHTML = [d.engine && esc(d.engine + (d.version ? " " + d.version : "")), `运行 <b>${d.running}</b> / 共 ${d.total}`,
    d.error ? `<span class="ctr-err">${icon("triangle-alert")}引擎无响应：${esc(d.error)}</span>` : "",
    !live ? `<span class="faint">离线，显示最后一次采样</span>` : ""].filter(Boolean).join(" · ");
  const rows = d.containers.filter((c) => showAll || c.state === "running")
    .sort((a, b) => startedAt(b) - startedAt(a) || a.name.localeCompare(b.name));
  const pg = pageOf(rows.length, ctrPage);
  ctrPage = pg.page;
  const view = rows.slice(pg.start, pg.start + PAGE);
  const exec = (s.caps || []).includes("docker.exec");
  const logsOK = live && (s.caps || []).includes("docker");
  const inspectOK = live && (s.caps || []).includes("docker.inspect");
  paintPager("ctrPager", "ctr", pg, rows.length);
  if (!rows.length) {
    $("#ctrTable").innerHTML = `<tr><td class="faint">${d.total ? "没有运行中的容器，勾选“显示已停止”查看全部" : "没有容器"}</td></tr>`;
  } else {
    $("#ctrTable").innerHTML = `<tr><th>容器名称</th><th>状态</th><th>镜像</th><th>CPU</th><th>内存</th><th>端口</th><th class="ctr-act">操作</th></tr>`
      + view.map((c) => {
        const run = c.state === "running";
        const h = c.health && HEALTH[c.health];
        const mem = c.memUsed ? `${bytes(c.memUsed)}${c.memLimit ? `<span class="faint"> / ${bytes(c.memLimit)}</span>` : ""}` : "—";
        const termTip = !live ? "探针离线" : !run ? "容器未运行" : exec ? "容器终端（docker exec）" : "探针未开放容器终端：需在探针环境变量设置 GUEST_DOCKER=rw";
        return `<tr class="${run ? "" : "ctr-off"}">
        <td><div class="ctr-name">${esc(c.name)}</div>
          <div class="ctr-sub"><span class="faint">${esc(c.id)}</span>${c.project ? `<button class="tag" data-act="compose" data-project="${esc(c.project)}" title="查看 Compose 编排">${esc(c.project)}</button>` : ""}</div></td>
        <td><span class="pl-pill ${run ? "ok" : c.state === "dead" || c.state === "restarting" ? "warn" : ""}" title="${esc(c.status || "")}">${STATES[c.state] || esc(c.state)}</span>${h ? `<span class="pl-pill ${h[1]}">${h[0]}</span>` : ""}<div class="faint ctr-status">${esc(c.status || "")}</div></td>
        <td><div class="ctr-img" title="${esc(c.image)}">${esc(c.image)}</div>${c.update ? `<span class="pl-pill info" title="镜像仓库中该标签已有更新的版本，拉取并重建容器即可升级">可更新</span>` : ""}</td>
        <td>${c.cpuPct != null ? c.cpuPct.toFixed(1) + "%" : "—"}</td>
        <td>${mem}</td>
        <td>${portChips(c.ports) || "—"}</td>
        <td class="ctr-act"><div class="ctr-acts"><button class="btn sm" data-act="inspect" data-name="${esc(c.name)}" ${inspectOK ? "" : "disabled"} title="${inspectOK ? "查看容器配置（不含环境变量）" : live ? "需升级探针到 1.18.0" : "探针离线"}">${icon("info")}详情</button>
          <button class="btn sm" data-act="logs" data-name="${esc(c.name)}" ${logsOK ? "" : "disabled"} title="${logsOK ? "查看容器日志" : "探针离线"}">${icon("scroll-text")}日志</button>
          <button class="btn sm" data-act="exec" data-name="${esc(c.name)}" ${live && run && exec ? "" : "disabled"} title="${esc(termTip)}">${icon("square-terminal")}终端</button></div></td></tr>`;
      }).join("");
  }
  renderImages(d);
}

/** @param {DockerSample} d */
function renderImages(d) {
  const box = $("#imgs");
  if (!box) return;
  const containers = d.containers || [];
  const usedRank = (/** @type {DockerImage} */ i) => (i.inUse > 0 ? 1 : 0);
  const imgs = (d.images || []).slice().sort((a, b) => usedRank(b) - usedRank(a) || (b.created || 0) - (a.created || 0) || a.name.localeCompare(b.name));
  box.hidden = !imgs.length;
  if (!imgs.length) { paintPager("imgPager", "img", pageOf(0, 1), 0); return; }
  const rows = imgs.filter((i) => showUnused || i.inUse > 0);
  const pg = pageOf(rows.length, imgPage);
  imgPage = pg.page;
  const used = imgs.filter((i) => i.inUse > 0).length;
  $("#imgDesc").textContent = `${imgs.length} 个镜像 · ${used} 个被容器使用`;
  paintPager("imgPager", "img", pg, rows.length);
  if (!rows.length) {
    $("#imgTable").innerHTML = `<tr><td class="faint">没有使用中的镜像，勾选“显示未使用”查看全部</td></tr>`;
    return;
  }
  $("#imgTable").innerHTML = `<tr><th>镜像名称</th><th>状态</th><th>大小</th><th>创建时间</th><th>更新检查</th><th>关联容器</th></tr>`
    + rows.slice(pg.start, pg.start + PAGE).map((i) => {
      const rel = relatedContainers(i, containers);
      const outdated = rel.some((c) => c.update);
      const when = i.created ? new Date(i.created * 1000).toISOString() : "";
      return `<tr class="${i.inUse ? "" : "ctr-off"}"><td><div class="ctr-name">${esc(i.name)}</div>
      <div class="faint ctr-img" title="${esc((i.tags || []).join("\n"))}">${esc(i.id)}${(i.tags || []).length > 1 ? " · " + (i.tags.length - 1) + " 个标签" : ""}</div></td>
      <td><span class="pl-pill ${i.inUse ? "ok" : ""}">${i.inUse ? "已使用" : "未使用"}</span></td>
      <td>${bytes(i.size)}</td>
      <td class="ctr-when">${when ? `<div>${ago(when)}</div><div class="faint">${fmtStamp(i.created)}</div>` : "—"}</td>
      <td>${outdated ? `<span class="pl-pill info" title="使用该镜像的容器检测到仓库有更新">可更新</span>` : `<span class="faint">—</span>`}</td>
      <td>${relHTML(rel.map((c) => c.name))}</td></tr>`;
    }).join("");
}

/** @param {number} total @param {number} page */
function pageOf(total, page) {
  const pages = Math.max(1, Math.ceil(total / PAGE) || 1);
  const p = Math.min(Math.max(1, page), pages);
  return { page: p, pages, start: (p - 1) * PAGE };
}

/** @param {string} id @param {string} which @param {{ page: number, pages: number }} pg @param {number} total */
function paintPager(id, which, pg, total) {
  const el = $("#" + id);
  if (!el) return;
  el.innerHTML = total ? `<span class="faint">共 ${total} 条</span>
    <button type="button" class="icon-btn" data-pager="${which}" data-dir="-1" ${pg.page <= 1 ? "disabled" : ""} title="上一页">${icon("chevron-left")}</button>
    <span class="pager-n">${pg.page} / ${pg.pages}</span>
    <button type="button" class="icon-btn" data-pager="${which}" data-dir="1" ${pg.page >= pg.pages ? "disabled" : ""} title="下一页">${icon("chevron-right")}</button>` : "";
}

/** @param {MouseEvent} ev @param {"ctr" | "img"} which */
function onPager(ev, which) {
  const b = /** @type {HTMLButtonElement | null} */ (/** @type {HTMLElement} */ (ev.target).closest("button[data-pager]"));
  if (!b || b.disabled || b.dataset.pager !== which || !current) return;
  const dir = Number(b.dataset.dir) || 0;
  if (which === "ctr") ctrPage += dir;
  else imgPage += dir;
  renderContainers(current);
}

/** Docker's "Up 29 hours" is the time since start, at the engine's rounding. */
function startedAt(/** @type {ContainerSample} */ c) {
  const up = upSeconds(c.status);
  if (c.state === "running" && up != null) return Date.now() / 1000 - up;
  return c.created || 0;
}

/** @param {string | undefined} status */
function upSeconds(status) {
  const s = status || "";
  if (/^Up\s+Less than a second/i.test(s)) return 0;
  const m = s.match(/^Up\s+(?:About\s+)?(?:an?\s+)?(\d+)?\s*(second|minute|hour|day|week|month|year)s?/i);
  if (!m) return null;
  const n = m[1] ? Number(m[1]) : 1;
  const unit = /** @type {keyof typeof UP_UNIT} */ (m[2].toLowerCase());
  return n * (UP_UNIT[unit] || 0);
}

/** @param {number | undefined} sec */
function fmtStamp(sec) {
  if (!sec) return "";
  const d = new Date(sec * 1000);
  if (Number.isNaN(+d)) return "";
  const p = (/** @type {number} */ n) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

/** @param {DockerImage} img @param {ContainerSample[]} containers */
function relatedContainers(img, containers) {
  const keys = new Set([img.name, img.id, ...(img.tags || [])].filter(Boolean));
  return containers.filter((c) => keys.has(c.image));
}

/** @param {string[]} names */
function relHTML(names) {
  if (!names.length) return "—";
  const rest = names.slice(1);
  const more = rest.length
    ? `<span class="rel-tag more" title="${esc(rest.join("\n"))}">+${rest.length}</span>`
    : "";
  return `<span class="ctr-rel"><span class="rel-tag" title="${esc(names[0])}">${esc(names[0])}</span>${more}</span>`;
}

/** @param {string | undefined} s */
function portChips(s) {
  const parts = (s || "").split(/,\s*/).filter(Boolean);
  if (!parts.length) return "";
  return `<div class="port-list">${parts.map((p) => {
    const m = p.match(/^(?:(\[[^\]]+\]|[^:]+):)?(\d+)→(\d+)\/(\w+)$/);
    if (!m) return `<span class="port-chip" title="端口映射（主机 → 容器）">${esc(p)}</span>`;
    const [, ip, pub, priv, proto] = m;
    const tip = (ip ? ip + ":" : "") + pub + " → " + priv + "/" + proto;
    return `<span class="port-chip" title="${esc(tip)}">${ip && ip !== "127.0.0.1" && ip !== "0.0.0.0" ? `<em>${esc(ip)}</em>` : ""}<b>${esc(pub)}</b><i>→</i>${esc(priv)}/${esc(proto)}</span>`;
  }).join("")}</div>`;
}

/** @param {MouseEvent} ev */
function onAction(ev) {
  const b = /** @type {HTMLButtonElement | null} */ (/** @type {HTMLElement} */ (ev.target).closest("button[data-act]"));
  if (!b || b.disabled || !current) return;
  if (b.dataset.act === "inspect") { openInspect(current, b.dataset.name || ""); return; }
  if (b.dataset.act === "compose") { openCompose(current, b.dataset.project || ""); return; }
  if (b.dataset.act === "logs") { openLogs(current, b.dataset.name || ""); return; }
  openTerm(current, { mode: "exec", container: b.dataset.name || "" });
}

/** @param {SystemView} s @param {string} name */
async function openInspect(s, name) {
  const live = (s.latest?.docker?.containers || []).find((c) => c.name === name);
  openDrawer(`容器 · ${name}`, [live?.image, live?.id].filter(Boolean).join(" · "), `<div class="faint ctr-pad">加载中…</div>`);
  const gen = $("#drawer").dataset.gen;
  /** @type {any} */
  let d;
  try {
    d = await api(`/sessions/${encodeURIComponent(s.id)}/docker/inspect?container=${encodeURIComponent(name)}`);
  } catch (e) {
    if ($("#drawer").dataset.gen !== gen) return;
    $("#drawerBody").innerHTML = `<div class="ctr-err ctr-pad">${icon("triangle-alert")}${esc(e.message)}</div>`;
    return;
  }
  if ($("#drawer").hidden || $("#drawer").dataset.gen !== gen) return;
  const cfg = d.Config || {}, host = d.HostConfig || {}, st = d.State || {};
  const cmd = [...(cfg.Entrypoint || []), ...(cfg.Cmd || [])].join(" ");
  const rp = host.RestartPolicy?.Name;
  const nets = Object.entries(d.NetworkSettings?.Networks || {}).map(([n, v]) => `${n}${v.IPAddress ? " · " + v.IPAddress : ""}`);
  const mounts = (d.Mounts || []).map((m) => ({ src: m.Source || m.Name || "", dst: m.Destination, ro: m.RW === false }));
  const labels = Object.entries(cfg.Labels || {});
  const time = (/** @type {string} */ t) => (t && !t.startsWith("0001") ? new Date(t).toLocaleString() : "");
  const run = !!st.Running;
  const health = live?.health && HEALTH[live.health];
  const row = (k, v) => (v === "" || v == null ? "" : `<tr><th>${k}</th><td>${typeof v === "string" ? esc(v) : v}</td></tr>`);
  const section = (title, body) => body ? `<section class="ctr-sec"><h4>${title}</h4>${body}</section>` : "";
  $("#drawerBody").innerHTML = `<div class="ctr-drawer">
    <div class="ctr-facts">
      <span class="pl-pill ${run ? "ok" : ""}">${run ? "运行中" : (STATES[live?.state || ""] || "已停止")}</span>
      ${health ? `<span class="pl-pill ${health[1]}">${health[0]}</span>` : ""}
      ${live?.project ? `<button class="tag" id="ctrCompose" title="查看编排">${esc(live.project)}</button>` : ""}
      ${live?.service ? `<span class="faint">${esc(live.service)}</span>` : ""}
    </div>
    ${section("基本", `<table class="kv-table info-table ctr-inspect">${[
      row("ID", (d.Id || "").slice(0, 12)), row("镜像", cfg.Image), row("镜像 ID", (d.Image || "").replace("sha256:", "").slice(0, 12)),
      row("命令", cmd), row("工作目录", cfg.WorkingDir), row("用户", cfg.User),
      row("创建", time(d.Created)), row("启动", time(st.StartedAt)), row("退出码", st.Running ? "" : st.ExitCode),
      row("重启策略", rp && rp !== "no" ? rp : ""), row("重启次数", d.RestartCount || ""),
    ].join("")}</table>`)}
    ${section("网络", `<table class="kv-table info-table ctr-inspect">${[
      row("网络模式", host.NetworkMode), row("网络", nets.join("\n")),
    ].join("")}</table>${portChips(live?.ports)}`)}
    ${section("存储", mounts.length ? `<table class="kv-table info-table ctr-inspect">${mounts.map((m) =>
      `<tr><th>${esc(m.dst)}</th><td>${esc(m.src)}${m.ro ? " <span class=\"pl-pill\">只读</span>" : ""}</td></tr>`).join("")}</table>` : "")}
    ${section("资源", `<table class="kv-table info-table ctr-inspect">${[
      row("内存限制", host.Memory ? bytes(host.Memory) : ""), row("CPU 限制", host.NanoCpus ? (host.NanoCpus / 1e9) + " 核" : ""),
      row("特权模式", host.Privileged ? "是" : ""),
    ].join("")}</table>`)}
    ${section("标签", labels.length ? `<table class="kv-table info-table ctr-inspect">${labels.map(([k, v]) =>
      `<tr><th>${esc(k)}</th><td>${esc(String(v))}</td></tr>`).join("")}</table>` : "")}
    <p class="faint ctr-note">${icon("info")}出于安全考虑，环境变量（Config.Env）已在探针端剔除。</p>
  </div>`;
  $("#drawerActs").innerHTML = `<button class="icon-btn" id="ctrJson" title="下载原始 JSON">${icon("download")}</button>`;
  $("#ctrJson").addEventListener("click", () => downloadJSON(name + ".json", d));
  const btn = $("#ctrCompose");
  if (btn && live?.project) btn.addEventListener("click", () => openCompose(s, live.project || ""));
}

/** @param {string} filename @param {unknown} data */
function downloadJSON(filename, data) {
  const a = document.createElement("a");
  a.href = URL.createObjectURL(new Blob([JSON.stringify(data, null, 2)], { type: "application/json" }));
  a.download = filename.replace(/[\\/]/g, "_");
  a.click();
  URL.revokeObjectURL(a.href);
}

/** @param {SystemView} s @param {string} project */
async function openCompose(s, project) {
  if (!project) return;
  openDrawer(`编排 · ${project}`, "Docker Compose", `<div class="faint ctr-pad">加载中…</div>`, null, { wide: true });
  /** @type {{ project: string, file?: string, content?: string, services?: string[], error?: string }} */
  let d;
  try {
    d = await api(`/sessions/${encodeURIComponent(s.id)}/docker/compose?project=${encodeURIComponent(project)}`);
  } catch (e) {
    $("#drawerBody").innerHTML = `<div class="ctr-err ctr-pad">${icon("triangle-alert")}${esc(e.message)}</div>`;
    return;
  }
  const svcs = (d.services || []).map((x) => `<span class="pl-pill">${esc(x)}</span>`).join("");
  $("#drawerBody").innerHTML = `<div class="cmp-drawer">
    <div class="ctr-facts">${svcs || `<span class="faint">未解析到服务名</span>`}${d.file ? `<span class="faint" title="${esc(d.file)}">${esc(d.file)}</span>` : ""}</div>
    ${d.error && !d.content ? `<div class="ctr-err ctr-pad">${icon("triangle-alert")}${esc(d.error)}</div>` : ""}
    ${d.content ? `<div class="cmp-src" id="cmpSrc">${highlightYAML(d.content)}</div>` : ""}
    ${d.error && d.content ? `<p class="faint ctr-note">${icon("info")}${esc(d.error)}</p>` : ""}
  </div>`;
}

/** @param {string} src */
function highlightYAML(src) {
  return src.split("\n").map((line, i) => {
    let inner;
    if (/^\s*#/.test(line)) inner = `<span class="yk-cmt">${esc(line) || " "}</span>`;
    else {
      const m = line.match(/^(\s*(?:-\s+)?)([\w./-]+)(:)(.*)$/);
      inner = m
        ? `${esc(m[1])}<span class="yk-key">${esc(m[2])}</span>${esc(m[3])}<span class="yk-val">${esc(m[4])}</span>`
        : esc(line) || " ";
    }
    return `<div class="yk-line"><span class="yk-n">${i + 1}</span><span class="yk-t">${inner}</span></div>`;
  }).join("");
}

/** @param {SystemView} s @param {string} name */
function openLogs(s, name) {
  let raw = "", followWS = /** @type {WebSocket | null} */ (null), stick = true, closed = false;
  openDrawer(`日志 · ${name}`, s.name, `<div class="log-drawer">
    <div class="drawer-tools">
      <select id="logSince">${LOG_RANGE.map(([v, n]) => `<option value="${v}">${n}</option>`).join("")}</select>
      <select id="logTail">${LOG_TAIL.map(([v, n]) => `<option value="${v}" ${v === "500" ? "selected" : ""}>${n}</option>`).join("")}</select>
      <input class="input" id="logQ" placeholder="过滤（模糊匹配）" autocomplete="off">
      <label><input type="checkbox" id="logFollow"> 追踪</label>
      <button class="btn sm" id="logReload">${icon("refresh-cw")}刷新</button>
      <button class="btn sm" id="logDown">${icon("download")}下载</button>
    </div>
    <div class="log-view" id="logView"><div class="faint">加载中…</div></div>
    <div class="drawer-foot" id="logFoot"></div>
  </div>`, () => { closed = true; if (followWS) followWS.close(); }, { wide: true });

  const view = () => $("#logView");
  const q = () => /** @type {HTMLInputElement} */ ($("#logQ")).value.trim();
  const sinceSec = () => Number(/** @type {HTMLSelectElement} */ ($("#logSince")).value) || 0;
  const tail = () => Number(/** @type {HTMLSelectElement} */ ($("#logTail")).value) || 500;
  const paint = () => {
    const box = view();
    if (!box) return;
    const needle = q().toLowerCase();
    const lines = raw.split("\n").filter((l) => l && (!needle || l.toLowerCase().includes(needle)));
    box.innerHTML = lines.length ? lines.map((l) => logLineHTML(l, q())).join("") : `<div class="faint">没有匹配的日志</div>`;
    $("#logFoot").textContent = `${lines.length} 行${needle ? "（已过滤）" : ""}${followWS ? " · 追踪中" : ""}`;
    if (stick) box.scrollTop = box.scrollHeight;
  };
  const stopFollow = () => { if (followWS) { followWS.close(); followWS = null; } };
  const load = async () => {
    if (closed) return;
    stopFollow();
    const since = sinceSec() ? Math.floor(Date.now() / 1000) - sinceSec() : 0;
    try {
      const d = /** @type {{ text: string }} */ (await api(`/sessions/${encodeURIComponent(s.id)}/docker/logs?container=${encodeURIComponent(name)}&tail=${tail()}${since ? "&since=" + since : ""}`));
      raw = d.text || "";
    } catch (e) {
      raw = "";
      view().innerHTML = `<div class="ctr-err">${icon("triangle-alert")}${esc(e.message)}</div>`;
      $("#logFoot").textContent = "";
      return;
    }
    paint();
    if (/** @type {HTMLInputElement} */ ($("#logFollow")).checked) startFollow(Math.floor(Date.now() / 1000));
  };
  const startFollow = async (since) => {
    stopFollow();
    try {
      const tk = await api(`/sessions/${encodeURIComponent(s.id)}/term`, { method: "POST", json: { mode: "logs", container: name, tail: 0, since } });
      if (closed) return;
      const proto = location.protocol === "https:" ? "wss:" : "ws:";
      const ws = new WebSocket(`${proto}//${location.host}${tk.ws}&cols=120&rows=32`);
      followWS = ws;
      ws.binaryType = "arraybuffer";
      ws.onopen = () => paint();
      let buf = "";
      ws.onmessage = (ev) => {
        buf += typeof ev.data === "string" ? ev.data : new TextDecoder().decode(ev.data);
        buf = buf.replace(/\r\n/g, "\n").replace(/\r/g, "\n");
        const parts = buf.split("\n");
        buf = parts.pop() || "";
        if (parts.length) { raw += (raw && !raw.endsWith("\n") ? "\n" : "") + parts.join("\n") + "\n"; paint(); }
      };
      ws.onclose = () => { if (followWS === ws) followWS = null; paint(); };
    } catch (e) {
      $("#logFoot").textContent = e.message;
    }
  };

  view().addEventListener("scroll", () => {
    const el = view();
    stick = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
  });
  $("#logReload").addEventListener("click", load);
  $("#logQ").addEventListener("input", paint);
  $("#logSince").addEventListener("change", load);
  $("#logTail").addEventListener("change", load);
  $("#logFollow").addEventListener("change", () => {
    if (/** @type {HTMLInputElement} */ ($("#logFollow")).checked) load();
    else { stopFollow(); paint(); }
  });
  $("#logDown").addEventListener("click", () => {
    const needle = q().toLowerCase();
    const text = raw.split("\n").filter((l) => l && (!needle || l.toLowerCase().includes(needle))).join("\n");
    const a = document.createElement("a");
    a.href = URL.createObjectURL(new Blob([text || raw], { type: "text/plain" }));
    a.download = name + ".log";
    a.click();
    URL.revokeObjectURL(a.href);
  });
  load();
}

/** @param {string} line @param {string} q */
function logLineHTML(line, q) {
  let ts = "", body = line;
  const m = line.match(/^(\d{4}-\d{2}-\d{2}T[0-9:.+-Z]+)\s+(.*)$/);
  if (m) { ts = m[1]; body = m[2]; }
  let lvl = "";
  if (/\b(error|fatal|panic|fail(ed|ure)?)\b/i.test(body)) lvl = "err";
  else if (/\b(warn(ing)?)\b/i.test(body)) lvl = "warn";
  else if (/\b(info|debug|trace)\b/i.test(body)) lvl = "info";
  return `<div class="log-line ${lvl}">${ts ? `<span class="log-ts">${esc(fmtLogTs(ts))}</span>` : ""}<span class="log-msg">${highlightLogMsg(body, q)}</span></div>`;
}

/** @param {string} body @param {string} q */
function highlightLogMsg(body, q) {
  let html = esc(body);
  html = html.replace(/\b(?:\d{1,3}\.){3}\d{1,3}\b/g, `<span class="log-ip">$&</span>`);
  html = html.replace(/&quot;((?:GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)\s+[^&]*)&quot;/g, `<span class="log-str">&quot;$1&quot;</span>`);
  html = html.replace(/\b(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)\b/g, `<span class="log-meth">$1</span>`);
  html = html.replace(/(HTTP\/\d(?:\.\d)?)((?:&quot;)?\s+)([1-5]\d{2})\b/g, (_, proto, sp, code) => {
    const n = Number(code);
    return `${proto}${sp}<span class="log-st ${n >= 500 ? "err" : n >= 400 ? "warn" : "ok"}">${code}</span>`;
  });
  html = html.replace(/\b(FATAL|ERROR|PANIC)\b/g, `<span class="log-st err">$1</span>`);
  html = html.replace(/\b(WARNING|WARN)\b/g, `<span class="log-st warn">$1</span>`);
  html = html.replace(/\b(LOG|INFO|DEBUG|TRACE)\b/g, `<span class="log-st ok">$1</span>`);
  if (q) html = html.replace(new RegExp(escRe(q), "gi"), "<mark>$&</mark>");
  return html;
}

/** @param {string} s */
const escRe = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

/** @param {string} ts */
function fmtLogTs(ts) {
  const d = new Date(ts);
  return Number.isNaN(+d) ? ts : d.toLocaleString([], { hour12: false, month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit" })
    + "." + String(d.getMilliseconds()).padStart(3, "0");
}

/** @param {SystemView} s */
export async function loadContainerSeries(s) {
  const main = isDockerRunner(s);
  if (!s.latest?.docker && !main) return;
  const from = Date.now() - SPAN[state.range], to = Date.now();
  const empty = !state.config.history ? "未配置 PROBE_HUB_DSN，无历史数据" : "暂无容器数据";
  if (!state.config.history) {
    paintContainerCharts(s, { t: [], containers: [] }, { from, to, empty });
    return;
  }
  /** @type {ContainerSeries} */
  let d;
  try {
    d = await api(`/monitor/systems/${encodeURIComponent(s.id)}/containers/series?range=${state.range}&top=8`);
  } catch (e) { return; }
  paintContainerCharts(s, d, { from, to, empty, stepSec: d.stepSec });
}

/**
 * Host pages keep a pair of inset CPU/mem charts under the table.
 * Docker-runner pages own the main CPU / mem / disk I/O / net cards.
 * @param {SystemView} s
 * @param {{ t: number[], containers: ContainerSeries["containers"] }} d
 * @param {{ from: number, to: number, empty: string, stepSec?: number }} span
 */
function paintContainerCharts(s, d, span) {
  const main = isDockerRunner(s);
  const base = { t: d.t, stepSec: span.stepSec, from: span.from, to: span.to, stacked: true, totalLabel: "合计", empty: span.empty };
  const color = (/** @type {number} */ i) => COLORS[i % COLORS.length];
  const cpuFmt = (/** @type {number | null} */ v) => (v == null ? "—" : (v >= 100 ? v.toFixed(0) : v.toFixed(1)) + "%");
  const lines = d.containers || [];
  /** @param {(c: ContainerSeries["containers"][number]) => (number | null)[]} field */
  const series = (field) => lines.map((c, i) => ({ label: c.name, color: color(i), data: field(c), width: 1.2 }));
  const lg = lines.map((c, i) => `<span><i style="background:${color(i)}"></i>${esc(c.name)}</span>`).join("");
  const run = (s.latest?.docker?.containers || []).filter((c) => c.stats);
  const sum = (/** @type {(c: ContainerSample) => number | undefined} */ f) => run.reduce((n, c) => n + (f(c) || 0), 0);
  const now = s.online && run.length;

  /** @param {string} id @param {ChartOpts} opts @param {string} [text] */
  const paint = (id, opts, text) => {
    const box = $("#c-" + id);
    if (!box) return;
    renderChart(box, opts);
    const n = $("#now-" + id);
    if (n && text !== undefined) n.textContent = text;
    const l = $("#lg-" + id);
    if (l) l.innerHTML = lg;
  };

  if (main) {
    paint("cpu", { ...base, fmt: cpuFmt, series: series((c) => c.cpu) }, now ? cpuFmt(sum((c) => c.cpuPct)) : "");
    paint("mem", { ...base, fmt: bytes, capacity: s.latest?.memTotal || undefined, series: series((c) => c.mem) },
      now ? `${bytes(sum((c) => c.memUsed))}${s.latest?.memTotal ? " / " + bytes(s.latest.memTotal) : ""}` : "");
    paint("dio", { ...base, fmt: rate, series: series((c) => c.blkRead) }, now ? `读 ${rate(sum((c) => c.blkReadBps))}` : "");
    paint("diow", { ...base, fmt: rate, series: series((c) => c.blkWrite) }, now ? `写 ${rate(sum((c) => c.blkWriteBps))}` : "");
    const n = (/** @type {number | null} */ v) => (v == null ? "—" : Math.round(v) + "");
    paint("cprocs", { ...base, fmt: n, series: series((c) => c.pids || []) }, now ? n(sum((c) => c.pids)) + " 个" : "");
    paint("cnet", { ...base, fmt: rate, series: series((c) => c.netRx) }, now ? `↓ ${rate(sum((c) => c.netRxBps))}` : "");
    paint("cnettx", { ...base, fmt: rate, series: series((c) => c.netTx) }, now ? `↑ ${rate(sum((c) => c.netTxBps))}` : "");
    const netNow = $("#now-cnet");
    if (netNow) netNow.textContent = now ? `↓ ${rate(sum((c) => c.netRxBps))} · ↑ ${rate(sum((c) => c.netTxBps))}` : "";
    const dioNow = $("#now-dio");
    if (dioNow) dioNow.textContent = now ? `读 ${rate(sum((c) => c.blkReadBps))} · 写 ${rate(sum((c) => c.blkWriteBps))}` : "";
    return;
  }
  paint("ccpu", { ...base, fmt: cpuFmt, series: series((c) => c.cpu) }, now ? cpuFmt(sum((c) => c.cpuPct)) : "");
  paint("cmem", { ...base, fmt: bytes, series: series((c) => c.mem) }, now ? bytes(sum((c) => c.memUsed)) : "");
}
