// @ts-check
import { $, api, bytes, esc } from "./core.js";
import { openModal, closeModal } from "./ui.js";

/** @param {string} a @param {string} b */
export function cmpVer(a, b) {
  const pa = a.split("."), pb = b.split(".");
  for (let i = 0; i < Math.max(pa.length, pb.length); i++) {
    const d = (parseInt(pa[i], 10) || 0) - (parseInt(pb[i], 10) || 0);
    if (d) return Math.sign(d);
  }
  return 0;
}

let cache = { at: 0, latest: "" };

/** The agent version the hub offers ("" when it has no signed builds). */
export async function latestAgent() {
  if (Date.now() - cache.at < 60000) return cache.latest;
  try {
    const r = /** @type {Releases} */ (await api("/releases"));
    cache = { at: Date.now(), latest: r.latest || "" };
  } catch {
    cache.at = Date.now();
  }
  return cache.latest;
}

/** @param {SystemView} s @param {string} latest */
export const outdated = (s, latest) => !!latest && !!s.version && cmpVer(s.version, latest) < 0;

/** @param {SystemView} s */
export const canUpgrade = (s) => s.online && (s.caps || []).includes("upgrade");

/** @type {Record<string, string>} */
export const RUNNERS = { service: "Windows 服务", tray: "后台（托盘图标）", console: "前台进程", systemd: "systemd 服务", launchd: "launchd 后台", docker: "Docker 容器" };

const hostName = (/** @type {SystemView} */ s) => s.name || s.hostname;

/** Explains why a host cannot be upgraded remotely. @param {SystemView} s @param {string} latest */
export function upgradeNotice(s, latest) {
  openModal("无法远程升级", `<p class="upd-desc">${esc(hostName(s))} 的探针 ${esc(s.version)} ${s.online
    ? `不支持远程升级。请手动替换为 ${esc(latest)} 一次，之后即可在这里一键升级。` : "不在线，上线后再试。"}</p>
    <div class="form-actions"><button class="btn primary" id="updOk">知道了</button></div>`, null, { small: true });
  $("#updOk").addEventListener("click", closeModal);
}

/** @param {SystemView} s @param {string} latest @param {() => void} [onEnd] */
export function upgradeOne(s, latest, onEnd) {
  upgradeDialog([s], latest, onEnd);
}

/** @param {SystemView[]} list @param {string} latest @param {() => void} [onEnd] */
export function upgradeAll(list, latest, onEnd) {
  upgradeDialog(list, latest, onEnd);
}

// After the restart the new build has the probation window (2 minutes by
// default) to reconnect before the watchdog rolls back; allow some slack.
const RESTART_WAIT = 200e3;

/** @type {Record<string, [string, number]>} stage -> label, progress floor */
const STAGES = {
  queued: ["下发中", 3], download: ["下载", 5], verify: ["校验签名", 66], selftest: ["运行自检", 74],
  restart: ["重启中，等待新版本上线", 82], done: ["完成", 100], failed: ["失败", 100], rolled_back: ["已回滚", 100],
};
const FINAL = new Set(["done", "failed", "rolled_back"]);

/**
 * Confirms, starts and follows an upgrade of one or more hosts. Progress comes
 * from GET /upgrade; closing the dialog only stops watching.
 * @param {SystemView[]} list @param {string} latest @param {() => void} [onEnd]
 */
function upgradeDialog(list, latest, onEnd) {
  const many = list.length > 1;
  /** @type {Map<string, { stage: string, done?: number, total?: number, msg?: string, to?: string, restartAt?: number }>} */
  const st = new Map(list.map((s) => [s.id, { stage: "" }]));
  /** @type {Map<string, string>} upgrades already on the hub before we started */
  const before = new Map();
  let timer = 0, closed = false;
  openModal(many ? `批量升级探针 · ${list.length} 台` : `升级探针 · ${hostName(list[0])}`, `<div class="upd">
    <p class="upd-desc" id="updDesc">${many ? `将以下 ${list.length} 台在线探针升级到 <b>${esc(latest)}</b>（每次最多 4 台并行）。` : `将探针升级到 <b>${esc(latest)}</b>。`}
      下载后校验 SHA-256 与发布签名、运行自检，然后自动重启；新版本 2 分钟内连不上 hub 会自动回滚到当前版本。</p>
    <ul class="upd-list">${list.map((s) => `<li data-id="${esc(s.id)}">
      <div class="upd-head"><b>${esc(hostName(s))}</b><span class="faint">${esc(s.version)} → ${esc(latest)}</span><span class="upd-st">待升级</span></div>
      <div class="upd-bar"><i></i></div></li>`).join("")}</ul>
    <div class="form-actions" id="updActs"><button class="btn" id="updCancel">取消</button><button class="btn primary" id="updGo">开始升级</button></div>
  </div>`, () => { closed = true; clearTimeout(timer); }, { small: true });
  $("#updCancel").addEventListener("click", closeModal);
  $("#updGo").addEventListener("click", start);

  async function start() {
    $("#updActs").innerHTML = `<button class="btn" id="updHide">后台运行</button>`;
    $("#updHide").addEventListener("click", closeModal);
    $("#updDesc").textContent = "升级进行中，可关闭此窗口在后台继续；完成后主机列表会显示新版本。";
    for (const s of list) st.set(s.id, { stage: "queued" });
    try {
      for (const p of /** @type {UpgradeProgress[]} */ (await api("/upgrade")) || []) before.set(p.id, p.started);
    } catch { /* progress is best effort */ }
    render();
    poll();
    try {
      if (many) {
        const res = /** @type {BatchResult[]} */ (await api("/upgrade", { method: "POST", json: { ids: list.map((s) => s.id), version: latest, confirm: true } }));
        for (const r of res || []) if (!r.ok) fail(r.id, r.message || "失败");
      } else {
        await api(`/sessions/${encodeURIComponent(list[0].id)}/upgrade`, { method: "POST", json: { version: latest, confirm: true } });
      }
    } catch (e) {
      for (const s of list) fail(s.id, /** @type {Error} */ (e).message);
    }
    render();
  }

  /** @param {string} id @param {string} msg */
  function fail(id, msg) {
    const x = st.get(id);
    if (x && !FINAL.has(x.stage)) Object.assign(x, { stage: "failed", msg });
  }

  async function poll() {
    if (closed) return;
    try {
      for (const p of /** @type {UpgradeProgress[]} */ (await api("/upgrade")) || []) {
        const x = st.get(p.id);
        if (!x || FINAL.has(x.stage) || before.get(p.id) === p.started) continue;
        Object.assign(x, { stage: p.stage, done: p.done, total: p.total, msg: p.message, to: p.to });
      }
    } catch { /* keep polling */ }
    const now = Date.now();
    for (const [id, x] of st) {
      if (x.stage !== "restart") continue;
      x.restartAt ||= now;
      if (now - x.restartAt > RESTART_WAIT) fail(id, "新版本迟迟未上线，请检查主机状态");
    }
    render();
    if ([...st.values()].every((x) => FINAL.has(x.stage))) return finish();
    timer = window.setTimeout(poll, 700);
  }

  function render() {
    if (closed) return;
    for (const [id, x] of st) {
      const li = document.querySelector(`.upd-list li[data-id="${CSS.escape(id)}"]`);
      if (!li || !x.stage) continue;
      const [label, floor] = STAGES[x.stage] || [x.stage, 0];
      let pct = floor, text = label;
      if (x.stage === "download" && x.total) {
        const f = Math.min(1, (x.done || 0) / x.total);
        pct = floor + f * (STAGES.verify[1] - floor);
        text = `下载 ${Math.round(f * 100)}% · ${bytes(x.done || 0)} / ${bytes(x.total)}`;
      }
      if (x.stage === "done") text = `已升级到 ${x.to || latest}`;
      if ((x.stage === "failed" || x.stage === "rolled_back") && x.msg) text = `${label}：${x.msg}`;
      li.className = x.stage === "done" ? "ok" : FINAL.has(x.stage) ? "bad" : x.stage === "restart" ? "wait" : "run";
      /** @type {HTMLElement} */ (li.querySelector(".upd-st")).textContent = text;
      /** @type {HTMLElement} */ (li.querySelector(".upd-bar i")).style.width = pct + "%";
    }
  }

  function finish() {
    const xs = [...st.values()], ok = xs.filter((x) => x.stage === "done").length;
    $("#updDesc").textContent = ok === xs.length ? (many ? `${ok} 台全部升级完成。` : "升级完成，新版本已上线。")
      : `${ok} 台成功，${xs.length - ok} 台未完成；失败的主机仍在运行原版本。`;
    $("#updActs").innerHTML = `<button class="btn primary" id="updDone">完成</button>`;
    $("#updDone").addEventListener("click", closeModal);
    if (onEnd) onEnd();
  }
}
