// @ts-check
import { $, esc, bytes, api } from "./core.js";
import { openModal, toggleFullscreen } from "./ui.js";

/** @typedef {{ index: number, name: string, x: number, y: number, width: number, height: number, primary?: boolean }} Screen */

const QUALITIES = [["auto", "智能", "按网络和机器性能自动平衡清晰度与流畅度"], ["smooth", "流畅", "流畅度优先，最高 10 fps，画质适当降低"], ["hd", "高清", "画质优先，最高 2560 像素，帧率较低"]];
const QKEY = "probe.screenQuality";

/** @param {SystemView} s */
export async function openScreen(s) {
  /** @type {WebSocket | null} */
  let ws = null;
  /** @type {string | null} */
  let url = null;
  let frames = 0;
  /** @type {Screen[]} */
  let screens = [];
  let cur = 0, failed = false;
  let quality = localStorage.getItem(QKEY) || "auto";
  /** "" until the agent reports quality profiles (older agents have none) */
  let qSupported = "";
  /** @param {KeyboardEvent} e */
  const onKey = (e) => {
    const t = e.target;
    if (screens.length < 2 || e.ctrlKey || e.metaKey || e.altKey || (t instanceof Element && t.closest("input, textarea, select"))) return;
    const n = Number(e.key);
    if (n >= 1 && n <= screens.length) pick(n - 1);
  };
  openModal(`实时画面 · ${s.name}`, `<div class="screen-bar" id="screenBar" hidden></div><div class="screen" id="screen"><div class="msg">正在申请票据…</div></div><p class="modal-foot" id="screenInfo"></p>`,
    () => { document.removeEventListener("keydown", onKey); if (ws) ws.close(); if (url) URL.revokeObjectURL(url); }, { wide: true, fullscreen: true });
  document.addEventListener("keydown", onKey);

  const renderBar = () => {
    const bar = $("#screenBar");
    if (!bar) return;
    bar.hidden = screens.length < 2 && !qSupported;
    if (bar.hidden) return;
    bar.innerHTML = (screens.length > 1 ? `<div class="seg" id="scrSeg">${screens.map((sc) => `<button type="button" data-i="${sc.index}" class="${sc.index === cur ? "on" : ""}" title="${sc.x}, ${sc.y} · 按 ${sc.index + 1} 切换">
      <b>${sc.index + 1}</b> ${esc(sc.name || "屏幕")}${sc.primary ? "（主屏）" : ""}<span class="faint"> ${sc.width}×${sc.height}</span></button>`).join("")}</div>` : "<span></span>") +
      (qSupported ? `<div class="seg" id="qSeg">${QUALITIES.map(([k, n, tip]) => `<button type="button" data-q="${k}" class="${k === quality ? "on" : ""}" title="${tip}">${n}</button>`).join("")}</div>` : "");
    bar.querySelectorAll("#scrSeg button").forEach((b) => b.addEventListener("click", () => pick(Number(b.dataset.i))));
    bar.querySelectorAll("#qSeg button").forEach((b) => b.addEventListener("click", () => setQuality(b.dataset.q || "auto")));
  };
  /** @param {number} i */
  const pick = (i) => {
    if (i === cur || !ws || ws.readyState !== 1) return;
    cur = i;
    renderBar();
    ws.send(JSON.stringify({ type: "screen", screen: i }));
  };
  /** @param {string} q */
  const setQuality = (q) => {
    localStorage.setItem(QKEY, q);
    if (q === quality || !ws || ws.readyState !== 1) return;
    quality = q;
    renderBar();
    ws.send(JSON.stringify({ type: "quality", quality: q }));
  };
  /** @param {string} text */
  const onText = (text) => {
    const box = $("#screen");
    /** @type {{ type?: string, screens?: Screen[], screen?: number, quality?: string, message?: string } | null} */
    let m = null;
    if (text.startsWith("{")) { try { m = JSON.parse(text); } catch { m = null; } }
    if (!m) { failed = true; if (box) box.innerHTML = `<div class="msg">${esc(text)}</div>`; return; }
    if (m.type === "info") { screens = m.screens || []; cur = m.screen || 0; qSupported = m.quality || ""; if (qSupported) quality = qSupported; renderBar(); }
    else if (m.type === "screen") { cur = m.screen || 0; renderBar(); }
    else if (m.type === "quality") { quality = m.quality || "auto"; renderBar(); }
    else if (m.type === "error") alert(m.message);
  };

  /** @type {[number, number][]} arrival time and size of frames in the last 3 s */
  const recent = [];
  try {
    const tk = await api(`/sessions/${encodeURIComponent(s.id)}/display`, { method: "POST" });
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    const sock = new WebSocket(`${proto}//${location.host}${tk.ws}&quality=${encodeURIComponent(quality)}`);
    ws = sock;
    sock.binaryType = "blob";
    sock.onmessage = (ev) => {
      const box = $("#screen");
      if (!box) return;
      if (typeof ev.data === "string") { onText(ev.data); return; }
      frames++;
      const now = Date.now();
      recent.push([now, ev.data.size]);
      while (recent.length && now - recent[0][0] > 3000) recent.shift();
      const next = URL.createObjectURL(ev.data);
      let img = box.querySelector("img");
      if (!img) {
        box.innerHTML = "";
        img = document.createElement("img");
        img.alt = "remote screen";
        img.addEventListener("dblclick", toggleFullscreen);
        box.appendChild(img);
      }
      const el = img;
      el.onload = () => {
        const info = $("#screenInfo");
        const span = recent.length > 1 ? (recent[recent.length - 1][0] - recent[0][0]) / 1000 : 0;
        const sum = recent.reduce((a, [, n]) => a + n, 0);
        const ql = QUALITIES.find(([k]) => k === quality);
        if (info) info.textContent = `${el.naturalWidth}×${el.naturalHeight} · ${span ? ((recent.length - 1) / span).toFixed(1) : "—"} fps · ${span ? bytes(sum / span) + "/s" : "—"} · ${bytes(sum / recent.length)}/帧` +
          (qSupported && ql ? ` · ${ql[1]}` : "") +
          (screens.length > 1 ? ` · 共 ${screens.length} 块屏幕` : "") +
          (matchMedia("(pointer: coarse)").matches ? " · 横屏查看更清晰" : screens.length > 1 ? " · 数字键切换屏幕，双击画面铺满页面" : " · 双击画面铺满页面");
      };
      el.src = next;
      if (url) URL.revokeObjectURL(url);
      url = next;
    };
    sock.onclose = () => {
      const box = $("#screen");
      if (box && !frames && !failed) box.innerHTML = `<div class="msg">连接已关闭（Windows 服务模式运行在 Session 0 时无法截取桌面，请以普通用户身份前台运行探针）</div>`;
    };
  } catch (e) {
    const box = $("#screen");
    if (box) box.innerHTML = `<div class="msg">${esc(/** @type {Error} */ (e).message)}</div>`;
  }
}
