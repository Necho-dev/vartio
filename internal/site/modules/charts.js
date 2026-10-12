// @ts-check
// Minimal SVG time-series charts (area + line, gaps, hover tooltip). No dependencies.
import { esc } from "./core.js";

const NS = "http://www.w3.org/2000/svg";
const PAD = { l: 54, r: 10, t: 8, b: 22 };
let tipEl = null;
const ro = new ResizeObserver((entries) => {
  for (const e of entries) {
    const box = /** @type {HTMLElement} */ (e.target);
    if (box.__chart) draw(box);
  }
});

function el(name, attrs, parent) {
  const n = document.createElementNS(NS, name);
  for (const k in attrs) n.setAttribute(k, attrs[k]);
  if (parent) parent.appendChild(n);
  return n;
}

function niceMax(v) {
  if (!(v > 0)) return 1;
  const p = Math.pow(10, Math.floor(Math.log10(v)));
  for (const m of [1, 1.2, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10]) if (m * p >= v) return m * p;
  return 10 * p;
}

function niceFloor(v) {
  if (!(v > 0)) return 0;
  const p = Math.pow(10, Math.floor(Math.log10(v)));
  let best = 0;
  for (const m of [1, 1.2, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10]) {
    const n = m * p;
    if (n <= v) best = n;
  }
  return best;
}

// fit "ceil" keeps the floor at 0 and shrinks the top to the data.
// fit "band" also lifts the floor when every point sits in a narrow band.
function axisRange(dataMin, dataMax, fit, hardMax, fitMin) {
  if (!(dataMax > 0)) return { min: 0, max: Math.max(hardMax || 1, fitMin || 0) };
  if (fit === "band" && dataMin > 0) {
    const span = Math.max(dataMax - dataMin, dataMax * 0.04);
    let min = niceFloor(dataMin - span * 0.8);
    if (!(min < dataMin)) min = 0;
    const max = niceMax(dataMax + span * 0.35);
    return { min, max: max > min ? max : min + 1 };
  }
  if (fit === "ceil") return { min: 0, max: Math.max(fitMin || 0, niceMax(dataMax * 1.05)) };
  return { min: 0, max: hardMax != null ? hardMax : niceMax(dataMax * 1.05) };
}

function timeLabel(ms, span) {
  const d = new Date(ms);
  const hh = String(d.getHours()).padStart(2, "0");
  const mm = String(d.getMinutes()).padStart(2, "0");
  if (span > 36 * 3600e3) return `${d.getMonth() + 1}/${d.getDate()} ${hh}:${mm}`;
  return `${hh}:${mm}`;
}

function tip() {
  if (!tipEl) {
    tipEl = document.createElement("div");
    tipEl.className = "tip";
    tipEl.hidden = true;
    document.body.appendChild(tipEl);
    window.addEventListener("scroll", () => (tipEl.hidden = true), { passive: true });
    window.addEventListener("hashchange", () => (tipEl.hidden = true));
  }
  return tipEl;
}

function draw(box) {
  const o = box.__chart;
  box.innerHTML = "";
  const W = box.clientWidth, H = box.clientHeight;
  if (W < 40 || H < 40) return;
  const svg = el("svg", { viewBox: `0 0 ${W} ${H}`, preserveAspectRatio: "none" }, box);
  const t = o.t || [];
  const to = o.to || Date.now();
  const from = o.from || (t.length ? t[0] : to - 3600e3);
  const span = Math.max(1, to - from);
  const dual = !!o.fmt2 && o.series.some((s) => s.right);
  const padR = dual ? PAD.l : PAD.r;
  const iw = W - PAD.l - padR, ih = H - PAD.t - PAD.b;
  const x = (ms) => PAD.l + ((ms - from) / span) * iw;

  let dataMax = 0, dataMax2 = 0, dataMin = Infinity, dataMin2 = Infinity;
  const stacked = !!o.stacked;
  const acc = stacked ? t.map(() => 0) : null;
  const plotted = o.series.map((s) => {
    // Overlays (dashed peak) and tooltip-only rows (idle) stay out of the stack and off the scale.
    const inStack = stacked && !s.tipOnly && s.area !== false;
    const lower = inStack ? acc.slice() : null;
    const vals = s.data.map((v, i) => {
      if (v == null || isNaN(v)) return null;
      if (inStack) { acc[i] += v; return acc[i]; }
      return v;
    });
    const right = dual && !!s.right;
    if (!s.tipOnly) {
      for (const v of vals) {
        if (v == null) continue;
        if (right) { dataMax2 = Math.max(dataMax2, v); dataMin2 = Math.min(dataMin2, v); }
        else { dataMax = Math.max(dataMax, v); dataMin = Math.min(dataMin, v); }
      }
    }
    return { ...s, vals, lower, right, inStack };
  });
  const left = axisRange(dataMin, dataMax, o.fit, o.max, o.fitMin);
  const yMin = left.min, yMax = left.max;
  const rightAxis = axisRange(dataMin2, dataMax2, o.fit, null);
  const yMin2 = rightAxis.min, yMax2 = rightAxis.max;
  const yOf = (min, max) => (v) => {
    const span = max - min || 1;
    return PAD.t + ih - ((Math.min(Math.max(v, min), max) - min) / span) * ih;
  };
  const y = yOf(yMin, yMax), y2 = yOf(yMin2, yMax2);

  const ticks = 4;
  for (let i = 0; i <= ticks; i++) {
    const v = yMin + ((yMax - yMin) / ticks) * i, yy = y(v);
    el("line", { x1: PAD.l, x2: W - padR, y1: yy, y2: yy, class: "gl" }, svg);
    const lab = el("text", { x: PAD.l - 8, y: yy + 3.5, "text-anchor": "end", class: "axis" }, svg);
    lab.textContent = o.fmt(v, true);
    if (dual) {
      const lab2 = el("text", { x: W - padR + 8, y: yy + 3.5, "text-anchor": "start", class: "axis" }, svg);
      lab2.textContent = o.fmt2(yMin2 + ((yMax2 - yMin2) / ticks) * i, true);
    }
  }
  const xt = Math.max(2, Math.min(6, Math.floor(iw / 110)));
  for (let i = 0; i <= xt; i++) {
    const ms = from + (span / xt) * i;
    const lab = el("text", { x: x(ms), y: H - 5, "text-anchor": i === 0 ? "start" : i === xt ? "end" : "middle", class: "axis" }, svg);
    lab.textContent = timeLabel(ms, span);
  }

  if (!t.length) {
    const nd = document.createElement("div");
    nd.className = "nodata";
    nd.textContent = o.empty || "暂无数据";
    box.appendChild(nd);
    placeNote(box);
    return;
  }

  const gap = (o.stepSec || 10) * 1000 * 2.5;
  const defs = el("defs", {}, svg);
  const areas = plotted.filter((s) => !s.tipOnly && s.area !== false);
  const lines = plotted.filter((s) => !s.tipOnly && s.area === false);
  const order = [...(stacked ? [...areas].reverse() : areas), ...lines];
  order.forEach((s, si) => {
    const gid = `g${Math.random().toString(36).slice(2)}${si}`;
    const lg = el("linearGradient", { id: gid, x1: 0, y1: 0, x2: 0, y2: 1 }, defs);
    el("stop", { offset: "0%", "stop-color": s.color, "stop-opacity": s.area === false ? 0 : stacked ? 0.5 : s.fill ?? 0.38 }, lg);
    el("stop", { offset: "100%", "stop-color": s.color, "stop-opacity": s.area === false ? 0 : stacked ? 0.28 : 0.03 }, lg);
    const segs = [];
    let cur = [];
    for (let i = 0; i < t.length; i++) {
      const v = s.vals[i];
      if (v == null || (cur.length && t[i] - t[i - 1] > gap)) {
        if (cur.length) segs.push(cur);
        cur = [];
        if (v == null) continue;
      }
      const yy = s.right ? y2 : y;
      cur.push([x(t[i]), yy(v), s.inStack ? yy(s.lower[i]) : PAD.t + ih]);
    }
    if (cur.length) segs.push(cur);
    s._segs = segs.map((seg) => (seg.length === 1 ? [seg[0], [seg[0][0] + 1.5, seg[0][1], seg[0][2]]] : seg));
    s._gid = gid;
  });
  order.forEach((s) => {
    if (s.area === false) return;
    for (const seg of s._segs) {
      const line = seg.map((p, i) => (i ? "L" : "M") + p[0].toFixed(1) + " " + p[1].toFixed(1)).join("");
      const back = seg.slice().reverse().map((p) => "L" + p[0].toFixed(1) + " " + p[2].toFixed(1)).join("");
      el("path", { d: `${line}${back}Z`, fill: `url(#${s._gid})` }, svg);
    }
  });
  order.forEach((s) => {
    for (const seg of s._segs) {
      const line = seg.map((p, i) => (i ? "L" : "M") + p[0].toFixed(1) + " " + p[1].toFixed(1)).join("");
      el("path", { d: line, fill: "none", stroke: s.color, "stroke-width": s.width || 1.6, "stroke-dasharray": s.dash ? "4 3" : "none", "stroke-linejoin": "round" }, svg);
    }
  });

  const cursor = el("line", { y1: PAD.t, y2: PAD.t + ih, class: "cursor", visibility: "hidden" }, svg);
  const dots = plotted.map((s) => s.tipOnly ? null : el("circle", { r: 3, fill: s.color, visibility: "hidden" }, svg));
  const hit = el("rect", { x: PAD.l, y: PAD.t, width: iw, height: ih, fill: "transparent" }, svg);
  const tp = tip();
  const hide = () => {
    tp.hidden = true;
    cursor.setAttribute("visibility", "hidden");
    dots.forEach((d) => { if (d) d.setAttribute("visibility", "hidden"); });
  };
  let hideTimer = 0;
  /** @param {PointerEvent} ev */
  const move = (ev) => {
    clearTimeout(hideTimer);
    const r = svg.getBoundingClientRect();
    const mx = ((ev.clientX - r.left) / r.width) * W;
    const ms = from + ((mx - PAD.l) / iw) * span;
    let lo = 0, hi = t.length - 1;
    while (lo < hi) { const mid = (lo + hi) >> 1; if (t[mid] < ms) lo = mid + 1; else hi = mid; }
    if (lo > 0 && Math.abs(t[lo - 1] - ms) < Math.abs(t[lo] - ms)) lo--;
    const cx = x(t[lo]);
    cursor.setAttribute("x1", cx); cursor.setAttribute("x2", cx); cursor.setAttribute("visibility", "visible");
    let rows = "", stackN = 0;
    plotted.forEach((s, i) => {
      const v = s.vals[lo], raw = s.data[lo];
      if (v == null) { if (dots[i]) dots[i].setAttribute("visibility", "hidden"); return; }
      if (s.inStack) stackN++;
      if (dots[i]) {
        dots[i].setAttribute("cx", cx); dots[i].setAttribute("cy", (s.right ? y2 : y)(v)); dots[i].setAttribute("visibility", "visible");
      }
      rows += `<div class="r"><span><i style="background:${s.color}"></i>${esc(s.label)}</span><b>${(s.right ? o.fmt2 : o.fmt)(raw)}</b></div>`;
    });
    if (stacked && o.totalLabel && stackN > 1 && acc[lo]) {
      rows += `<div class="r sum"><span>${o.totalLabel}</span><b>${o.fmt(acc[lo])}${o.capacity ? ` / ${o.fmt(o.capacity)}` : ""}</b></div>`;
    }
    if (o.tipExtra) rows += o.tipExtra(lo) || "";
    const d = new Date(t[lo]);
    tp.innerHTML = `<div class="t">${d.toLocaleString()}</div>${rows}`;
    tp.hidden = false;
    const tw = tp.offsetWidth;
    let left = ev.clientX + 14;
    if (left + tw > window.innerWidth - 8) left = ev.clientX - tw - 14;
    tp.style.left = Math.max(8, left) + "px";
    tp.style.top = ev.clientY - 10 + "px";
  };
  hit.addEventListener("pointermove", move);
  hit.addEventListener("pointerdown", move);
  // A tap ends with pointerleave right away; keep the reading up long enough to read it.
  hit.addEventListener("pointerleave", (ev) => {
    clearTimeout(hideTimer);
    if (ev.pointerType === "touch") hideTimer = setTimeout(hide, 2500);
    else hide();
  });
  placeNote(box);
}

function placeNote(box) {
  const note = box.__chart && box.__chart.note;
  if (!note) return;
  const n = document.createElement("div");
  n.className = "chart-note";
  n.innerHTML = note;
  box.appendChild(n);
}

/**
 * @param {HTMLElement} box
 * @param {ChartOpts} opts
 */
export function renderChart(box, opts) {
  const first = !box.__chart;
  box.__chart = opts;
  if (first) ro.observe(box);
  draw(box);
}
