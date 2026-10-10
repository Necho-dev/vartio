// @ts-check
// Port forwards: a port on the hub machine relays to host:port as an agent
// sees it. Rules live on the hub (closing the page keeps them), each local
// port belongs to one rule, and HTTP / WebSocket / any TCP passes through.
import { $, esc, bytes, api, state } from "./core.js";
import { icon } from "./icons.js";
import { openDrawer } from "./ui.js";

/**
 * @typedef {{ id: string, agent: string, name?: string, bind: string, port: number, target: string, note?: string, created: string,
 *   online: boolean, listening: boolean, error?: string, lastError?: string, conns: number, total: number, rxBytes: number, txBytes: number }} ForwardItem
 */

const BIND_NAMES = /** @type {Record<string, string>} */ ({ "127.0.0.1": "仅本机", "0.0.0.0": "局域网" });

/** @param {ForwardItem} f */
const localAddr = (f) => `${f.bind === "0.0.0.0" ? location.hostname : "127.0.0.1"}:${f.port}`;

/** @param {ForwardItem} f */
function status(f) {
  if (f.error) return `<span class="pl-pill warn" title="${esc(f.error)}">监听失败</span>`;
  if (!f.online) return '<span class="pl-pill info">探针离线</span>';
  return '<span class="pl-pill ok">转发中</span>';
}

/**
 * Opens the forward drawer. With a system it manages that machine only;
 * without one it lists every machine's rules (home page entry).
 * @param {SystemView | null} [sys]
 */
export async function openForwardDrawer(sys) {
  const scoped = !!sys;
  let timer = 0;
  openDrawer(scoped ? `隧道转发 · ${sys.name || sys.hostname}` : "隧道转发", "把远端机器上的 TCP 服务映射到 Hub 所在机器的本地端口；HTTP、WebSocket 等基于 TCP 的协议都能直接用。关闭页面不影响转发",
    `<div class="drawer-tools fw-form">
      <div class="fw-row">
        <select class="input" id="fwBind" title="本地监听地址：127.0.0.1 仅 Hub 所在机器可访问；0.0.0.0 同网段任何人无需登录即可访问">
          <option value="127.0.0.1">127.0.0.1</option><option value="0.0.0.0">0.0.0.0</option></select>
        <input class="input" id="fwPort" type="number" min="1" max="65535" step="1" placeholder="本地端口" title="本地端口，留空自动分配">
        <span class="faint">→</span>
        ${scoped ? "" : '<select class="input" id="fwAgent" title="被映射的远端机器"></select>'}
        <input class="input" id="fwHost" value="127.0.0.1" placeholder="远程主机" title="远端机器上看到的地址">
        <input class="input" id="fwRport" type="number" min="1" max="65535" step="1" placeholder="远程端口">
        <input class="input" id="fwNote" placeholder="备注">
        <button class="btn primary" id="fwAdd">${icon("plus")}添加</button>
      </div>
      <p class="err" id="fwErr"></p>
    </div>
    <div class="drawer-scroll plug-drawer"><div id="fwList" class="pl-meta">加载中…</div>
      <p class="pl-meta" style="margin-top:12px">远程主机默认只允许回环地址（127.0.0.1 / localhost）；要转发到远端机器所在网络的其他地址，需在那台探针上配置 <code>GUEST_TUNNEL_ALLOW</code>。</p></div>`,
    () => clearInterval(timer));

  /** @type {ForwardItem[]} */
  let list = [];
  const err = (/** @type {string} */ m, ok = false) => { const e = $("#fwErr"); if (e) { e.className = ok ? "pl-meta ok" : "err"; e.textContent = m; } };

  if (!scoped) {
    /** @type {SystemView[]} */
    let systems = state.systems;
    if (!systems.length) { try { systems = await api("/monitor/systems"); } catch { systems = []; } }
    const on = systems.filter((s) => s.online);
    const sel = /** @type {HTMLSelectElement | null} */ ($("#fwAgent"));
    if (sel) sel.innerHTML = on.length ? on.map((s) => `<option value="${esc(s.id)}">${esc(s.name || s.hostname)}</option>`).join("") : '<option value="">没有在线机器</option>';
  }

  const draw = () => {
    const box = $("#fwList");
    if (!box) return;
    if (!list.length) { box.className = "pl-meta"; box.textContent = scoped ? "这台机器还没有转发规则。" : "还没有转发规则。"; return; }
    box.className = "";
    box.innerHTML = `<table class="pl-table"><tr><th>本地</th>${scoped ? "" : "<th>机器</th>"}<th>远程</th><th>状态</th><th title="当前 / 累计">连接</th><th title="下行 / 上行">流量</th><th></th></tr>
      ${list.map((f) => `<tr>
        <td><span class="mono">${esc(localAddr(f))}</span><span class="pl-pill" title="${f.bind === "0.0.0.0" ? "监听 0.0.0.0，同网段可直接访问" : "监听 127.0.0.1，只有 Hub 所在机器能访问"}">${BIND_NAMES[f.bind] || esc(f.bind)}</span>${f.note ? `<div class="faint">${esc(f.note)}</div>` : ""}</td>
        ${scoped ? "" : `<td><a href="#/system/${encodeURIComponent(f.agent)}">${esc(f.name || f.agent)}</a></td>`}
        <td class="mono">${esc(f.target)}</td>
        <td>${status(f)}${f.lastError ? `<div class="faint clip" style="max-width:220px" title="${esc(f.lastError)}">最近失败：${esc(f.lastError)}</div>` : ""}</td>
        <td class="mono">${f.conns} / ${f.total}</td>
        <td class="mono faint">↓${bytes(f.rxBytes)} ↑${bytes(f.txBytes)}</td>
        <td class="acts"><button class="btn sm" data-copy="${esc(localAddr(f))}" title="复制本地地址">${icon("copy")}</button><button class="btn sm danger" data-del="${esc(f.id)}">${icon("trash")}删除</button></td>
      </tr>`).join("")}</table>`;
    box.querySelectorAll("[data-copy]").forEach((/** @type {HTMLElement} */ b) => b.addEventListener("click", async () => {
      try { await navigator.clipboard.writeText(b.dataset.copy || ""); b.innerHTML = icon("check"); } catch { prompt("本地地址", b.dataset.copy); }
    }));
    box.querySelectorAll("[data-del]").forEach((/** @type {HTMLElement} */ b) => b.addEventListener("click", async () => {
      const f = list.find((x) => x.id === b.dataset.del);
      if (!f || !confirm(`删除转发 ${localAddr(f)} → ${f.target}？正在使用的连接会立即断开。`)) return;
      try { await api(`/forwards/${encodeURIComponent(f.id)}`, { method: "DELETE" }); await load(); } catch (e) { err(e.message); }
    }));
  };

  const load = async () => {
    try {
      const r = await api("/forwards" + (scoped ? `?agent=${encodeURIComponent(sys.id)}` : ""));
      list = r.forwards;
      draw();
    } catch (e) { err(e.message); }
  };

  $("#fwAdd").addEventListener("click", async () => {
    err("");
    const val = (/** @type {string} */ id) => /** @type {HTMLInputElement} */ ($(id))?.value.trim() || "";
    const agent = scoped ? sys.id : val("#fwAgent");
    const port = val("#fwPort") ? Number(val("#fwPort")) : 0;
    const rport = Number(val("#fwRport"));
    const host = val("#fwHost") || "127.0.0.1";
    if (!agent) return err("请选择机器");
    if (!Number.isInteger(port) || port < 0 || port > 65535) return err("本地端口应为 1–65535，留空自动分配");
    if (!Number.isInteger(rport) || rport < 1 || rport > 65535) return err("请填写远程端口（1–65535）");
    if (port) {
      // Server checks across all machines; the drawer may show only one.
      const all = scoped ? (await api("/forwards")).forwards : list;
      const dup = all.find((/** @type {ForwardItem} */ f) => f.port === port);
      if (dup) return err(`本地端口 ${port} 已被 ${dup.name || dup.agent} 的 ${dup.target} 转发占用`);
    }
    const btn = /** @type {HTMLButtonElement} */ ($("#fwAdd"));
    btn.disabled = true;
    try {
      const target = host.includes(":") && !host.startsWith("[") ? `[${host}]:${rport}` : `${host}:${rport}`;
      const f = await api("/forwards", { method: "POST", json: { agent, bind: val("#fwBind"), port, target, note: val("#fwNote") } });
      /** @type {HTMLInputElement} */ ($("#fwPort")).value = "";
      /** @type {HTMLInputElement} */ ($("#fwNote")).value = "";
      await load();
      err(`已添加：${localAddr(f)} → ${f.target}`, true);
    } catch (e) {
      const m = /** @type {any} */ (e).code === "agent_policy" ? `${e.message}（远端探针只允许回环地址，可在那台机器上配置 GUEST_TUNNEL_ALLOW）` : e.message;
      err(m);
    } finally { btn.disabled = false; }
  });

  await load();
  timer = setInterval(() => { if ($("#fwList")) load(); else clearInterval(timer); }, 3000);
}
