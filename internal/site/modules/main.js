// @ts-check
import { $, API, app, state, esc, api, clearTimers, showLogin } from "./core.js";
import { closeModal, closeDrawer, setFull } from "./ui.js";
import { setRouter, route } from "./nav.js";
import { dashboard, renderTable } from "./dashboard.js";
import { detail } from "./detail.js";
import { pluginCenter, pluginPage, unmountPlugin } from "./plugins.js";
import { openForwardDrawer } from "./forwards.js";

// ---------- login ----------
$("#loginForm").addEventListener("submit", async (/** @type {SubmitEvent} */ e) => {
  e.preventDefault();
  state.token = $("#tokenInput").value.trim();
  try {
    await api("/monitor/systems");
    localStorage.setItem("probeHubToken", state.token);
    $("#login").hidden = true;
    $("#logoutBtn").hidden = !state.config.authRequired;
    route();
  } catch (err) {
    $("#loginErr").textContent = "令牌无效";
  }
});
$("#logoutBtn").addEventListener("click", () => {
  localStorage.removeItem("probeHubToken");
  state.token = "";
  showLogin();
});

// ---------- theme / clock / search ----------
const THEME_KEY = "probeHubTheme";
const THEME_CYCLE = ["system", "light", "dark"];
const themeQuery = window.matchMedia("(prefers-color-scheme: dark)");

function themeMode() {
  const saved = localStorage.getItem(THEME_KEY);
  return saved === "light" || saved === "dark" ? saved : "system";
}

/** @param {string} mode */
function applyTheme(mode) {
  const theme = mode === "system" ? (themeQuery.matches ? "dark" : "light") : mode;
  document.documentElement.dataset.theme = theme;
  document.documentElement.dataset.themeMode = mode;
  const label = mode === "system" ? "跟随系统" : mode === "light" ? "浅色" : "深色";
  $("#themeBtn").setAttribute("title", "主题：" + label + "，点击切换");
}

applyTheme(themeMode());
$("#forwardsBtn").addEventListener("click", () => openForwardDrawer(null));
$("#themeBtn").addEventListener("click", () => {
  const mode = themeMode();
  const next = THEME_CYCLE[(THEME_CYCLE.indexOf(mode) + 1) % THEME_CYCLE.length];
  if (next === "system") localStorage.removeItem(THEME_KEY);
  else localStorage.setItem(THEME_KEY, next);
  applyTheme(next);
  if (location.hash.startsWith("#/system/")) route();
});
themeQuery.addEventListener("change", () => {
  if (themeMode() !== "system") return;
  applyTheme("system");
  if (location.hash.startsWith("#/system/")) route();
});
setInterval(() => ($("#clock").textContent = new Date().toLocaleString()), 1000);
$("#search").addEventListener("input", (/** @type {any} */ e) => {
  state.q = e.target.value.trim().toLowerCase();
  if (!location.hash || location.hash === "#/") renderTable();
  else location.hash = "#/";
});
document.addEventListener("keydown", (e) => {
  const tag = document.activeElement.tagName;
  if (e.key === "/" && tag !== "INPUT" && tag !== "TEXTAREA") {
    e.preventDefault();
    $("#search").focus();
  }
  if (e.key === "Escape") {
    if (/** @type {Element} */ (e.target).closest(".xterm")) return;
    if ($(".modal-box").classList.contains("is-full")) { setFull(false); return; }
    if (!$("#drawer").hidden) closeDrawer();
    else closeModal();
  }
});

// ---------- router ----------
setRouter(() => {
  clearTimers();
  closeModal();
  closeDrawer();
  unmountPlugin();
  const m = location.hash.match(/^#\/system\/(.+)$/);
  const pl = location.hash.match(/^#\/plugins(?:\/([^?]+))?(?:\?(.*))?$/);
  if (m) detail(decodeURIComponent(m[1]));
  else if (pl && pl[1]) pluginPage(decodeURIComponent(pl[1]), new URLSearchParams(pl[2] || ""));
  else if (pl) pluginCenter();
  else dashboard();
});
window.addEventListener("hashchange", route);

(async function boot() {
  try {
    state.config = await (await fetch(API + "/monitor/config")).json();
  } catch (e) {
    app.innerHTML = `<div class="empty">无法连接 Vartio：${esc(e.message)}</div>`;
    return;
  }
  $("#logoutBtn").hidden = !state.config.authRequired;
  if (state.config.authRequired && !state.token) { showLogin(); return; }
  route();
})();
