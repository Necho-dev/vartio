// @ts-check
// Pages re-render through route() without importing the router itself,
// which would make every page module part of an import cycle.

/** @type {() => void} */
let handler = () => {};
/** @param {() => void} fn */
export const setRouter = (fn) => { handler = fn; };
export const route = () => handler();
