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
// Below its button (or at the pointer, for a right-click: at is {x, y}),
// pushed up and left as far as needed to stay inside the window.
function showMenu(menu, button, at) {
  menu.hidden = false;
  openMenu = menu;
  const b = at ? { bottom: at.y - 4, right: at.x + menu.offsetWidth } : button.getBoundingClientRect();
  menu.style.top = Math.max(4, Math.min(b.bottom + 4, innerHeight - menu.offsetHeight - 4)) + "px";
  menu.style.left = Math.max(4, Math.min(b.right - menu.offsetWidth, innerWidth - menu.offsetWidth - 4)) + "px";
}
// A right-click on a card opens its "⋯" menu at the pointer; fields and links keep the browser's own.
document.addEventListener("contextmenu", (e) => {
  if (e.target.closest("input, textarea, select, a, .menu")) return;
  const card = e.target.closest(".card");
  const menu = card && (card.querySelector(".menu") || (card.buildMenu && card.buildMenu()));
  if (!menu) return;
  e.preventDefault();
  closeMenu();
  showMenu(menu, null, { x: e.clientX, y: e.clientY });
});
// A fixed menu would stay put while its row scrolls away.
document.addEventListener("scroll", () => closeMenu(), true);
// Escape closes it too, and gives the focus back to its "⋯" button.
document.addEventListener("keydown", (e) => {
  if (e.key !== "Escape" || !openMenu) return;
  const more = openMenu.parentElement.querySelector("button.icon");
  closeMenu();
  if (more) more.focus();
});

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
  check: "M3.5 8.5l3 3 6-7",
  alert: "M8 3v6.5M8 12.5h.01",
  link: "M7 9a2.5 2.5 0 0 0 3.5 0l2-2a2.5 2.5 0 0 0-3.5-3.5l-.5.5M9 7a2.5 2.5 0 0 0-3.5 0l-2 2a2.5 2.5 0 0 0 3.5 3.5l.5-.5",
  box: "M2.5 5L8 2.5 13.5 5v6L8 13.5 2.5 11zM2.5 5L8 7.5 13.5 5M8 7.5v6",
  copy: "M5.5 5.5h7.5v8H5.5zM3 10.5V2.5h7.5",
  open: "M9 3h4v4M13 3L7.5 8.5M11 9.5V13H3V5h3.5",
  grip: "M6 4h.01M10 4h.01M6 8h.01M10 8h.01M6 12h.01M10 12h.01",
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

// menuButton is the "⋯" button of a menu: it opens it, or closes it when it is
// open. getMenu gives the menu (a line builds its menu when first used).
function menuButton(getMenu) {
  return iconButton("more", "More", (e) => {
    e.stopPropagation();
    const m = getMenu();
    const was = !m.hidden;
    closeMenu();
    if (!was) showMenu(m, e.currentTarget);
  });
}

// menuItem is an entry of a menu: it closes the menu, then does go.
function menuItem(label, go) {
  return el("button", { onclick: () => { closeMenu(); go(); } }, label);
}

// fileItems are the menu's Show in folder and Copy link for download id;
// getUrl gives its link as last shown.
function fileItems(id, getUrl) {
  return {
    folder: menuItem("Show in folder", () => act("showInFolder", { id })),
    copy: menuItem("Copy link", () => copyLink(getUrl())),
  };
}

// confirmRow is the question a card asks inline (a popup can't show confirm
// dialogs) before something that can't be undone: the question, the buttons
// that answer it, and a Cancel. Hidden until asked; ask.text is the question
// text node's element, for a question that changes.
function confirmRow(question, ...buttons) {
  const text = el("span", { class: "muted" }, question);
  const ask = el("div", { class: "actions ask", hidden: true }, text, ...buttons,
    el("button", { class: "small", onclick: () => { ask.hidden = true; } }, "Cancel"));
  ask.text = text;
  return ask;
}

// editable switches a value (facts) to its form and back: the function it
// returns takes true for the form (after fill puts the current values in the
// boxes) and false for the values. editBtn is hidden while the form shows.
function editable(facts, form, editBtn, fill) {
  return (on) => {
    if (on) fill();
    form.hidden = !on;
    facts.hidden = on;
    editBtn.hidden = on;
  };
}

// fresh rebuilds node's children only when what they show (sig) changed:
// rebuilt every second, text in them couldn't be selected to copy.
const shownSig = new WeakMap();
function fresh(node, sig, build) {
  const s = JSON.stringify(sig);
  if (shownSig.get(node) === s) return false;
  shownSig.set(node, s);
  node.replaceChildren(...build());
  return true; // it changed
}
