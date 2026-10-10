// @ts-check
import { $, api } from "./core.js";
import { openModal, closeModal } from "./ui.js";
import { icon } from "./icons.js";

let xtermLoaded = null;
function loadXterm() {
  if (xtermLoaded) return xtermLoaded;
  const css = document.createElement("link");
  css.rel = "stylesheet";
  css.href = "vendor/xterm.css";
  document.head.appendChild(css);
  const js = (src) => new Promise((ok, fail) => {
    const el = document.createElement("script");
    el.src = src;
    el.onload = ok;
    el.onerror = () => fail(new Error("加载 " + src + " 失败"));
    document.head.appendChild(el);
  });
  // Fonts must be ready before xterm measures cells, or glyphs misalign / icons render as boxes.
  const font = document.fonts ? Promise.all([
    document.fonts.load('13px "JetBrains Mono"'),
    document.fonts.load('13px "Symbols Nerd Font Mono"', "\ue0b0\uf015"),
  ]).catch(() => {}) : Promise.resolve();
  xtermLoaded = Promise.all([js("vendor/xterm.js"), font])
    .then(() => Promise.all([js("vendor/addon-fit.js"), js("vendor/addon-unicode11.js")]));
  return xtermLoaded;
}

const TERM_FONT = '"JetBrains Mono", "Cascadia Mono", "SF Mono", SFMono-Regular, Menlo, Consolas, "Symbols Nerd Font Mono", "PingFang SC", "Microsoft YaHei", monospace';

const TERM_MAX = 8;
const TERM_THEMES = {
  dark: {
    background: "#0b0b0e", foreground: "#e4e4e7", cursor: "#22c55e", cursorAccent: "#0b0b0e",
    selectionBackground: "#3f3f4699", scrollbarSliderBackground: "#3a3a4266",
    scrollbarSliderHoverBackground: "#55555e88", scrollbarSliderActiveBackground: "#6b6b75aa",
  },
  // ANSI white/yellow are darkened so they stay readable on a light background.
  light: {
    background: "#fafafa", foreground: "#27272a", cursor: "#16a34a", cursorAccent: "#fafafa",
    selectionBackground: "#93c5fd80", scrollbarSliderBackground: "#a1a1aa55",
    scrollbarSliderHoverBackground: "#71717a77", scrollbarSliderActiveBackground: "#52525b99",
    black: "#27272a", red: "#dc2626", green: "#15803d", yellow: "#a16207", blue: "#1d4ed8", magenta: "#9333ea", cyan: "#0e7490", white: "#52525b",
    brightBlack: "#71717a", brightRed: "#ef4444", brightGreen: "#16a34a", brightYellow: "#b45309", brightBlue: "#2563eb", brightMagenta: "#a855f7", brightCyan: "#0891b2", brightWhite: "#3f3f46",
  },
};
const termTheme = () => TERM_THEMES[document.documentElement.dataset.theme === "light" ? "light" : "dark"];

// Each tab is its own ticket + WebSocket + agent PTY; closing the modal ends them all.
/** @param {SystemView} s */
export async function openTerm(s) {
  const tabs = [];
  let active = null, seq = 0, ro = null, closed = false;
  const base = `终端 · ${s.name}`;
  openModal(base, `<div class="term-tabs" id="termTabs"><button class="term-add" id="termAdd" title="新建标签页">${icon("plus")}</button></div>
    <div class="term" id="term"></div><p class="modal-foot" id="termInfo">正在连接…</p>`, () => {
    closed = true;
    if (ro) ro.disconnect();
    tabs.splice(0).forEach(dispose);
  }, { wide: true, fullscreen: true });

  const info = (t) => {
    if (t !== active) return;
    const el = $("#termInfo");
    if (!el) return;
    el.textContent = t.dead ? "会话已结束 · 点击 + 新建标签页"
      : t.ws && t.ws.readyState === 1 ? `已连接 · ${t.term.cols}×${t.term.rows} · ${tabs.length}/${TERM_MAX} 个会话 · 空闲 15 分钟自动断开 · 双击标题或右上角按钮铺满页面`
      : "正在连接…";
  };
  const label = (t) => {
    t.btn.querySelector(".lbl").textContent = t.title ? `${t.n} · ${t.title}` : `会话 ${t.n}`;
    t.btn.title = t.title || `会话 ${t.n}`;
    t.btn.classList.toggle("dead", t.dead);
    if (t === active) $("#modalTitle").textContent = t.title ? `${base} — ${t.title}` : base;
  };
  const fitActive = () => {
    if (!active) return;
    try { active.fit.fit(); } catch (e) { /* disposed */ }
  };
  function dispose(t) {
    if (t.ws) { t.ws.onclose = null; t.ws.close(); }
    if (t.term) t.term.dispose();
  }
  function activate(t) {
    active = t;
    tabs.forEach((x) => { x.pane.classList.toggle("on", x === t); x.btn.classList.toggle("on", x === t); });
    fitActive();
    t.term.focus();
    label(t);
    info(t);
  }
  function closeTab(t) {
    const i = tabs.indexOf(t);
    if (i < 0) return;
    tabs.splice(i, 1);
    dispose(t);
    t.pane.remove();
    t.btn.remove();
    $("#termAdd").disabled = tabs.length >= TERM_MAX;
    if (!tabs.length) return closeModal();
    if (active === t) activate(tabs[Math.min(i, tabs.length - 1)]);
    else info(active);
  }
  async function addTab() {
    if (closed || tabs.length >= TERM_MAX) return;
    const t = { n: ++seq, title: "", dead: false, ws: null };
    t.btn = document.createElement("button");
    t.btn.className = "term-tab";
    t.btn.innerHTML = `<span class="dot"></span><span class="lbl"></span><span class="x" title="关闭">${icon("x")}</span>`;
    t.btn.addEventListener("click", (e) => {
      if (e.target.classList.contains("x")) closeTab(t); else activate(t);
    });
    t.btn.addEventListener("auxclick", (e) => { if (e.button === 1) closeTab(t); });
    $("#termTabs").insertBefore(t.btn, $("#termAdd"));
    t.pane = document.createElement("div");
    t.pane.className = "term-pane";
    $("#term").appendChild(t.pane);
    tabs.push(t);
    $("#termAdd").disabled = tabs.length >= TERM_MAX;

    t.term = new Terminal({
      allowProposedApi: true, cursorBlink: true, fontSize: 13, lineHeight: 1.15, scrollback: 5000,
      fontFamily: TERM_FONT, rescaleOverlappingGlyphs: true, theme: termTheme(),
    });
    t.fit = new FitAddon.FitAddon();
    t.term.loadAddon(t.fit);
    t.term.loadAddon(new Unicode11Addon.Unicode11Addon());
    t.term.unicode.activeVersion = "11";
    t.term.open(t.pane);
    t.fit.fit();
    label(t);
    activate(t);
    t.term.onTitleChange((x) => { t.title = x || ""; label(t); });

    try {
      const tk = await api(`/sessions/${encodeURIComponent(s.id)}/term`, { method: "POST" });
      if (closed || !tabs.includes(t)) return;
      const proto = location.protocol === "https:" ? "wss:" : "ws:";
      const ws = new WebSocket(`${proto}//${location.host}${tk.ws}&cols=${t.term.cols}&rows=${t.term.rows}`);
      t.ws = ws;
      ws.binaryType = "arraybuffer";
      const enc = new TextEncoder();
      ws.onopen = () => info(t);
      ws.onmessage = (ev) => t.term.write(typeof ev.data === "string" ? ev.data : new Uint8Array(ev.data));
      ws.onclose = () => {
        t.dead = true;
        t.term.write("\r\n\x1b[90m[连接已关闭]\x1b[0m\r\n");
        label(t);
        info(t);
      };
      t.term.onData((d) => { if (ws.readyState === 1) ws.send(enc.encode(d)); });
      t.term.onResize(({ cols, rows }) => {
        if (ws.readyState === 1) ws.send(JSON.stringify({ type: "resize", cols, rows }));
        info(t);
      });
    } catch (e) {
      t.dead = true;
      t.term.write(`\x1b[31m${e.message}\x1b[0m\r\n`);
      label(t);
      info(t);
    }
  }

  try {
    await loadXterm();
    if (closed) return;
    $("#termAdd").addEventListener("click", addTab);
    let pending = 0;
    ro = new ResizeObserver(() => {
      cancelAnimationFrame(pending);
      pending = requestAnimationFrame(fitActive);
    });
    ro.observe($("#term"));
    await addTab();
  } catch (e) {
    const el = $("#termInfo");
    if (el) el.textContent = e.message;
  }
}
