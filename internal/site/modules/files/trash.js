// @ts-check
import { $, esc, bytes, fmtTime, api } from "../core.js";
import { icon } from "../icons.js";
import { fileIcon, listHTML, view } from "./common.js";
import { ask } from "./dialog.js";

// Recycle bin overlay: list, restore, purge and empty via /sessions/{id}/trash.
/** @param {import("./common.js").FilesCtx} fm */
export async function openTrash(fm) {
  if (view.cur) view.cur.close();
  const box = document.createElement("div");
  box.className = "fm-view fm-trash";
  box.innerHTML = `<div class="fm-vhead">${icon("trash", "fi file")}<b>回收站</b><span class="faint" data-v="meta"></span>
      <span class="fm-vtools">
        <button class="btn sm" data-v="restore" disabled>${icon("rotate-ccw")}恢复</button>
        <button class="btn sm danger" data-v="purge" disabled>彻底删除</button>
        <button class="btn sm danger" data-v="empty" disabled>清空回收站</button>
        <button class="icon-btn" data-v="reload" title="刷新">${icon("refresh-cw")}</button>
        <button class="icon-btn" data-v="close" title="关闭（Esc）">${icon("x")}</button></span></div>
    <div class="fm-tbody">
      <div class="fm-list"><table class="fm-table"><colgroup><col style="width:34px"><col style="width:30%"><col><col style="width:150px"><col style="width:96px"></colgroup>
        <thead><tr><th><input type="checkbox" data-v="all"></th><th>名称</th><th>原位置</th><th>删除时间</th><th>大小</th></tr></thead>
        <tbody></tbody></table><div class="fm-empty" data-v="empty-msg">正在读取…</div></div>
      <p class="modal-foot" data-v="foot"></p></div>`;
  $(".modal-box").appendChild(box);
  const q = (/** @type {string} */ k) => $(`[data-v="${k}"]`, box), tbody = box.querySelector("tbody");
  const ts = { items: [], sel: new Set(), anchor: null, loc: "" };
  const chosen = () => ts.items.filter((x) => ts.sel.has(x.id));
  view.cur = {
    close: () => { box.remove(); view.cur = null; },
    selectAll: () => { ts.items.forEach((x) => ts.sel.add(x.id)); paint(); },
  };
  /** @param {TrashItem} it */
  const where = (it) => it.orig ? it.orig.slice(0, it.orig.length - it.name.length).replace(/[\\/]$/, "") || it.orig : "";
  function paint() {
    tbody.querySelectorAll("tr").forEach((tr) => {
      const on = ts.sel.has(tr.dataset.id);
      tr.classList.toggle("on", on);
      tr.querySelector("input").checked = on;
    });
    const sel = chosen();
    q("all").checked = ts.items.length > 0 && sel.length === ts.items.length;
    q("restore").disabled = q("purge").disabled = !sel.length;
    q("empty").disabled = !ts.items.length;
    const size = ts.items.reduce((n, e) => n + (e.size || 0), 0);
    q("meta").textContent = ts.items.length ? `${ts.items.length} 项 · ${bytes(size)}` : "";
    q("foot").textContent = (sel.length ? `已选 ${sel.length} 项 · ` : "") + `位置：${ts.loc}` +
      (ts.items.some((x) => !x.orig) ? " · 原位置未知的项目会恢复到主目录" : "");
  }
  function render() {
    tbody.innerHTML = ts.items.map((it) => `<tr data-id="${esc(it.id)}">
      <td><input type="checkbox"></td>
      <td class="nm" title="${esc(it.name)}"><div>${fileIcon(it)}<span>${esc(it.name)}</span></div></td>
      <td class="mono faint" title="${esc(it.orig || "")}">${it.orig ? esc(where(it)) : "<i>未知</i>"}</td>
      <td class="faint">${fmtTime(it.deletedUtc)}</td>
      <td class="num">${it.isDir && !it.size ? "" : bytes(it.size || 0)}</td></tr>`).join("");
    const msg = q("empty-msg");
    msg.hidden = ts.items.length > 0;
    msg.textContent = "回收站是空的";
    paint();
  }
  /** @param {TrashResult} r */
  const apply = (r) => {
    ts.items = r.items || []; ts.loc = r.location || ts.loc;
    ts.sel.forEach((id) => { if (!ts.items.some((x) => x.id === id)) ts.sel.delete(id); });
    render();
  };
  /** @param {TrashRequest} [json] @returns {Promise<TrashResult>} */
  const trashAPI = (json) => api(`/sessions/${fm.sid}/trash`, json ? { method: "POST", json } : {});
  const fail = (e) => {
    const msg = q("empty-msg");
    msg.hidden = false;
    msg.textContent = e.code === "not_implemented" ? "该探针版本不支持回收站管理，请升级到 1.7.0 及以上" : `读取失败：${e.message}`;
  };
  async function load() {
    try { apply(await trashAPI()); } catch (e) { if (view.cur) fail(e); }
  }
  /** @param {string} title @param {TrashResult} r */
  function report(title, r) {
    if (!r.failed || !r.failed.length) return;
    const name = (id) => (ts.items.find((x) => x.id === id) || {}).name || id;
    ask({ title, ok: "知道了", body: `<ul class="fm-dlg-list">${r.failed.map((f) => `<li><span>${esc(name(f.id))}：${esc(f.message)}</span></li>`).join("")}</ul>` });
  }
  /** @param {TrashRequest["op"]} op @param {string[]} ids @param {string} title */
  async function run(op, ids, title) {
    try {
      const r = await trashAPI({ op, ids, confirm: true });
      apply(r);
      if (op === "restore") fm.reloadSoon();
      report(title, r);
      return r;
    } catch (e) {
      ask({ title, ok: "知道了", body: `<p>${esc(e.message)}</p>` });
    }
  }

  tbody.addEventListener("click", (/** @type {any} */ e) => {
    const tr = e.target.closest("tr");
    if (!tr) return;
    const id = tr.dataset.id, ids = ts.items.map((x) => x.id);
    if (e.target.type === "checkbox" || e.metaKey || e.ctrlKey) {
      ts.sel.has(id) ? ts.sel.delete(id) : ts.sel.add(id);
      ts.anchor = id;
    } else if (e.shiftKey && ts.anchor && ids.includes(ts.anchor)) {
      const [a, b] = [ids.indexOf(ts.anchor), ids.indexOf(id)].sort((x, y) => x - y);
      ts.sel.clear();
      ids.slice(a, b + 1).forEach((x) => ts.sel.add(x));
    } else {
      ts.sel.clear(); ts.sel.add(id); ts.anchor = id;
    }
    paint();
  });
  q("all").addEventListener("change", (/** @type {any} */ e) => { ts.items.forEach((x) => (e.target.checked ? ts.sel.add(x.id) : ts.sel.delete(x.id))); paint(); });
  q("close").addEventListener("click", () => view.cur && view.cur.close());
  q("reload").addEventListener("click", load);
  q("restore").addEventListener("click", async () => {
    const list = chosen();
    if (!list.length) return;
    const ok = await ask({
      title: `恢复 ${list.length} 项？`, ok: "恢复",
      body: `<ul class="fm-dlg-list">${list.slice(0, 8).map((it) => `<li>${fileIcon(it)}<span>${esc(it.orig || it.name + "（原位置未知，恢复到主目录）")}</span></li>`).join("")}${list.length > 8 ? `<li class="more">…以及另外 ${list.length - 8} 项</li>` : ""}</ul>
        <p class="faint">恢复到原位置；如已存在同名项目，会自动改名为“名称 2”。</p>`,
    });
    if (ok) run("restore", list.map((x) => x.id), "部分项目恢复失败");
  });
  q("purge").addEventListener("click", async () => {
    const list = chosen();
    if (!list.length) return;
    const strict = list.some((x) => x.isDir) || list.length > 5;
    const one = list.length === 1 ? (list[0].isDir ? "文件夹" : "文件") : "";
    const ok = await ask({
      title: one ? `彻底删除${one}？` : `彻底删除 ${list.length} 项？`, ok: "彻底删除", danger: true,
      word: !strict ? "" : one ? list[0].name : String(list.length),
      wordLabel: one ? `请输入${one}名称` : "请输入要删除的项目数",
      body: `${listHTML(list)}<p class="warn">${icon("triangle-alert")}将从回收站中移除，<b>无法恢复</b>。</p>`,
    });
    if (ok) run("purge", list.map((x) => x.id), "部分项目删除失败");
  });
  q("empty").addEventListener("click", async () => {
    const n = ts.items.length;
    if (!n) return;
    const ok = await ask({
      title: "清空回收站？", ok: "清空回收站", danger: true,
      word: String(n), wordLabel: "请输入回收站中的项目数",
      body: `<p>回收站中共有 <b>${n}</b> 项，合计 ${bytes(ts.items.reduce((s, e) => s + (e.size || 0), 0))}。</p>
        <p class="warn">${icon("triangle-alert")}清空后全部<b>无法恢复</b>。</p><p class="faint">位置：<code>${esc(ts.loc)}</code></p>`,
    });
    if (ok) run("empty", [], "部分项目未能清除");
  });
  load();
}
