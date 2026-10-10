// @ts-check
import { esc } from "../core.js";
import { icon } from "../icons.js";

const IMAGE_EXT = /\.(png|jpe?g|gif|webp|bmp|ico|svg|avif|heic|tiff?|psd)$/i;
const ARCHIVE_EXT = /\.(zip|7z|rar|gz|tgz|bz2|xz|zst|tar|jar|apk|dmg|pkg|deb|rpm|iso|msi)$/i;
const TEXT_EXT = /\.(txt|log|md|json|ya?ml|toml|ini|conf|cfg|env|csv|tsv|xml|html?|css|js|ts|go|py|sh|ps1|bat|cmd|sql|c|h|cpp|java|rs|rb|php)$/i;

/** @param {{ name: string, isDir?: boolean }} it */
export const fileIcon = (it) => {
  if (it.isDir) return icon("folder", "fi dir");
  if (IMAGE_EXT.test(it.name)) return icon("file-image", "fi file img");
  if (ARCHIVE_EXT.test(it.name)) return icon("file-archive", "fi file zip");
  return icon(TEXT_EXT.test(it.name) ? "file-text" : "file", "fi file");
};

/** @param {{ name: string, isDir?: boolean }[]} items */
export const listHTML = (items) => {
  const shown = items.slice(0, 8).map((it) => `<li>${fileIcon(it)}<span>${esc(it.name)}</span></li>`).join("");
  return `<ul class="fm-dlg-list">${shown}${items.length > 8 ? `<li class="more">…以及另外 ${items.length - 8} 项</li>` : ""}</ul>`;
};

/**
 * The overlay covering the file list (preview or recycle bin); at most one.
 * @type {{ cur: { close: () => void, selectAll: () => void } | null }}
 */
export const view = { cur: null };

/**
 * What the preview, recycle bin and transfer modules need from the open file manager.
 * @typedef {{
 *   bios: string,
 *   st: FilesState,
 *   info: (msg: string) => void,
 *   join: (dir: string, name: string) => string,
 *   reloadSoon: () => void,
 *   download: (list: FileEntry[]) => void,
 * }} FilesCtx
 */

/**
 * @typedef {{
 *   id?: number, dir: "up" | "down", name: string, total: number, done?: number,
 *   state?: "wait" | "run" | "ok" | "err" | "cancel" | "exists", phase?: string, err?: string, t0?: number,
 *   abort?: (() => void) | null, file?: File, dest?: string, src?: string, isDir?: boolean,
 *   reload?: boolean, overwrite?: boolean,
 * }} Xfer
 */

/**
 * @typedef {{
 *   cwd: string, sep: string, parent: string, home: string, roots: string[], entries: FileEntry[],
 *   sel: Set<string>, anchor: string | null, back: string[], fwd: string[],
 *   sort: { key: string, dir: number }, q: string, hidden: boolean, trash?: boolean,
 *   xfers: Xfer[], seq: number, closed: boolean, loading: boolean,
 * }} FilesState
 */
