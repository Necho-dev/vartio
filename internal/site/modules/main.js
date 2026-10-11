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
const theme = localStorage.getItem("probeHubTheme") || "dark";
document.documentElement.dataset.theme = theme;
$("#forwardsBtn").addEventListener("click", () => openForwardDrawer(null));
$("#themeBtn").addEventListener("click", () => {
  const t = document.documentElement.dataset.theme === "dark" ? "light" : "dark";
  document.documentElement.dataset.theme = t;
  localStorage.setItem("probeHubTheme", t);
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
