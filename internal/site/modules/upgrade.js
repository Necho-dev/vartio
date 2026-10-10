// @ts-check
import { api } from "./core.js";

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
export const RUNNERS = { service: "Windows 服务", tray: "后台（托盘图标）", console: "前台进程", systemd: "systemd 服务", launchd: "launchd 后台" };

/** @param {SystemView} s @param {string} latest */
export async function upgradeOne(s, latest) {
  if (!confirm(`将 ${s.name || s.hostname} 的探针从 ${s.version} 升级到 ${latest}？\n\n下载并校验签名后自动重启；新版本 2 分钟内连不上 hub 会自动回滚。`)) return null;
  return /** @type {UpgradeResult} */ (await api(`/sessions/${encodeURIComponent(s.id)}/upgrade`, { method: "POST", json: { version: latest, confirm: true } }));
}

/** @param {number} n @param {string} latest */
export async function upgradeAll(n, latest) {
  if (!confirm(`将 ${n} 台在线探针升级到 ${latest}？\n\n每台下载并校验签名后自动重启，失败的会自动回滚。`)) return null;
  return /** @type {BatchResult[]} */ (await api("/upgrade", { method: "POST", json: { version: latest, confirm: true } }));
}
