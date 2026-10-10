// @ts-check
// Plugin center (#/plugins), plugin pages (#/plugins/{name}?agent=…) and the
// per-host plugin drawer. A plugin's UI is an ES module served by the hub at
// item.ui; it exports mount(el, ctx) and may return a cleanup function. The
// host picks the agent, so a plugin only ever renders for one agent.
import { $, app, esc, html, raw, api } from "./core.js";
import { icon } from "./icons.js";
import { openDrawer } from "./ui.js";

/**
 * @typedef {{ id: string, hostname?: string, os?: string, version?: string, ready: boolean, missing?: string[] }} PluginAgent
 * @typedef {{ name: string, title: string, version: string, description?: string, primitives: string[], targets?: string[], installed: boolean, ui?: string, agents: PluginAgent[] }} PluginItem
 * @typedef {{ name: string, agent: string, embedded: boolean, params: URLSearchParams,
 *   api: (path: string, opts?: any) => Promise<any>, base: string, open: (name: string, params?: Record<string, string>) => void }} PluginCtx
 */

const PRIM_NAMES = /** @type {Record<string, string>} */ ({
  "tunnel.tcp": "TCP 隧道", "fs.which": "程序查找", "fs.read": "读小文件", "proc.spawn": "进程启动", "net.listeners": "端口监听",
});

/** @type {(() => void) | null} */
let cleanup = null;
export function unmountPlugin() {
  if (cleanup) { try { cleanup(); } catch { /* plugin bug must not break routing */ } }
  cleanup = null;
}

/** @returns {Promise<PluginItem[]>} */
export const listPlugins = () => api("/plugins");

/** @param {PluginItem} p */
const readyCount = (p) => p.agents.filter((a) => a.ready).length;

/** @param {string} name @param {Record<string, string>} [params] */
export function pluginHref(name, params) {
  const q = new URLSearchParams(params || {}).toString();
  return `#/plugins/${encodeURIComponent(name)}${q ? "?" + q : ""}`;
}

// ---------- plugin center ----------
export async function pluginCenter() {
  app.innerHTML = html`<a class="back" href="#/">${raw(icon("arrow-left"))}返回全部主机</a>
    <section class="card"><div class="card-head"><div><h2>插件中心</h2><p>插件运行在 Hub 上，经探针的通用能力（隧道、程序查找、读小文件、进程启动、端口监听）操作远端机器；卸载后接口与页面立即停用</p></div></div>
    <div class="plug-grid" id="plugGrid"><div class="empty">加载中…</div></div></section>`;
  await drawCenter();
}

async function drawCenter() {
  const box = $("#plugGrid");
  /** @type {PluginItem[]} */
  let list;
  try { list = await listPlugins(); } catch (e) {
    if (box && e.message !== "unauthorized") box.innerHTML = `<div class="empty err">${esc(e.message)}</div>`;
    return;
  }
  if (!box) return;
  if (!list.length) { box.innerHTML = `<div class="empty">没有内置插件</div>`; return; }
  box.innerHTML = list.map((p) => {
    const n = readyCount(p);
    return `<article class="plug-card${p.installed ? " on" : ""}" data-name="${esc(p.name)}">
      <div class="plug-top"><span class="plug-ic">${icon(p.name === "cdp" ? "globe" : "grid-2x2-plus")}</span>
        <div class="plug-title"><b>${esc(p.title)}</b><span class="faint mono">${esc(p.name)} · v${esc(p.version)}</span></div>
        <span class="badge ${p.installed ? "up" : ""}">${p.installed ? "已安装" : "未安装"}</span></div>
      <p class="plug-desc">${esc(p.description || "")}</p>
      <div class="plug-prims">${p.primitives.map((c) => `<span class="tag" title="${esc(c)}">${esc(PRIM_NAMES[c] || c)}</span>`).join("")}</div>
      <div class="plug-foot"><span class="faint">${p.agents.length ? `${n} / ${p.agents.length} 台在线探针可用` : "暂无在线探针"}</span>
        <span class="plug-btns">
          ${p.installed
            ? `${p.ui ? `<a class="btn primary sm" href="${pluginHref(p.name)}">${icon("external-link")}打开</a>` : ""}<button class="btn danger sm" data-act="uninstall">卸载</button>`
            : `<button class="btn primary sm" data-act="install">${icon("plus")}安装</button>`}
        </span></div>
      ${p.agents.some((a) => !a.ready) ? `<details class="plug-miss"><summary class="faint">${p.agents.length - n} 台探针缺少所需能力</summary>${p.agents.filter((a) => !a.ready).map((a) => `<div class="faint">${esc(a.hostname || a.id)}（${esc(a.version || "?")}）缺 ${esc((a.missing || []).join("、"))}</div>`).join("")}<div class="faint">升级探针到 1.16.0 或以上</div></details>` : ""}
    </article>`;
  }).join("");
  box.querySelectorAll("[data-act]").forEach((b) => b.addEventListener("click", async () => {
    const card = /** @type {HTMLElement} */ (b.closest(".plug-card"));
    const name = card.dataset.name || "";
    const on = /** @type {HTMLElement} */ (b).dataset.act === "install";
    if (!on && !confirm(`卸载插件 ${name}？它的接口与页面会停用，正在进行的连接会被断开。`)) return;
    /** @type {HTMLButtonElement} */ (b).disabled = true;
    try {
      await api("/plugins/" + encodeURIComponent(name), { method: on ? "POST" : "DELETE" });
      await drawCenter();
    } catch (e) { alert(e.message); /** @type {HTMLButtonElement} */ (b).disabled = false; }
  }));
}

// ---------- plugin page ----------
/** @param {string} name @param {URLSearchParams} params */
export async function pluginPage(name, params) {
  app.innerHTML = html`<a class="back" href="#/plugins">${raw(icon("arrow-left"))}插件中心</a><div id="plugPage"><div class="empty">加载中…</div></div>`;
  /** @type {PluginItem} */
  let p;
  try { p = await api("/plugins/" + encodeURIComponent(name)); } catch (e) {
    if (e.message !== "unauthorized") $("#plugPage").innerHTML = `<div class="empty err">${esc(e.message)}</div>`;
    return;
  }
  const page = $("#plugPage");
  if (!page) return;
  if (!p.installed) { page.innerHTML = html`<div class="empty">插件 ${p.title} 未安装，请先在<a href="#/plugins">插件中心</a>安装</div>`.toString(); return; }
  if (!p.ui) { page.innerHTML = html`<div class="empty">插件 ${p.title} 没有页面，接口见 ${"/v1/probe/plugins/" + p.name}</div>`.toString(); return; }
  const ready = p.agents.filter((a) => a.ready);
  let agent = params.get("agent") || "";
  if (!ready.some((a) => a.id === agent)) agent = ready[0] ? ready[0].id : "";
  page.innerHTML = `<section class="card plug-page">
    <div class="card-head"><div><h2>${esc(p.title)}</h2><p>${esc(p.description || "")}</p></div>
      <label class="plug-agent">探针 <select class="input" id="plugAgent">${ready.map((a) => `<option value="${esc(a.id)}"${a.id === agent ? " selected" : ""}>${esc(a.hostname || a.id)}${a.os ? " · " + esc(a.os) : ""}</option>`).join("")}</select></label></div>
    <div class="plug-body" id="plugBody"></div></section>`;
  const body = $("#plugBody");
  if (!agent) {
    body.innerHTML = `<div class="empty">没有满足 ${esc(p.primitives.join("、"))} 的在线探针${p.agents.length ? "（需要探针 1.16.0 或以上）" : ""}</div>`;
    return;
  }
  const go = (/** @type {string} */ id) => {
    params.set("agent", id);
    history.replaceState(null, "", pluginHref(name, Object.fromEntries(params)));
    return mountInto(body, p, id, false, params);
  };
  $("#plugAgent").addEventListener("change", (/** @type {any} */ e) => { params.delete("port"); go(e.target.value); });
  await go(agent);
}

/**
 * @param {HTMLElement} el @param {PluginItem} p @param {string} agent
 * @param {boolean} embedded @param {URLSearchParams} [params]
 */
async function mountInto(el, p, agent, embedded, params) {
  unmountPlugin();
  el.innerHTML = `<div class="empty">加载插件…</div>`;
  /** @type {any} */
  let mod;
  try { mod = await import(/* @vite-ignore */ p.ui || ""); } catch (e) {
    el.innerHTML = `<div class="empty err">插件页面加载失败：${esc(e.message)}</div>`;
    return;
  }
  el.innerHTML = "";
  const base = "/plugins/" + encodeURIComponent(p.name);
  /** @type {PluginCtx} */
  const ctx = {
    name: p.name, agent, embedded, base, params: params || new URLSearchParams(),
    api: (path, opts) => api(base + path, opts),
    open: (name, q) => { location.hash = pluginHref(name, q); },
  };
  try {
    const off = await mod.mount(el, ctx);
    cleanup = typeof off === "function" ? off : null;
  } catch (e) {
    el.innerHTML = `<div class="empty err">${esc(e.message)}</div>`;
  }
}

// ---------- host drawer ----------
/** @param {SystemView} s */
export async function openPluginDrawer(s) {
  openDrawer(`插件 · ${s.name}`, "已安装且本机探针可用的插件", `<div class="drawer-tools" id="plugTabs"><span class="faint">加载中…</span></div><div class="drawer-scroll plug-drawer" id="plugMount"></div>`, unmountPlugin);
  /** @type {PluginItem[]} */
  let list;
  try { list = await listPlugins(); } catch (e) { $("#plugTabs").innerHTML = `<span class="err">${esc(e.message)}</span>`; return; }
  const usable = list.filter((p) => p.installed && p.ui && p.agents.some((a) => a.id === s.id && a.ready));
  const tabs = $("#plugTabs");
  if (!tabs) return;
  if (!usable.length) {
    const why = list.some((p) => p.installed) ? "已安装的插件在这台探针上都不可用（需要探针 1.16.0 或以上）" : "还没有安装插件";
    tabs.innerHTML = `<span class="faint">${why}</span><a class="btn sm" href="#/plugins">${icon("grid-2x2-plus")}插件中心</a>`;
    return;
  }
  tabs.innerHTML = `<div class="seg">${usable.map((p, i) => `<button data-n="${esc(p.name)}" class="${i ? "" : "on"}">${esc(p.title)}</button>`).join("")}</div>
    <a class="btn sm" id="plugFull" href="${pluginHref(usable[0].name, { agent: s.id })}">${icon("external-link")}在页面中打开</a>`;
  const show = (/** @type {PluginItem} */ p) => {
    tabs.querySelectorAll(".seg button").forEach((b) => b.classList.toggle("on", /** @type {HTMLElement} */ (b).dataset.n === p.name));
    $("#plugFull").setAttribute("href", pluginHref(p.name, { agent: s.id }));
    return mountInto($("#plugMount"), p, s.id, true);
  };
  tabs.querySelectorAll(".seg button").forEach((b) => b.addEventListener("click", () => {
    const p = usable.find((x) => x.name === /** @type {HTMLElement} */ (b).dataset.n);
    if (p) show(p);
  }));
  await show(usable[0]);
}
