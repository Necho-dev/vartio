// @ts-check

export const API = "/v1/probe";

/**
 * DOM lookups are typed loosely: the markup is ours and every id is known.
 * @param {string} s
 * @param {ParentNode} [r]
 * @returns {any}
 */
export const $ = (s, r = document) => r.querySelector(s);

export const app = $("#app");

/**
 * @type {{
 *   token: string, config: HubConfig | null, systems: SystemView[], q: string, chip: string,
 *   sort: { key: string, dir: number }, range: string, evCat: string, timers: number[],
 * }}
 */
export const state = {
  token: localStorage.getItem("probeHubToken") || "",
  config: null,
  systems: [],
  q: "",
  chip: "all",
  sort: { key: "name", dir: 1 },
  range: localStorage.getItem("probeHubRange") || "1h",
  evCat: "",
  timers: [],
};

// ---------- html ----------
/** @param {unknown} s */
export const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));

class SafeHTML {
  /** @param {string} s */
  constructor(s) { this.s = s; }
  toString() { return this.s; }
}
/**
 * Marks trusted markup so html`` does not escape it.
 * @param {string} s
 */
export const raw = (s) => new SafeHTML(s);
/**
 * Tagged template that escapes every interpolation except html``/raw() results;
 * arrays are joined, null/undefined/false render as nothing.
 * @param {TemplateStringsArray} strings
 * @param {...unknown} vals
 * @returns {SafeHTML}
 */
export function html(strings, ...vals) {
  /** @param {unknown} v @returns {string} */
  const one = (v) => v == null || v === false ? "" : v instanceof SafeHTML ? v.s : Array.isArray(v) ? v.map(one).join("") : esc(v);
  let out = strings[0];
  vals.forEach((v, i) => { out += one(v) + strings[i + 1]; });
  return new SafeHTML(out);
}

// ---------- format ----------
/** @param {number | null | undefined} v @param {boolean} [axis] */
export function bytes(v, axis) {
  if (v == null || isNaN(v)) return "—";
  const u = ["B", "KB", "MB", "GB", "TB", "PB"];
  let i = 0, n = Math.abs(v);
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  const d = axis ? (n >= 100 || n === 0 ? 0 : n >= 10 ? 0 : 1) : n >= 100 ? 0 : n >= 10 ? 1 : 2;
  return (v < 0 ? "-" : "") + n.toFixed(d) + " " + u[i];
}
/** @param {number | null | undefined} v @param {boolean} [axis] */
export const rate = (v, axis) => (v == null ? "—" : bytes(v, axis) + "/s");
/** @param {number | null | undefined} v @param {boolean} [axis] */
export const pct = (v, axis) => {
  if (v == null || isNaN(v)) return "—";
  const digits = axis ? (Math.abs(v - Math.round(v)) > 0.05 && v < 10 ? 1 : 0) : 1;
  return v.toFixed(digits) + "%";
};
/** @param {number} sec */
export function dur(sec) {
  if (!(sec > 0)) return "—";
  const d = Math.floor(sec / 86400), h = Math.floor((sec % 86400) / 3600), m = Math.floor((sec % 3600) / 60);
  if (d) return `${d} 天 ${h} 小时`;
  if (h) return `${h} 小时 ${m} 分`;
  return `${m} 分钟`;
}
/** @param {string} ts */
export function ago(ts) {
  const s = (Date.now() - new Date(ts).getTime()) / 1000;
  if (!(s >= 0)) return "—";
  if (s < 60) return "刚刚";
  if (s < 3600) return Math.floor(s / 60) + " 分钟前";
  if (s < 86400) return Math.floor(s / 3600) + " 小时前";
  return Math.floor(s / 86400) + " 天前";
}
/** @param {string} [iso] */
export const fmtTime = (iso) => {
  if (!iso) return "";
  const d = new Date(iso), p = (/** @type {number} */ n) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
};
/** @param {number} p */
const level = (p) => (p >= 90 ? "crit" : p >= 70 ? "warn" : "");
/** @param {number} p */
export const meter = (p) => `<div class="meter"><span class="pct">${pct(p)}</span><div class="bar"><i class="${level(p)}" style="width:${Math.max(0, Math.min(100, p || 0)).toFixed(1)}%"></i></div></div>`;

// ---------- system helpers ----------
/** @param {SystemView} s */
export function osLabel(s) {
  const i = s.info || /** @type {HostInfo} */ ({});
  if (s.runner === "docker") return ["Docker", i.arch].filter(Boolean).join(" · ");
  const name = (i.platform || i.os || "").replace(/^Microsoft\s+/, "");
  return [name, i.arch].filter(Boolean).join(" · ");
}
/** @param {SystemView} s */
export function osKey(s) {
  if (s.runner === "docker") return "docker";
  const o = ((s.info && s.info.os) || "").toLowerCase();
  return o || "unknown";
}
/** @type {Record<string, string>} */
export const OS_NAMES = { windows: "Windows", darwin: "MacOS", linux: "Linux", docker: "Docker", unknown: "未知" };
/** Stable order for fleet counts and filters. */
export const OS_ORDER = ["darwin", "windows", "linux", "docker", "unknown"];
/** Short names for a guest hypervisor. A physical machine leaves virt empty. */
export const VIRT_NAMES = { vmware: "VMware", kvm: "KVM", qemu: "QEMU", hyperv: "Hyper-V", virtualbox: "VirtualBox", parallels: "Parallels", xen: "Xen", bhyve: "bhyve" };
export const VIRT_ORDER = ["vmware", "kvm", "hyperv", "virtualbox", "parallels", "xen", "bhyve", "qemu"];

/** @param {SystemView} s */
export function virtKey(s) {
  return (s.info && s.info.virt) || "";
}

/** Pill beside the hostname. Physical machines and agents that omit virt return "". */
/** @param {SystemView} s */
export function virtTag(s) {
  const k = virtKey(s);
  if (!k) return "";
  return `<span class="os-tag virt-tag">${esc(VIRT_NAMES[k] || k)}</span>`;
}
/** @param {SystemView} s */
export const ip = (s) => {
  const n = s.latest && s.latest.nics && s.latest.nics.find((x) => x.ipv4);
  return (n && n.ipv4) || s.remoteAddr || "";
};

// ---------- timers ----------
export function clearTimers() {
  state.timers.forEach(clearInterval);
  state.timers = [];
}
/** @param {number} ms @param {() => unknown} fn */
export function every(ms, fn) {
  state.timers.push(setInterval(fn, ms));
}

// ---------- api ----------
/** @typedef {Error & { code?: string }} ApiError */
/**
 * @param {string} path
 * @param {{ method?: string, headers?: Record<string, string>, json?: unknown, body?: BodyInit }} [opts]
 * @returns {Promise<any>}
 */
export async function api(path, opts = {}) {
  const headers = Object.assign({}, opts.headers || {});
  if (state.token) headers.Authorization = "Bearer " + state.token;
  if (opts.json !== undefined) {
    headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(opts.json);
  }
  const res = await fetch(API + path, { method: opts.method || "GET", headers, body: opts.body });
  if (res.status === 401) {
    showLogin();
    throw new Error("unauthorized");
  }
  if (res.status === 204) return null;
  const data = await res.json().catch(() => null);
  if (!res.ok) {
    const err = /** @type {ApiError} */ (new Error((data && (data.message || data.code)) || res.statusText));
    err.code = data && data.code;
    throw err;
  }
  return data;
}

/** @param {string} [msg] */
export function showLogin(msg) {
  clearTimers();
  $("#login").hidden = false;
  $("#loginErr").textContent = msg || "";
  setTimeout(() => $("#tokenInput").focus(), 30);
}
