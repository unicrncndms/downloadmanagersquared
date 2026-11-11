"use strict";

// What the downloads view builds its cards from: the "⋯" menu, names that
// work as buttons, icons, and rebuilding only what changed.

// An open "⋯" menu closes on a click anywhere else.
let openMenu = null;
document.addEventListener("click", (e) => {
  if (openMenu && !openMenu.parentElement.contains(e.target)) closeMenu();
});
function closeMenu() {
  if (openMenu) openMenu.hidden = true;
  openMenu = null;
}
// Below its button, pushed up and left as far as needed to stay inside the window.
function showMenu(menu, button) {
  menu.hidden = false;
  openMenu = menu;
  const b = button.getBoundingClientRect();
  menu.style.top = Math.max(4, Math.min(b.bottom + 4, innerHeight - menu.offsetHeight - 4)) + "px";
  menu.style.left = Math.max(4, Math.min(b.right - menu.offsetWidth, innerWidth - menu.offsetWidth - 4)) + "px";
}
// A fixed menu would stay put while its row scrolls away.
document.addEventListener("scroll", () => closeMenu(), true);
// Escape closes it too, and gives the focus back to its "⋯" button.
document.addEventListener("keydown", (e) => {
  if (e.key !== "Escape" || !openMenu) return;
  const more = openMenu.parentElement.querySelector("button.icon");
  closeMenu();
  if (more) more.focus();
});

// pressable lets a name that opens and closes something work by keyboard:
// focusable, announced as a button, Enter and Space click it. on false
// makes it plain text again.
function pressable(node, on, expanded) {
  if (!on) {
    for (const a of ["tabindex", "role", "aria-expanded"]) node.removeAttribute(a);
    return;
  }
  node.setAttribute("tabindex", "0");
  node.setAttribute("role", "button");
  node.setAttribute("aria-expanded", String(!!expanded));
  if (node.dataset.pressable) return;
  node.dataset.pressable = "1";
  node.addEventListener("keydown", (e) => {
    if (node.getAttribute("role") !== "button" || (e.key !== "Enter" && e.key !== " ")) return;
    e.preventDefault();
    node.click();
  });
}

// Small line icons, drawn in the text colour.
const ICONS = {
  pause: "M5 3v10M11 3v10",
  play: "M5 3l8 5-8 5z",
  more: "M3 8h.01M8 8h.01M13 8h.01",
  folder: "M2 4.5h4l1.5 1.5H14v6.5H2z",
  close: "M4 4l8 8M12 4l-8 8",
  up: "M8 13V3M4 7l4-4 4 4",
  down: "M8 3v10M4 9l4 4 4-4",
  chevron: "M4 6l4 4 4-4",
  gear: "M8 4a4 4 0 1 0 0 8a4 4 0 1 0 0-8zM8 6.9a1.1 1.1 0 1 0 0 2.2a1.1 1.1 0 1 0 0-2.2zM8 1.7V4M8 12v2.3M1.7 8H4M12 8h2.3M3.5 3.5l1.7 1.7M10.8 10.8l1.7 1.7M3.5 12.5l1.7-1.7M10.8 5.2l1.7-1.7",
};
function icon(name) {
  const ns = "http://www.w3.org/2000/svg";
  const svg = document.createElementNS(ns, "svg");
  svg.setAttribute("viewBox", "0 0 16 16");
  svg.setAttribute("aria-hidden", "true");
  const p = document.createElementNS(ns, "path");
  p.setAttribute("d", ICONS[name]);
  svg.append(p);
  return svg;
}
function iconButton(name, title, onclick) {
  return el("button", { class: "icon", title, "aria-label": title, onclick }, icon(name));
}

// fresh rebuilds node's children only when what they show (sig) changed:
// rebuilt every second, text in them couldn't be selected to copy.
const shownSig = new WeakMap();
function fresh(node, sig, build) {
  const s = JSON.stringify(sig);
  if (shownSig.get(node) === s) return;
  shownSig.set(node, s);
  node.replaceChildren(...build());
}
