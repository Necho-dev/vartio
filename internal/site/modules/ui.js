// @ts-check
import { $ } from "./core.js";

// ---------- modal ----------
/** @type {(() => void) | null} */
let onModalClose = null;
/**
 * @param {string} title
 * @param {string} html
 * @param {(() => void) | null} [onClose]
 * @param {{ wide?: boolean, fullscreen?: boolean }} [opts]
 */
export function openModal(title, html, onClose, opts = {}) {
  document.querySelectorAll(".tip").forEach((t) => { /** @type {HTMLElement} */ (t).hidden = true; });
  $("#modalTitle").textContent = title;
  $("#modalBody").innerHTML = html;
  $("#modal").classList.toggle("wide", !!opts.wide);
  $("#modalFull").hidden = !opts.fullscreen;
  $("#modal").hidden = false;
  onModalClose = onClose || null;
}
export function closeModal() {
  if ($("#modal").hidden) return;
  setFull(false);
  $("#modal").hidden = true;
  $("#modal").classList.remove("wide");
  $("#modalBody").innerHTML = "";
  if (onModalClose) onModalClose();
  onModalClose = null;
}
// "Full" means filling the page, not the physical screen.
/** @param {boolean} on */
export function setFull(on) { $(".modal-box").classList.toggle("is-full", on); }
export function toggleFullscreen() { setFull(!$(".modal-box").classList.contains("is-full")); }
$("#modalClose").addEventListener("click", closeModal);
$("#modalFull").addEventListener("click", toggleFullscreen);
$("#modal").addEventListener("click", (/** @type {MouseEvent} */ e) => { if (/** @type {Element} */ (e.target).id === "modal") closeModal(); });
$("#modalTitle").addEventListener("dblclick", () => { if (!$("#modalFull").hidden) toggleFullscreen(); });

// ---------- drawer ----------
/** @type {(() => void) | null} */
let onDrawerClose = null;
/**
 * @param {string} title
 * @param {string} sub
 * @param {string} html
 * @param {(() => void) | null} [onClose]
 */
export function openDrawer(title, sub, html, onClose) {
  document.querySelectorAll(".tip").forEach((t) => { /** @type {HTMLElement} */ (t).hidden = true; });
  closeDrawer();
  $("#drawerTitle").textContent = title;
  $("#drawerSub").textContent = sub || "";
  $("#drawerBody").innerHTML = html;
  $("#drawer").hidden = false;
  onDrawerClose = onClose || null;
}
export function closeDrawer() {
  if ($("#drawer").hidden) return;
  $("#drawer").hidden = true;
  $("#drawerBody").innerHTML = "";
  if (onDrawerClose) onDrawerClose();
  onDrawerClose = null;
}
$("#drawerClose").addEventListener("click", closeDrawer);
$("#drawerMask").addEventListener("click", closeDrawer);
