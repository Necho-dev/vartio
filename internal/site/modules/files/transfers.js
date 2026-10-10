// @ts-check
import { $, API, state, esc, bytes, rate, pct, showLogin } from "../core.js";
import { icon } from "../icons.js";
import { listHTML } from "./common.js";
import { ask } from "./dialog.js";

const XFER_PARALLEL = 2;

// Upload/download queue in the right-hand panel; at most XFER_PARALLEL run at once.
/** @param {import("./common.js").FilesCtx} fm */
export function createTransfers(fm) {
  const { st } = fm;
  function renderQueue() {
    if (st.closed) return;
    const list = $("#fmQList");
    const active = st.xfers.filter((x) => x.state === "run" || x.state === "wait").length;
    $("#fmQSum").textContent = st.xfers.length ? `${active} 进行中 / 共 ${st.xfers.length}` : "";
    if (!st.xfers.length) {
      list.innerHTML = `<div class="fm-qempty">${icon("arrow-up-down")}无进行中的传输<br><small>拖拽文件或文件夹到左侧列表即可上传</small></div>`;
      return;
    }
    list.innerHTML = st.xfers.map((x) => {
      const p = x.total ? Math.min(100, (x.done / x.total) * 100) : x.state === "ok" ? 100 : 0;
      const speed = x.state === "run" && x.t0 ? rate(x.done / Math.max(0.2, (Date.now() - x.t0) / 1000)) : "";
      const label = { wait: "排队中", run: x.phase || `${pct(p)} · ${speed}`, ok: "完成", err: x.err || "失败", cancel: "已取消", exists: "目标已存在" }[x.state];
      return `<div class="fm-x ${x.state}" data-id="${x.id}">
        <div class="fm-xrow"><span class="dir ${x.dir}">${icon(x.dir === "up" ? "arrow-up" : "arrow-down")}</span><span class="fm-xname" title="${esc(x.dest || x.name)}">${esc(x.name)}</span>
          ${x.state === "exists" ? `<button class="btn sm" data-act="overwrite">覆盖</button>` : ""}
          ${x.state === "run" || x.state === "wait" ? `<button class="x" data-act="cancel" title="取消">${icon("x")}</button>` : ""}
          ${x.state === "err" || x.state === "cancel" ? `<button class="x" data-act="retry" title="重试">${icon("rotate-ccw")}</button>` : ""}</div>
        <div class="fm-xbar"><i style="width:${p.toFixed(1)}%"></i></div>
        <div class="fm-xmeta"><span>${x.total ? bytes(x.done) + " / " + bytes(x.total) : ""}</span><span>${esc(label)}</span></div></div>`;
    }).join("");
  }
  let queueTimer = 0;
  const tickQueue = () => { if (!queueTimer) queueTimer = setTimeout(() => { queueTimer = 0; renderQueue(); }, 100); };
  $("#fmQList").addEventListener("click", (e) => {
    const b = e.target.closest("button[data-act]");
    const x = b && st.xfers.find((y) => y.id === +b.closest(".fm-x").dataset.id);
    if (!x) return;
    if (b.dataset.act === "cancel") { x.abort ? x.abort() : (x.state = "cancel"); tickQueue(); pump(); return; }
    const restart = () => { x.state = "wait"; x.done = 0; x.err = ""; tickQueue(); pump(); };
    if (b.dataset.act !== "overwrite") return restart();
    ask({
      title: "覆盖已有文件？", ok: "覆盖", danger: true,
      body: `<p>远端已存在 <code>${esc(x.dest)}</code>。</p><p class="warn">覆盖后原文件内容将被替换，无法恢复。</p>`,
    }).then((yes) => { if (yes) { x.overwrite = true; restart(); } });
  });
  $("#fmQClear").addEventListener("click", () => {
    st.xfers = st.xfers.filter((x) => x.state === "run" || x.state === "wait" || x.state === "exists");
    renderQueue();
  });

  /** @param {import("./common.js").Xfer} x */
  function enqueue(x) {
    x.id = ++st.seq; x.state = "wait"; x.done = 0;
    st.xfers.unshift(x);
  }
  function pump() {
    if (st.closed) return;
    let running = st.xfers.filter((x) => x.state === "run").length;
    for (const x of [...st.xfers].reverse()) {
      if (running >= XFER_PARALLEL) break;
      if (x.state !== "wait") continue;
      running++;
      x.state = "run"; x.t0 = Date.now(); x.phase = "";
      (x.dir === "up" ? runUpload(x) : runDownload(x)).finally(() => { x.abort = null; tickQueue(); pump(); });
    }
    tickQueue();
  }

  /** @param {import("./common.js").Xfer} x @returns {Promise<void>} */
  function runUpload(x) {
    return new Promise((resolve) => {
      const xhr = new XMLHttpRequest();
      x.abort = () => { xhr.abort(); };
      xhr.open("PUT", `${API}/sessions/${fm.bios}/files?path=${encodeURIComponent(x.dest)}${x.overwrite ? "&confirm=true" : ""}`);
      if (state.token) xhr.setRequestHeader("Authorization", "Bearer " + state.token);
      xhr.upload.onprogress = (ev) => {
        x.done = ev.loaded;
        if (ev.loaded >= ev.total) x.phase = "写入远端…";
        tickQueue();
      };
      xhr.onload = () => {
        let data = null;
        try { data = JSON.parse(xhr.responseText); } catch (e) { /* not json */ }
        if (xhr.status >= 200 && xhr.status < 300) {
          x.state = "ok"; x.done = x.total;
          if (x.reload) fm.reloadSoon();
        } else if (data && data.code === "confirm_required") {
          x.state = "exists";
        } else {
          x.state = "err"; x.err = (data && (data.message || data.code)) || xhr.statusText || "HTTP " + xhr.status;
          if (xhr.status === 401) showLogin();
        }
        resolve();
      };
      xhr.onerror = () => { x.state = "err"; x.err = "网络错误"; resolve(); };
      xhr.onabort = () => { x.state = "cancel"; resolve(); };
      xhr.send(x.file);
    });
  }

  /** @param {import("./common.js").Xfer} x */
  async function runDownload(x) {
    const ctl = new AbortController();
    x.abort = () => ctl.abort();
    if (x.isDir) x.phase = "远端打包中…";
    tickQueue();
    try {
      const headers = state.token ? { Authorization: "Bearer " + state.token } : {};
      const res = await fetch(`${API}/sessions/${fm.bios}/files/content?path=${encodeURIComponent(x.src)}`, { headers, signal: ctl.signal });
      if (!res.ok) {
        const data = await res.json().catch(() => null);
        throw new Error((data && (data.message || data.code)) || res.statusText);
      }
      x.total = +res.headers.get("Content-Length") || x.total || 0;
      x.t0 = Date.now(); x.phase = "";
      const reader = res.body.getReader(), parts = [];
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        parts.push(value);
        x.done += value.length;
        tickQueue();
      }
      const url = URL.createObjectURL(new Blob(parts));
      const a = document.createElement("a");
      a.href = url; a.download = x.name;
      document.body.appendChild(a); a.click(); a.remove();
      setTimeout(() => URL.revokeObjectURL(url), 30000);
      x.state = "ok";
    } catch (e) {
      if (e.name === "AbortError") x.state = "cancel";
      else { x.state = "err"; x.err = e.message; }
    }
  }

  /** @param {FileEntry[]} list */
  function download(list) {
    list.forEach((it) => enqueue({ dir: "down", src: it.path, isDir: it.isDir, name: it.isDir ? it.name + ".zip" : it.name, total: it.isDir ? 0 : it.size || 0 }));
    pump();
  }
  /** @param {{ file: File, rel?: string }[]} files */
  async function upload(files) {
    if (!files.length) return;
    const base = st.cwd;
    const size = files.reduce((n, f) => n + f.file.size, 0);
    const tops = [...new Set(files.map((f) => (f.rel || f.file.name).split("/")[0]))];
    const yes = await ask({
      title: `上传 ${files.length} 个文件？`, ok: "开始上传",
      body: `${listHTML(tops.map((n) => ({ name: n, isDir: files.some((f) => (f.rel || "").startsWith(n + "/")) })))}
        <p>合计 ${bytes(size)}，上传到 <code>${esc(base)}</code></p><p class="faint">远端已有同名文件时不会覆盖，会在传输列表中单独询问。</p>`,
    });
    if (!yes) return;
    files.forEach(({ file, rel }) => {
      const relPath = (rel || file.name).split("/").filter(Boolean).join(st.sep);
      enqueue({ dir: "up", file, name: rel || file.name, dest: fm.join(base, relPath), total: file.size, reload: true });
    });
    pump();
  }

  return { download, upload };
}
