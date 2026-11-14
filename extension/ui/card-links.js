"use strict";

// A download's connections in its card: one line each, with its state,
// requests and speed. Used for the failed ones on the card and all of them in
// Details (card.js makeCard).

// One line per interface: name, state, connections, speed (and why, if it failed).
function renderLinks(id, d, grid, rows, ids) {
  const torrent = d.kind === "torrent";
  let k = 0;
  for (const ifc of ids) {
    const l = d.links[ifc];
    let r = rows.get(ifc);
    if (!r) {
      r = {
        name: el("span", { class: "who" }), state: el("span"), info: el("span", { class: "muted" }), speed: el("span"),
        whyText: el("span"),
        retry: el("button", { class: "small", onclick: () => act("retryLink", { id, iface: ifc }) }, "Retry"),
      };
      r.why = el("div", { class: "why" }, r.whyText, " ", r.retry);
      rows.set(ifc, r);
    }
    for (const n of [r.name, r.state, r.info, r.speed, r.why]) place(grid, n, k++);
    fresh(r.name, [ifc, ifaceName(ifc, latest.state, latest.settings), ifaceHue(ifc, latest.state)], () => {
      const dot = el("i", { class: "dot" });
      dot.style.color = ifaceHue(ifc, latest.state);
      return [dot, ifaceName(ifc, latest.state, latest.settings)];
    });
    fresh(r.state, [l.tone, l.label, l.resolved, l.family], () => [
      el("span", { class: "badge " + (l.tone || "dim") }, l.label),
      l.resolved ? el("span", { class: "muted", title: "This connection got its own link from the site" }, " own link") : null,
      l.family ? el("span", { class: "muted", title: "The address family this connection fetches the file over" }, " " + l.family) : null,
    ].filter(Boolean));
    r.info.textContent = !l.conns ? "" : torrent ? plural(l.conns, "peer") : plural(l.conns, "request");
    r.speed.textContent = torrent
      ? [l.speed > 0 ? "↓ " + fmtSpeed(l.speed) : "", l.up > 0 ? "↑ " + fmtSpeed(l.up) : ""].filter(Boolean).join(" ")
      : l.speed > 0 ? fmtSpeed(l.speed) : "";
    r.why.hidden = !l.error && !l.canRetry;
    r.whyText.textContent = l.error || "";
    r.retry.hidden = !l.canRetry;
  }
  while (grid.children.length > k) grid.lastChild.remove();
  for (const ifc of rows.keys()) if (!ids.includes(ifc)) rows.delete(ifc);
}
