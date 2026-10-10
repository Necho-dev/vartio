// @ts-check
import { $, API, state, esc, bytes, showLogin } from "../core.js";
import { icon } from "../icons.js";
import { fileIcon, view } from "./common.js";

const PREVIEW_MAX = 10 << 20;
const IMAGE_MAX = 30 << 20;
const IMAGE_TYPES = { png: "image/png", jpg: "image/jpeg", jpeg: "image/jpeg", gif: "image/gif", webp: "image/webp", bmp: "image/bmp", ico: "image/x-icon", svg: "image/svg+xml", avif: "image/avif" };
const BINARY_EXT = /\.(exe|dll|sys|msi|bin|iso|img|vmdk|zip|7z|rar|gz|tgz|bz2|xz|zst|tar|jar|apk|dmg|pkg|deb|rpm|heic|tiff?|psd|mp[34]|m4a|mov|avi|mkv|flv|wmv|wav|flac|ogg|pdf|docx?|xlsx?|pptx?|db|sqlite|pyc|class|o|a|so|dylib|ttf|otf|woff2?)$/i;
/** @param {Uint8Array} buf Returns the likely encoding, or "" for binary. */
function sniffText(buf) {
  const n = Math.min(buf.length, 8192);
  if (buf[0] === 0xff && buf[1] === 0xfe) return "utf-16le";
  if (buf[0] === 0xfe && buf[1] === 0xff) return "utf-16be";
  for (let i = 0; i < n; i++) if (buf[i] === 0) return "";
  try { new TextDecoder("utf-8", { fatal: true }).decode(buf); return "utf-8"; } catch (e) { return "gbk"; }
}
/** @param {import("./common.js").FilesCtx} fm @param {FileEntry} it */
export async function preview(fm, it) {
  if (view.cur) view.cur.close();
  const box = document.createElement("div");
  box.className = "fm-view";
  box.innerHTML = `<div class="fm-vhead">${fileIcon(it)}<b title="${esc(it.path)}">${esc(it.name)}</b>
      <span class="faint" data-v="meta">${bytes(it.size || 0)}</span>
      <span class="fm-vtools">
        <select data-v="enc" hidden title="文本编码"><option value="utf-8">UTF-8</option><option value="gbk">GBK</option><option value="utf-16le">UTF-16 LE</option><option value="utf-16be">UTF-16 BE</option></select>
        <label class="fm-check" data-v="wrapL" hidden><input type="checkbox" data-v="wrap" checked> 自动换行</label>
        <button class="btn sm" data-v="down">${icon("download")}下载</button>
        <button class="icon-btn" data-v="close" title="关闭（Esc）">${icon("x")}</button></span></div>
    <div class="fm-vbody"><div class="fm-vmsg">正在读取…</div></div>`;
  $(".modal-box").appendChild(box);
  const q = (/** @type {string} */ k) => $(`[data-v="${k}"]`, box), vbody = box.querySelector(".fm-vbody");
  const ctl = new AbortController();
  let blobURL = "";
  view.cur = {
    close: () => { ctl.abort(); box.remove(); if (blobURL) URL.revokeObjectURL(blobURL); view.cur = null; },
    selectAll: () => {
      const pre = box.querySelector(".fm-vtext");
      if (!pre) return;
      const r = document.createRange();
      r.selectNodeContents(pre);
      const sel = getSelection();
      sel.removeAllRanges(); sel.addRange(r);
    },
  };
  q("close").addEventListener("click", () => view.cur && view.cur.close());
  q("down").addEventListener("click", () => fm.download([it]));
  const refuse = (why) => { vbody.innerHTML = `<div class="fm-vmsg">${why}<br><small>可点击右上角“下载”保存到本地查看</small></div>`; };
  const imgType = IMAGE_TYPES[(it.name.match(/\.([^.]+)$/) || [])[1]?.toLowerCase()];
  const limit = imgType ? IMAGE_MAX : PREVIEW_MAX;
  if ((it.size || 0) > limit) return refuse(`文件超过 ${bytes(limit)}，不支持在线预览`);
  if (!imgType && BINARY_EXT.test(it.name)) return refuse("该类型不支持在线预览，仅支持文本和常见图片");
  let buf;
  try {
    const headers = state.token ? { Authorization: "Bearer " + state.token } : {};
    const res = await fetch(`${API}/sessions/${fm.bios}/files/content?path=${encodeURIComponent(it.path)}`, { headers, signal: ctl.signal });
    if (!res.ok) {
      const data = await res.json().catch(() => null);
      if (res.status === 401) showLogin();
      throw new Error((data && (data.message || data.code)) || res.statusText);
    }
    buf = new Uint8Array(await res.arrayBuffer());
  } catch (e) {
    if (e.name !== "AbortError") refuse(`读取失败：${esc(e.message)}`);
    return;
  }
  if (!view.cur) return;
  if (buf.length > limit) return refuse(`文件超过 ${bytes(limit)}，不支持在线预览`);
  if (imgType) {
    blobURL = URL.createObjectURL(new Blob([buf], { type: imgType }));
    const wrap = document.createElement("div");
    wrap.className = "fm-vimg";
    const img = new Image();
    img.alt = it.name;
    img.title = "点击切换 适应窗口 / 原始大小";
    img.onload = () => { q("meta").textContent = `${bytes(buf.length)} · ${img.naturalWidth} × ${img.naturalHeight}`; };
    img.onerror = () => refuse("图片无法解码（文件可能已损坏或格式不受浏览器支持）");
    img.addEventListener("click", () => wrap.classList.toggle("actual"));
    img.src = blobURL;
    wrap.appendChild(img);
    vbody.innerHTML = "";
    vbody.appendChild(wrap);
    return;
  }
  const enc = sniffText(buf);
  if (!enc) return refuse("文件包含二进制内容，不是文本文件");
  const pre = document.createElement("pre");
  pre.className = "fm-vtext wrap";
  vbody.innerHTML = "";
  vbody.appendChild(pre);
  const show = (e) => {
    const text = new TextDecoder(e).decode(buf);
    pre.textContent = text;
    q("meta").textContent = `${bytes(buf.length)} · ${(text.match(/\n/g) || []).length + 1} 行`;
  };
  q("enc").value = enc; q("enc").hidden = false; q("wrapL").hidden = false;
  q("enc").addEventListener("change", (/** @type {any} */ e) => show(e.target.value));
  q("wrap").addEventListener("change", (/** @type {any} */ e) => pre.classList.toggle("wrap", e.target.checked));
  show(enc);
  vbody.scrollTop = 0;
}
