// @ts-check
import { $, esc, bytes, api } from "./core.js";
import { openModal, toggleFullscreen } from "./ui.js";

/** @param {SystemView} s */
export async function openScreen(s) {
  let ws = null, url = null, frames = 0, bytesIn = 0;
  openModal(`实时画面 · ${s.name}（只读，约 2 fps）`, `<div class="screen" id="screen"><div class="msg">正在申请票据…</div></div><p class="modal-foot" id="screenInfo"></p>`,
    () => { if (ws) ws.close(); if (url) URL.revokeObjectURL(url); }, { wide: true, fullscreen: true });
  const t0 = Date.now();
  try {
    const tk = await api(`/sessions/${encodeURIComponent(s.biosUuid)}/display`, { method: "POST" });
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    ws = new WebSocket(`${proto}//${location.host}${tk.ws}`);
    ws.binaryType = "blob";
    ws.onmessage = (ev) => {
      const box = $("#screen");
      if (!box) return;
      if (typeof ev.data === "string") { box.innerHTML = `<div class="msg">${esc(ev.data)}</div>`; return; }
      frames++; bytesIn += ev.data.size;
      const next = URL.createObjectURL(ev.data);
      let img = box.querySelector("img");
      if (!img) {
        box.innerHTML = "";
        img = document.createElement("img");
        img.alt = "remote screen";
        img.addEventListener("dblclick", toggleFullscreen);
        box.appendChild(img);
      }
      img.onload = () => {
        const info = $("#screenInfo");
        if (info) info.textContent = `${img.naturalWidth}×${img.naturalHeight} · ${frames} 帧 · ${(frames / ((Date.now() - t0) / 1000)).toFixed(1)} fps · 平均 ${bytes(bytesIn / frames)}/帧 · 双击画面铺满页面`;
      };
      img.src = next;
      if (url) URL.revokeObjectURL(url);
      url = next;
    };
    ws.onclose = () => {
      const box = $("#screen");
      if (box && !frames) box.innerHTML = `<div class="msg">连接已关闭（Windows 服务模式运行在 Session 0 时无法截取桌面，请以普通用户身份前台运行探针）</div>`;
    };
  } catch (e) {
    const box = $("#screen");
    if (box) box.innerHTML = `<div class="msg">${esc(e.message)}</div>`;
  }
}
