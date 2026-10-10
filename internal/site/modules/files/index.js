// @ts-check
import { $, esc, bytes, fmtTime, api } from "../core.js";
import { openModal } from "../ui.js";
import { icon } from "../icons.js";
import { fileIcon, listHTML, view } from "./common.js";
import { ask, activeDialog } from "./dialog.js";
import { preview } from "./preview.js";
import { openTrash } from "./trash.js";
import { createTransfers } from "./transfers.js";

const FILE_COLS = [["name", "名称"], ["mtime", "修改时间"], ["size", "大小"]];

// Remote file manager. "Local" is the browser: uploads come from the file
// picker / drag & drop, downloads land in the browser's download folder.
/** @param {SystemView} s */
export function openFiles(s) {
  const bios = encodeURIComponent(s.biosUuid);
  /** @type {import("./common.js").FilesState} */
  const st = {
    cwd: "", sep: "/", parent: "", home: "", roots: [], entries: [], sel: new Set(), anchor: null,
    back: [], fwd: [], sort: { key: "name", dir: 1 }, q: "", hidden: localStorage.getItem("probeHubHidden") === "1",
    xfers: [], seq: 0, closed: false, loading: false,
  };
  openModal(`文件 · ${s.name}`, `
    <div class="fm">
      <div class="fm-main">
        <div class="fm-bar">
          <button class="icon-btn" id="fmBack" title="后退">${icon("chevron-left")}</button>
          <button class="icon-btn" id="fmFwd" title="前进">${icon("chevron-right")}</button>
          <button class="icon-btn" id="fmUp" title="上一级">${icon("arrow-up")}</button>
          <button class="icon-btn" id="fmHome" title="主目录">${icon("house")}</button>
          <select id="fmRoots" title="磁盘"></select>
          <input id="fmPath" class="fm-path" spellcheck="false" placeholder="输入路径后回车">
          <button class="icon-btn" id="fmReload" title="刷新">${icon("refresh-cw")}</button>
          <input id="fmFilter" class="fm-filter" placeholder="筛选当前目录">
        </div>
        <div class="fm-tools">
          <button class="btn sm" id="fmMkdir">${icon("folder-plus")}新建文件夹</button>
          <button class="btn sm" id="fmUpFiles">${icon("upload")}上传文件</button>
          <button class="btn sm" id="fmUpDir">${icon("folder-up")}上传文件夹</button>
          <span class="fm-sep"></span>
          <button class="btn sm" id="fmDown" disabled>${icon("download")}下载</button>
          <button class="btn sm" id="fmRename" disabled>${icon("pencil-line")}重命名</button>
          <button class="btn sm danger" id="fmDel" disabled>${icon("trash-2")}删除</button>
          <span class="fm-sep"></span>
          <button class="btn sm" id="fmTrash" title="查看、恢复或清空远端回收站">${icon("trash")}回收站</button>
          <label class="fm-check"><input type="checkbox" id="fmHidden"> 显示隐藏文件</label>
          <input type="file" id="fmPickFiles" multiple hidden>
          <input type="file" id="fmPickDir" webkitdirectory multiple hidden>
        </div>
        <div class="fm-list" id="fmList" tabindex="0">
          <table class="fm-table"><colgroup><col style="width:34px"><col><col style="width:150px"><col style="width:96px"></colgroup>
            <thead><tr><th><input type="checkbox" id="fmAll"></th>${FILE_COLS.map(([k, n]) => `<th data-k="${k}">${n}<span class="arr"></span></th>`).join("")}</tr></thead>
            <tbody id="fmBody"></tbody></table>
          <div class="fm-empty" id="fmEmpty" hidden></div>
          <div class="fm-drop" id="fmDrop" hidden><div>松开鼠标，上传到当前目录</div></div>
        </div>
      </div>
      <aside class="fm-queue">
        <div class="fm-qhead"><b>传输</b><span id="fmQSum"></span><button class="btn sm" id="fmQClear">清除已完成</button></div>
        <div class="fm-qlist" id="fmQList"><div class="fm-qempty">${icon("arrow-up-down")}无进行中的传输<br><small>拖拽文件或文件夹到左侧列表即可上传</small></div></div>
      </aside>
    </div>
    <p class="modal-foot" id="fmInfo"></p>`, () => {
    st.closed = true;
    if (view.cur) view.cur.close();
    st.xfers.forEach((x) => x.abort && x.abort());
    document.removeEventListener("keydown", onKey, true);
  }, { wide: true, fullscreen: true });

  /** @param {string} dir @param {string} name */
  const join = (dir, name) => (dir.endsWith(st.sep) ? dir + name : dir + st.sep + name);
  /** @param {string} p */
  const byPath = (p) => st.entries.find((e) => e.path === p);
  const selected = () => st.entries.filter((e) => st.sel.has(e.path));
  const info = (msg) => { const el = $("#fmInfo"); if (el) el.textContent = msg; };

  function visible() {
    const q = st.q.toLowerCase(), { key, dir } = st.sort;
    return st.entries
      .filter((e) => (st.hidden || !e.hidden) && (!q || e.name.toLowerCase().includes(q)))
      .sort((a, b) => {
        if (a.isDir !== b.isDir) return a.isDir ? -1 : 1;
        const av = key === "size" ? a.size || 0 : key === "mtime" ? a.mtimeUtc || "" : a.name.toLowerCase();
        const bv = key === "size" ? b.size || 0 : key === "mtime" ? b.mtimeUtc || "" : b.name.toLowerCase();
        return (av < bv ? -1 : av > bv ? 1 : 0) * dir;
      });
  }

  function render() {
    if (st.closed) return;
    const rows = visible();
    $("#fmBody").innerHTML = rows.map((e) => `<tr data-p="${esc(e.path)}" class="${st.sel.has(e.path) ? "on" : ""}${e.hidden ? " dim" : ""}">
      <td><input type="checkbox" ${st.sel.has(e.path) ? "checked" : ""}></td>
      <td class="nm" title="${esc(e.name)}"><div>${fileIcon(e)}<span>${esc(e.name)}</span>${e.link ? `<em>↪</em>` : ""}</div></td>
      <td class="faint">${fmtTime(e.mtimeUtc)}</td>
      <td class="num">${e.isDir ? "" : bytes(e.size || 0)}</td></tr>`).join("");
    const empty = $("#fmEmpty");
    empty.hidden = rows.length > 0 || st.loading;
    empty.textContent = st.q ? "没有匹配的文件" : "空文件夹";
    $("#fmList").querySelectorAll("th[data-k]").forEach((th) => {
      th.querySelector(".arr").textContent = th.dataset.k === st.sort.key ? (st.sort.dir > 0 ? " ▲" : " ▼") : "";
    });
    paintSel();
  }

  // Selection changes only restyle existing rows: rebuilding them between the
  // two clicks of a double-click would swallow the dblclick event.
  function paintSel() {
    if (st.closed) return;
    const rows = visible();
    $("#fmBody").querySelectorAll("tr").forEach((tr) => {
      const on = st.sel.has(tr.dataset.p);
      tr.classList.toggle("on", on);
      tr.querySelector("input").checked = on;
    });
    const sel = selected();
    $("#fmAll").checked = rows.length > 0 && rows.every((e) => st.sel.has(e.path));
    $("#fmDown").disabled = !sel.length;
    $("#fmDel").disabled = !sel.length;
    $("#fmRename").disabled = sel.length !== 1;
    $("#fmBack").disabled = !st.back.length;
    $("#fmFwd").disabled = !st.fwd.length;
    $("#fmUp").disabled = !st.parent;
    const size = sel.reduce((n, e) => n + (e.isDir ? 0 : e.size || 0), 0);
    const hiddenN = st.entries.filter((e) => e.hidden).length;
    info(`${rows.length} 项${!st.hidden && hiddenN ? `（另有 ${hiddenN} 个隐藏）` : ""}` +
      (sel.length ? ` · 已选 ${sel.length} 项${size ? "，" + bytes(size) : ""}` : "") +
      " · 双击文件夹进入、双击文本/图片预览 · Delete 删除 · F2 重命名");
  }

  /** @param {string} path @param {"push" | "keep"} [how] */
  async function load(path, how) {
    st.loading = true;
    info("正在读取…");
    try {
      /** @type {FilesListResult} */
      const r = await api(`/sessions/${bios}/files?path=${encodeURIComponent(path)}`);
      if (st.closed) return;
      if (how === "push" && st.cwd && st.cwd !== r.path) { st.back.push(st.cwd); st.fwd = []; }
      st.cwd = r.path; st.sep = r.sep || (r.path.includes("\\") ? "\\" : "/");
      st.parent = r.parent || ""; st.home = r.home || st.home; st.roots = r.roots || st.roots;
      st.entries = r.entries || [];
      st.trash = !!r.trash;
      $("#fmTrash").hidden = !st.trash;
      if (how !== "keep") { st.sel.clear(); st.anchor = null; }
      else st.sel.forEach((p) => { if (!byPath(p)) st.sel.delete(p); });
      $("#fmPath").value = st.cwd;
      const roots = $("#fmRoots");
      const cur = st.roots.filter((x) => st.cwd.toLowerCase().startsWith(x.toLowerCase())).sort((a, b) => b.length - a.length)[0] || "";
      roots.innerHTML = st.roots.map((x) => `<option ${x === cur ? "selected" : ""}>${esc(x)}</option>`).join("");
      roots.hidden = st.roots.length < 2;
    } catch (e) {
      ask({ title: "无法打开", body: `<p><code>${esc(path)}</code></p><p class="warn">${esc(e.message)}</p>`, ok: "知道了" });
      $("#fmPath").value = st.cwd;
    } finally {
      st.loading = false;
      render();
    }
  }
  const go = (p) => load(p, "push");
  const reload = () => load(st.cwd, "keep");
  let reloadTimer = 0;
  const reloadSoon = () => { clearTimeout(reloadTimer); reloadTimer = setTimeout(reload, 400); };
  /** @type {import("./common.js").FilesCtx} */
  const fm = { bios, st, info, join, reloadSoon, download: (list) => download(list) };
  const { download, upload } = createTransfers(fm);

  // ----- toolbar -----
  $("#fmBack").addEventListener("click", () => { if (st.back.length) { st.fwd.push(st.cwd); load(st.back.pop()); } });
  $("#fmFwd").addEventListener("click", () => { if (st.fwd.length) { st.back.push(st.cwd); load(st.fwd.pop()); } });
  $("#fmUp").addEventListener("click", () => st.parent && go(st.parent));
  $("#fmHome").addEventListener("click", () => go("~"));
  $("#fmReload").addEventListener("click", reload);
  $("#fmRoots").addEventListener("change", (e) => go(e.target.value));
  $("#fmPath").addEventListener("keydown", (e) => { if (e.key === "Enter") go(e.target.value.trim() || "~"); });
  $("#fmFilter").addEventListener("input", (e) => { st.q = e.target.value.trim(); render(); });
  $("#fmHidden").checked = st.hidden;
  $("#fmHidden").addEventListener("change", (e) => {
    st.hidden = e.target.checked;
    localStorage.setItem("probeHubHidden", st.hidden ? "1" : "0");
    render();
  });
  $("#fmList").querySelectorAll("th[data-k]").forEach((th) => th.addEventListener("click", () => {
    const k = th.dataset.k;
    st.sort = { key: k, dir: st.sort.key === k ? -st.sort.dir : k === "name" ? 1 : -1 };
    render();
  }));
  $("#fmAll").addEventListener("change", (e) => {
    visible().forEach((x) => (e.target.checked ? st.sel.add(x.path) : st.sel.delete(x.path)));
    paintSel();
  });

  // ----- selection & open -----
  const body = $("#fmBody");
  body.addEventListener("click", (e) => {
    const tr = e.target.closest("tr");
    if (!tr) return;
    const p = tr.dataset.p, rows = visible().map((x) => x.path);
    if (e.target.type === "checkbox") {
      st.sel.has(p) ? st.sel.delete(p) : st.sel.add(p);
    } else if (e.shiftKey && st.anchor && rows.includes(st.anchor)) {
      const [a, b] = [rows.indexOf(st.anchor), rows.indexOf(p)].sort((x, y) => x - y);
      if (!(e.metaKey || e.ctrlKey)) st.sel.clear();
      rows.slice(a, b + 1).forEach((x) => st.sel.add(x));
    } else if (e.metaKey || e.ctrlKey) {
      st.sel.has(p) ? st.sel.delete(p) : st.sel.add(p);
      st.anchor = p;
    } else {
      st.sel.clear(); st.sel.add(p); st.anchor = p;
    }
    paintSel();
  });
  body.addEventListener("dblclick", (e) => {
    const tr = e.target.closest("tr");
    const it = tr && byPath(tr.dataset.p);
    if (!it) return;
    if (it.isDir) go(it.path); else preview(fm, it);
  });

  /** @param {string} v @param {string} [cur] */
  const nameCheck = (v, cur) => {
    if (!v) return "";
    if (/[\\/:*?"<>|]/.test(v)) return `<span class="bad">名称不能包含 \\ / : * ? " &lt; &gt; |</span>`;
    if (v !== cur && st.entries.some((e) => e.name.toLowerCase() === v.toLowerCase() && e.name !== cur))
      return `<span class="bad">当前目录已存在同名项目</span>`;
    return "";
  };

  // ----- file ops -----
  /** @param {FilesOpRequest} json @returns {Promise<FilesOpResult>} */
  const op = (json) => api(`/sessions/${bios}/files/op`, { method: "POST", json });
  $("#fmMkdir").addEventListener("click", async () => {
    const name = await ask({
      title: "新建文件夹", ok: "创建",
      body: `<p>位置：<code>${esc(st.cwd)}</code></p>`,
      input: { value: "新建文件夹", select: "all" }, check: (v) => nameCheck(v),
    });
    if (!name) return;
    try { await op({ op: "mkdir", path: join(st.cwd, name) }); await reload(); st.sel = new Set([join(st.cwd, name)]); paintSel(); }
    catch (e) { ask({ title: "新建失败", body: `<p>${esc(e.message)}</p>`, ok: "知道了" }); }
  });
  async function rename() {
    const [it] = selected();
    if (!it) return;
    const name = await ask({
      title: `重命名${it.isDir ? "文件夹" : "文件"}`, ok: "确认重命名",
      body: `<p>原名称：<code>${esc(it.name)}</code></p>`,
      input: { value: it.name, select: it.isDir ? "all" : "stem" },
      check: (v) => nameCheck(v, it.name) || (v && v !== it.name ? `<span class="ok">${esc(it.name)} → ${esc(v)}</span>` : `<span class="bad">名称未改变</span>`),
    });
    if (!name || name === it.name) return;
    try { const r = await op({ op: "rename", path: it.path, to: name }); await reload(); st.sel = new Set([r.path]); paintSel(); }
    catch (e) { ask({ title: "重命名失败", body: `<p>${esc(e.message)}</p>`, ok: "知道了" }); }
  }
  async function remove() {
    const list = selected();
    if (!list.length) return;
    const dirs = list.filter((x) => x.isDir).length;
    const size = list.reduce((n, e) => n + (e.isDir ? 0 : e.size || 0), 0);
    const strict = dirs > 0 || list.length > 5;
    const one = list.length === 1 ? (list[0].isDir ? "文件夹" : "文件") : "";
    const canTrash = st.trash;
    const perm = (on) => on || !canTrash;
    const extra = `${dirs ? `其中 ${dirs} 个文件夹会连同内部所有内容一起删除；` : ""}${size ? `文件合计 ${bytes(size)}；` : ""}`;
    const res = await ask({
      title: (on) => (perm(on) ? "彻底删除" : "删除") + (one ? `${one}？` : ` ${list.length} 项？`),
      ok: (on) => (perm(on) ? "彻底删除" : "删除"),
      danger: true,
      toggle: canTrash ? "彻底删除（不进回收站，无法恢复）" : "",
      word: (on) => (!perm(on) || !strict ? "" : one ? list[0].name : String(list.length)),
      wordLabel: one ? `请输入${one}名称` : "请输入要删除的项目数",
      body: (on) => `${listHTML(list)}${perm(on)
        ? `<p class="warn">${icon("triangle-alert")}${extra}删除后<b>无法恢复</b>${canTrash ? "" : "（该探针版本不支持回收站，升级到 1.6.0 及以上可用）"}。</p>`
        : `<p class="faint">${extra}将移到远端主机的回收站，可从回收站恢复。</p>`}
        <p class="faint">位置：<code>${esc(st.cwd)}</code></p>`,
    });
    if (!res) return;
    const permanent = perm(res.on);
    const fails = [];
    let noBin = false;
    for (const it of list) {
      info(`正在删除 ${it.name}…`);
      try { await op(permanent ? { op: "delete", path: it.path, confirm: true } : { op: "trash", path: it.path }); }
      catch (e) { fails.push(`${it.name}：${e.message}`); noBin = noBin || e.code === "not_supported"; }
    }
    await reload();
    if (fails.length) ask({
      title: "部分项目删除失败", ok: "知道了",
      body: `<ul class="fm-dlg-list">${fails.map((f) => `<li><span>${esc(f)}</span></li>`).join("")}</ul>
        ${noBin ? `<p class="faint">该位置没有回收站（如网络盘、U 盘或与回收站不在同一磁盘），如确需删除请勾选“彻底删除”。</p>` : ""}`,
    });
  }
  $("#fmRename").addEventListener("click", rename);
  $("#fmDel").addEventListener("click", remove);
  $("#fmDown").addEventListener("click", () => download(selected()));
  $("#fmTrash").addEventListener("click", () => openTrash(fm));

  function onKey(e) {
    if (st.closed || $("#modal").hidden) return;
    if (view.cur && !activeDialog()) {
      if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); view.cur.close(); }
      else if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "a" && !e.target.closest?.("input, textarea, select")) { e.preventDefault(); view.cur.selectAll(); }
      return;
    }
    if (activeDialog()) {
      if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); activeDialog().cancel(); }
      else if (e.key === "Enter") { e.preventDefault(); e.stopPropagation(); activeDialog().submit(); }
      return;
    }
    if (e.target.closest("input, textarea, select")) return;
    if (!$("#fmList")) return;
    if (e.key === "Delete" || (e.key === "Backspace" && (e.metaKey || e.ctrlKey))) { e.preventDefault(); remove(); }
    else if (e.key === "F2") { e.preventDefault(); rename(); }
    else if (e.key === "Backspace" && st.parent) { e.preventDefault(); go(st.parent); }
    else if ((e.metaKey || e.ctrlKey) && e.key === "a") { e.preventDefault(); visible().forEach((x) => st.sel.add(x.path)); paintSel(); }
    else if (e.key === "Enter") {
      const list = selected();
      if (list.length === 1) list[0].isDir ? go(list[0].path) : preview(fm, list[0]);
    }
  }
  document.addEventListener("keydown", onKey, true);

  $("#fmUpFiles").addEventListener("click", () => $("#fmPickFiles").click());
  $("#fmUpDir").addEventListener("click", () => $("#fmPickDir").click());
  for (const id of ["#fmPickFiles", "#fmPickDir"]) {
    $(id).addEventListener("change", (e) => {
      upload([...e.target.files].map((f) => ({ file: f, rel: f.webkitRelativePath || f.name })));
      e.target.value = "";
    });
  }

  // Drag & drop, including folders via the entries API.
  const listEl = $("#fmList"), drop = $("#fmDrop");
  let depth = 0;
  listEl.addEventListener("dragenter", (e) => { if (e.dataTransfer.types.includes("Files")) { depth++; drop.hidden = false; } });
  listEl.addEventListener("dragleave", () => { if (--depth <= 0) { depth = 0; drop.hidden = true; } });
  listEl.addEventListener("dragover", (e) => { if (e.dataTransfer.types.includes("Files")) e.preventDefault(); });
  listEl.addEventListener("drop", async (e) => {
    e.preventDefault();
    depth = 0; drop.hidden = true;
    const entries = [...e.dataTransfer.items].map((i) => i.webkitGetAsEntry && i.webkitGetAsEntry()).filter(Boolean);
    if (!entries.length) return upload([...e.dataTransfer.files].map((f) => ({ file: f, rel: f.name })));
    const out = [];
    const walk = async (ent, prefix) => {
      if (ent.isFile) {
        const file = await new Promise((res, rej) => ent.file(res, rej));
        out.push({ file, rel: prefix + ent.name });
      } else if (ent.isDirectory) {
        const reader = ent.createReader();
        for (;;) {
          const batch = await new Promise((res, rej) => reader.readEntries(res, rej));
          if (!batch.length) break;
          for (const c of batch) await walk(c, prefix + ent.name + "/");
        }
      }
    };
    info("正在读取拖入的文件…");
    for (const ent of entries) await walk(ent, "");
    upload(out);
    render();
  });

  go("~");
}
