// @ts-check
import { $, esc } from "../core.js";

// ----- confirm dialog -----
// ask() resolves to the input value (or true without input), null on cancel.
// word: the user must type it before the confirm button unlocks.
// toggle: label of a checkbox; title/body/ok/danger/word/wordLabel may then be
// functions of its state, and ask() resolves to { on } instead of true.
let dlg = null;
export function ask(opts) {
  const { input = null, check = null, toggle = "" } = opts;
  if (dlg) dlg.cancel();
  return new Promise((resolve) => {
    let on = false;
    const val = (k, d) => { const v = opts[k] === undefined ? d : opts[k]; return typeof v === "function" ? v(on) : v; };
    const mask = document.createElement("div");
    mask.className = "fm-dlg-mask";
    mask.innerHTML = `<div class="fm-dlg" role="dialog">
      <h4></h4><div class="fm-dlg-body"></div>
      ${input ? `<input class="fm-dlg-in" spellcheck="false" value="${esc(input.value || "")}">` : ""}
      ${input && check ? `<div class="fm-dlg-hint"></div>` : ""}
      ${toggle ? `<label class="fm-dlg-toggle"><input type="checkbox" class="fm-dlg-t"> ${esc(toggle)}</label>` : ""}
      <label class="fm-dlg-word" hidden><span></span><input class="fm-dlg-w" spellcheck="false" autocomplete="off"></label>
      <div class="fm-dlg-err"></div>
      <div class="fm-dlg-btns"><button class="btn" data-a="no">取消</button><button class="btn" data-a="yes"></button></div></div>`;
    $(".modal-box").appendChild(mask);
    const box = $(".fm-dlg", mask), yes = $('[data-a="yes"]', mask);
    const inp = $(".fm-dlg-in", mask), w = $(".fm-dlg-w", mask), wl = $(".fm-dlg-word", mask);
    const hint = $(".fm-dlg-hint", mask), tg = $(".fm-dlg-t", mask);
    let word = "";
    const paint = () => {
      const danger = val("danger", false);
      box.classList.toggle("danger", !!danger);
      box.querySelector("h4").textContent = val("title", "");
      box.querySelector(".fm-dlg-body").innerHTML = val("body", "");
      yes.textContent = val("ok", "确定");
      yes.className = `btn ${danger ? "danger-solid" : "primary"}`;
      const nw = val("word", "");
      if (nw !== word) w.value = "";
      word = nw;
      wl.hidden = !word;
      wl.querySelector("span").innerHTML = `${val("wordLabel", "") || "请输入"} <b>${esc(word)}</b> 以确认`;
    };
    const sync = () => {
      let okay = true;
      if (word) okay = w.value.trim() === word;
      if (inp) {
        const v = inp.value.trim(), msg = check ? check(v) : "";
        if (hint) hint.innerHTML = msg;
        okay = okay && v && !(check && hint && hint.querySelector(".bad"));
      }
      yes.disabled = !okay;
    };
    const done = (v) => {
      mask.remove();
      dlg = null;
      resolve(v);
    };
    dlg = {
      cancel: () => done(null),
      submit: () => { if (!yes.disabled) done(inp ? inp.value.trim() : toggle ? { on } : true); },
    };
    mask.addEventListener("click", (/** @type {any} */ e) => {
      const a = e.target.closest("[data-a]");
      if (a) a.dataset.a === "yes" ? dlg.submit() : dlg.cancel();
      else if (e.target === mask) dlg.cancel();
    });
    [inp, w].forEach((el) => el && el.addEventListener("input", sync));
    if (tg) tg.addEventListener("change", () => { on = tg.checked; paint(); sync(); if (word) w.focus(); });
    paint();
    sync();
    const focus = word ? w : inp || yes;
    setTimeout(() => {
      focus.focus();
      if (inp && input.select) {
        const dot = input.value.lastIndexOf(".");
        inp.setSelectionRange(0, input.select === "stem" && dot > 0 ? dot : input.value.length);
      }
    }, 20);
  });
}

/** The open dialog, if any; key handlers route Esc/Enter to it. */
export const activeDialog = () => dlg;
