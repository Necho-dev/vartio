// @ts-check
// Plugin center (#/plugins), plugin pages (#/plugins/{name}?agent=…) and the
// per-host plugin drawer. A plugin's UI is an ES module served by the hub at
// item.ui; it exports mount(el, ctx) and may return a cleanup function. The
// host picks the agent, so a plugin only ever renders for one agent.
import { $, app, esc, html, raw, api } from "./core.js";
import { icon } from "./icons.js";
import { openDrawer, openModal, closeModal } from "./ui.js";

/**
 * @typedef {{ id: string, hostname?: string, os?: string, version?: string, ready: boolean, missing?: string[] }} PluginAgent
 * @typedef {{ name: string, title: string, version: string, description?: string, primitives: string[], targets?: string[], machines?: string[], installed: boolean, status?: string, error?: string, ui?: string, repo?: string, official?: boolean, origin?: string, agents: PluginAgent[] }} PluginItem
 * @typedef {{ id: string, name: string, url: string, keyId?: string, plugins: number, error?: string }} PluginRepo
 * @typedef {{ official?: { name: string }, repos: PluginRepo[], manual: { name: string, url: string, sha256?: string, signingKeyId?: string }[] }} PluginSources
 * @typedef {{ name: string, agent: string, embedded: boolean, params: URLSearchParams,
 *   api: (path: string, opts?: any) => Promise<any>, base: string, open: (name: string, params?: Record<string, string>) => void }} PluginCtx
 */

const PRIM_NAMES = /** @type {Record<string, string>} */ ({
  "tunnel.tcp": "TCP 隧道", "fs.which": "程序查找", "fs.read": "读小文件", "proc.spawn": "进程启动", "net.listeners": "端口监听",
  machine: "不适用这台机器",
});
const MACHINE_NAMES = /** @type {Record<string, string>} */ ({
  mac: "Mac", windows: "Windows", linux: "Linux", docker: "Docker", vmware: "VMware",
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
/** @type {"all" | "installed" | "settings"} */
let centerTab = "all";
let centerQuery = "";
/** @type {PluginItem[]} */
let catalog = [];
/** @type {PluginSources} */
let sources = { repos: [], manual: [] };
let centerPoll = 0;

function tabFromHash() {
  const t = new URLSearchParams((location.hash.split("?")[1]) || "").get("tab");
  return t === "installed" || t === "settings" ? t : "all";
}

/** In-page confirm. Resolves true only when the primary button is pressed. */
function askChoice(/** @type {{ title: string, body: string, ok: string, danger?: boolean }} */ opts) {
  return new Promise((resolve) => {
    let settled = false;
    const finish = (/** @type {boolean} */ yes) => {
      if (settled) return;
      settled = true;
      closeModal();
      resolve(yes);
    };
    openModal(opts.title, `<p class="plug-ask">${esc(opts.body)}</p>
      <div class="form-actions"><button type="button" class="btn" id="plugNo">取消</button>
      <button type="button" class="btn ${opts.danger ? "danger-solid" : "primary"}" id="plugYes">${esc(opts.ok)}</button></div>`,
      () => finish(false), { small: true });
    $("#plugNo").addEventListener("click", () => finish(false));
    $("#plugYes").addEventListener("click", () => finish(true));
    $("#plugYes").focus();
  });
}

/** In-page notice. Replaces window.alert. */
function tell(/** @type {string} */ title, /** @type {string} */ body) {
  return new Promise((resolve) => {
    let settled = false;
    const finish = () => {
      if (settled) return;
      settled = true;
      closeModal();
      resolve(undefined);
    };
    openModal(title, `<p class="plug-ask">${esc(body)}</p>
      <div class="form-actions"><button type="button" class="btn primary" id="plugYes">知道了</button></div>`,
      finish, { small: true });
    $("#plugYes").addEventListener("click", finish);
    $("#plugYes").focus();
  });
}

export async function pluginCenter() {
  clearTimeout(centerPoll);
  centerTab = tabFromHash();
  app.innerHTML = html`<a class="back" href="#/">${raw(icon("arrow-left"))}返回全部主机</a>
    <div class="plug-center">
      <div class="plug-bar">
        <div class="seg" id="plugTabs">
          <button type="button" data-tab="all">${raw(icon("package"))}插件目录</button>
          <button type="button" data-tab="installed">${raw(icon("check"))}已安装<span class="plug-count" id="plugInstalledN"></span></button>
          <button type="button" data-tab="settings">${raw(icon("settings"))}设置</button>
        </div>
        <div class="plug-tools" id="plugTools">
          <label class="search">${raw(icon("search"))}<input id="plugQ" placeholder="搜索插件或能力" value="${esc(centerQuery)}" autocomplete="off"></label>
          <button type="button" class="icon-btn" id="plugRefresh" title="刷新">${raw(icon("refresh-cw"))}</button>
        </div>
      </div>
      <p class="plug-note" id="plugNote">${raw(icon("info"))}安装会下载已签名的 .vtp 安装包，核对整包 SHA-256 和 Ed25519 签名后再启动。插件本身的问题请到卡片上的仓库入口提交。</p>
      <div id="plugMain"><div class="empty">加载中…</div></div>
    </div>`;
  $("#plugTabs").querySelectorAll("[data-tab]").forEach((b) => b.addEventListener("click", () => {
    centerTab = /** @type {"all" | "installed" | "settings"} */ (/** @type {HTMLElement} */ (b).dataset.tab || "all");
    const q = centerTab === "all" ? "" : "?tab=" + centerTab;
    history.replaceState(null, "", "#/plugins" + q);
    paintCenter();
    if (centerTab === "settings") drawCenter(true);
  }));
  $("#plugQ").addEventListener("input", (/** @type {any} */ e) => { centerQuery = e.target.value; paintCenter(); });
  $("#plugRefresh").addEventListener("click", () => drawCenter(true));
  await drawCenter(true);
}

/** @param {boolean} [reloadSources] */
async function drawCenter(reloadSources) {
  clearTimeout(centerPoll);
  const box = $("#plugMain");
  if (reloadSources || centerTab === "settings") {
    try { sources = await api("/plugins/_sources"); } catch (e) {
      if (e.message === "unauthorized") return;
      if (centerTab === "settings" && box) box.innerHTML = `<div class="empty err">${esc(e.message)}</div>`;
    }
  }
  try { catalog = await listPlugins(); } catch (e) {
    if (box && e.message !== "unauthorized" && centerTab !== "settings") box.innerHTML = `<div class="empty err">${esc(e.message)}</div>`;
    return;
  }
  if (!box) return;
  paintCenter();
  if (centerTab !== "settings" && catalog.some((p) => p.status === "installing")) centerPoll = window.setTimeout(() => drawCenter(false), 1500);
}

function paintCenter() {
  const box = $("#plugMain");
  const nEl = $("#plugInstalledN");
  const note = $("#plugNote");
  if (!box) return;
  $("#plugTabs").querySelectorAll("[data-tab]").forEach((b) => b.classList.toggle("on", /** @type {HTMLElement} */ (b).dataset.tab === centerTab));
  const installedN = catalog.filter((p) => p.installed).length;
  if (nEl) nEl.textContent = installedN ? String(installedN) : "";
  if (note) note.hidden = centerTab === "settings";
  if (centerTab === "settings") { paintSettings(box); return; }
  const q = centerQuery.trim().toLowerCase();
  const list = catalog.filter((p) => {
    if (centerTab === "installed" && !p.installed) return false;
    if (!q) return true;
    const hay = [p.title, p.name, p.description || "", p.origin || "", ...(p.machines || []).map((c) => MACHINE_NAMES[c] || c), ...(p.primitives || []).map((c) => PRIM_NAMES[c] || c)].join(" ").toLowerCase();
    return hay.includes(q);
  });
  if (!catalog.length) { box.innerHTML = `<div class="empty">目录里没有可安装的插件</div>`; return; }
  if (!list.length) {
    box.innerHTML = `<div class="empty">${centerTab === "installed" && !q ? "还没有安装插件" : "没有匹配的插件"}</div>`;
    return;
  }
  box.innerHTML = `<div class="plug-grid">${list.map(cardHTML).join("")}</div>`;
  box.querySelectorAll("[data-act]").forEach((b) => b.addEventListener("click", () => onPluginAct(/** @type {HTMLElement} */ (b))));
  clampTags(box);
}

/** @param {PluginItem} p */
function cardHTML(p) {
  const n = readyCount(p);
  const badge = p.status === "installing" ? "安装中" : p.status === "error" ? "安装失败" : p.installed ? "已安装" : "";
  const badgeCls = p.status === "ready" ? "up" : p.status === "error" ? "down" : "";
  const buttons = p.status === "installing"
    ? `<button class="btn sm" disabled>安装中…</button>`
    : p.status === "ready"
      ? `${p.ui ? `<a class="btn primary sm" href="${pluginHref(p.name)}">${icon("external-link")}打开</a>` : ""}<button class="btn sm" data-act="uninstall">卸载</button>`
      : `${p.status === "error" ? `<button class="btn sm" data-act="install">重试</button>` : `<button class="btn primary sm" data-act="install">安装</button>`}${p.installed ? `<button class="btn sm" data-act="uninstall">卸载</button>` : ""}`;
  const repo = p.repo ? `<a class="icon-btn" href="${esc(p.repo)}" target="_blank" rel="noopener" title="GitHub">${icon("github")}</a>` : "";
  const origin = p.official
    ? `<span class="plug-official">${icon("badge-check")}Vartio 官方</span>`
    : `<span class="faint">${esc(p.origin || "自定义")}</span>`;
  const ver = p.version ? `v${esc(p.version)} · ` : "";
  const machines = (p.machines || []).map((c) => `<span class="tag">${esc(MACHINE_NAMES[c] || c)}</span>`).join("");
  const prims = (p.primitives || []).map((c) => `<span class="tag" title="${esc(PRIM_NAMES[c] || c)}">${esc(PRIM_NAMES[c] || c)}</span>`).join("");
  const blocked = p.agents.filter((a) => !a.ready);
  const needUpgrade = blocked.some((a) => (a.missing || []).some((c) => c !== "machine"));
  return `<article class="plug-card" data-name="${esc(p.name)}">
    <div class="plug-top"><span class="plug-ic tone-${esc(p.name)}">${icon(p.name === "cdp" ? "globe" : p.name === "bitbrowser" ? "grid-2x2-plus" : "package")}</span>
      <div class="plug-title"><b>${esc(p.title)}</b>${origin}</div>
      ${badge ? `<span class="badge ${badgeCls}">${badge}</span>` : ""}
      ${repo}</div>
    <div class="plug-prims">${machines}${prims}</div>
    <p class="plug-desc" title="${esc(p.description || "")}">${esc(p.description || "")}</p>
    ${p.error ? `<p class="plug-err">${esc(p.error)}</p>` : ""}
    <div class="plug-foot"><span class="faint">${ver}${p.agents.length ? `${n} / ${p.agents.length} 台可用` : "暂无在线探针"}</span>
      <span class="plug-btns">${buttons}</span></div>
    ${blocked.length ? `<details class="plug-miss"><summary class="faint">${blocked.length} 台探针不可用</summary>${blocked.map((a) => `<div class="faint">${esc(a.hostname || a.id)}（${esc(a.version || "?")}）缺 ${esc((a.missing || []).map((c) => PRIM_NAMES[c] || c).join("、"))}</div>`).join("")}${needUpgrade ? `<div class="faint">升级探针到 1.16.0 或以上</div>` : ""}</details>` : ""}
  </article>`;
}

/** @param {HTMLElement} b */
async function onPluginAct(b) {
  const card = /** @type {HTMLElement} */ (b.closest(".plug-card"));
  const name = card.dataset.name || "";
  const p = catalog.find((x) => x.name === name);
  const title = p ? p.title : name;
  const install = b.dataset.act === "install";
  const yes = await askChoice(install
    ? { title: `安装${title}`, body: `将下载「${title}」的已签名安装包，核对 SHA-256 和签名后启动。`, ok: "安装" }
    : { title: `卸载${title}`, body: `「${title}」的进程会停掉，已下载的安装包会被删除，正在进行的连接会被断开。`, ok: "卸载", danger: true });
  if (!yes) return;
  b.disabled = true;
  b.textContent = install ? "安装中…" : "卸载中…";
  try {
    await api("/plugins/" + encodeURIComponent(name), { method: install ? "POST" : "DELETE" });
    await drawCenter();
  } catch (e) {
    await tell(install ? "安装失败" : "卸载失败", e.message);
    await drawCenter();
  }
}

/** Keep capability tags on one line; the rest collapse into +N. @param {ParentNode} root */
function clampTags(root) {
  root.querySelectorAll(".plug-prims").forEach((row) => {
    const tags = [...row.querySelectorAll(".tag")];
    if (!tags.length || row.scrollHeight <= row.clientHeight + 1) return;
    /** @type {string[]} */
    const hidden = [];
    while (tags.length && row.scrollHeight > row.clientHeight + 1) {
      const last = tags.pop();
      if (!last) break;
      hidden.unshift(last.textContent || "");
      last.remove();
      let more = row.querySelector(".plug-more");
      if (!more) {
        more = document.createElement("span");
        more.className = "tag plug-more";
        row.appendChild(more);
      }
      more.textContent = "+" + hidden.length;
      more.title = hidden.join("、");
    }
  });
}

/** @param {HTMLElement} box */
function paintSettings(box) {
  const repos = sources.repos || [];
  const manual = sources.manual || [];
  box.innerHTML = `<div class="plug-settings">
    <section class="card plug-set">
      <h3>${icon("download")}手动安装</h3>
      <p class="hint">填写 .vtp 地址、整包 SHA-256，以及一把已经信任的签名密钥。插件名必须和安装包里的 manifest 一致。</p>
      <form class="plug-form" id="plugManual">
        <input class="input" id="plugManName" placeholder="插件名，如 hello" autocomplete="off" spellcheck="false">
        <input class="input grow" id="plugManURL" placeholder="https://example.com/hello-1.0.0-darwin-arm64.vtp" autocomplete="off" spellcheck="false">
        <input class="input" id="plugManSHA" placeholder="SHA-256" autocomplete="off" spellcheck="false">
        <input class="input" id="plugManKey" placeholder="签名密钥 ID" autocomplete="off" spellcheck="false">
        <button class="btn primary" type="submit">安装</button>
      </form>
      <div id="plugManualList">${manual.length ? manual.map((m) => `<div class="plug-repo"><div class="grow"><b>${esc(m.name)}</b><span class="faint">${esc(m.url)}</span></div><button class="btn sm" data-drop-manual="${esc(m.name)}">移除</button></div>`).join("") : `<p class="faint">还没有手动安装的插件</p>`}</div>
    </section>
    <section class="card plug-set">
      <h3>${icon("package")}插件仓库</h3>
      <p class="hint">官方目录随 Hub 发布，安装包由官方密钥签名。自定义目录同样是 catalogVersion 为 1 的 JSON，每条插件给出 .vtp 地址、整包 SHA-256 和密钥 ID。添加仓库时要带上对应公钥。</p>
      <p class="plug-note plug-warn">${icon("triangle-alert")}公钥一旦加入就会用来验签。只添加自己确认可信的仓库。</p>
      <div class="plug-repo">
        <div class="grow"><b>Vartio 官方 <span class="plug-official">${icon("badge-check")}官方</span></b><span class="faint">内置目录，随 Hub 发布</span></div>
        <span class="badge up">已启用</span>
      </div>
      ${repos.map((repo) => `<div class="plug-repo"><div class="grow"><b>${esc(repo.name)}</b><span class="faint">${esc(repo.url)}</span>${repo.error ? `<span class="plug-err">${esc(repo.error)}</span>` : `<span class="faint">${repo.plugins} 个插件</span>`}</div><button class="btn sm" data-drop-repo="${esc(repo.id)}">移除</button></div>`).join("")}
      <form class="plug-form" id="plugRepo">
        <input class="input" id="plugRepoID" placeholder="仓库 ID" autocomplete="off" spellcheck="false">
        <input class="input" id="plugRepoName" placeholder="仓库名称" autocomplete="off">
        <input class="input grow" id="plugRepoURL" placeholder="https://example.com/catalog/index.json" autocomplete="off" spellcheck="false">
        <input class="input" id="plugRepoKey" placeholder="密钥 ID" autocomplete="off" spellcheck="false">
        <input class="input grow" id="plugRepoPub" placeholder="Base64 公钥" autocomplete="off" spellcheck="false">
        <button class="btn primary" type="submit">添加仓库</button>
      </form>
    </section>
  </div>`;
  $("#plugManual").addEventListener("submit", async (/** @type {SubmitEvent} */ e) => {
    e.preventDefault();
    const name = $("#plugManName").value.trim();
    const url = $("#plugManURL").value.trim();
    const sha256 = $("#plugManSHA").value.trim();
    const signingKeyId = $("#plugManKey").value.trim();
    if (!name || !url || !sha256 || !signingKeyId) return;
    const yes = await askChoice({ title: `安装${name}`, body: `将从 ${url} 下载安装包，并用已信任的密钥 ${signingKeyId} 验签。`, ok: "安装" });
    if (!yes) return;
    const btn = /** @type {HTMLButtonElement} */ ($("#plugManual button"));
    btn.disabled = true;
    btn.textContent = "安装中…";
    try {
      await api("/plugins/_manual", { method: "POST", json: { name, url, sha256, signingKeyId } });
      await drawCenter(true);
    } catch (err) {
      await tell("安装失败", err.message);
      await drawCenter(true);
    }
  });
  $("#plugRepo").addEventListener("submit", async (/** @type {SubmitEvent} */ e) => {
    e.preventDefault();
    const id = $("#plugRepoID").value.trim();
    const name = $("#plugRepoName").value.trim();
    const url = $("#plugRepoURL").value.trim();
    const keyId = $("#plugRepoKey").value.trim();
    const publicKey = $("#plugRepoPub").value.trim();
    if (!id || !name || !url || !keyId || !publicKey) return;
    const yes = await askChoice({ title: `添加仓库 ${name}`, body: `Hub 会读取 ${url}，并用密钥 ${keyId} 校验其中的安装包。`, ok: "添加" });
    if (!yes) return;
    try {
      sources = await api("/plugins/_sources", { method: "POST", json: { id, name, url, keyId, publicKey } });
      await drawCenter(true);
    } catch (err) {
      await tell("无法添加仓库", err.message);
    }
  });
  box.querySelectorAll("[data-drop-manual]").forEach((b) => b.addEventListener("click", async () => {
    const name = /** @type {HTMLElement} */ (b).dataset.dropManual || "";
    const yes = await askChoice({ title: `移除${name}`, body: `「${name}」会从手动安装列表中去掉。如果它正在运行，进程会停掉，已下载的安装包会被删除。`, ok: "移除", danger: true });
    if (!yes) return;
    try {
      await api("/plugins/_manual/" + encodeURIComponent(name), { method: "DELETE" });
      await drawCenter(true);
    } catch (err) { await tell("移除失败", err.message); }
  }));
  box.querySelectorAll("[data-drop-repo]").forEach((b) => b.addEventListener("click", async () => {
    const id = /** @type {HTMLElement} */ (b).dataset.dropRepo || "";
    const yes = await askChoice({ title: `移除仓库 ${id}`, body: "目录里来自这个仓库的未安装插件会消失。已经安装的插件会继续运行，直到单独卸载。", ok: "移除", danger: true });
    if (!yes) return;
    try {
      sources = await api("/plugins/_sources/" + encodeURIComponent(id), { method: "DELETE" });
      await drawCenter(true);
    } catch (err) { await tell("移除失败", err.message); }
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
