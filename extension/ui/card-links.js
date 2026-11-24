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
        name: el("span", { class: "who" }), state: el("span"), info: el("span", { class: "muted peers" }), speed: el("span", { class: "rate" }),
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
    r.info.textContent = !l.conns ? "" : torrent ? [plural(l.conns, "peer"), ...lossLines(l)].join("\n") : plural(l.conns, "request");
    r.info.title = torrent ? lossTitle(l) : "";
    r.speed.textContent = torrent
      ? [l.speed > 0 ? "↓ " + fmtSpeed(l.speed) : "", l.up > 0 ? "↑ " + fmtSpeed(l.up) : ""].filter(Boolean).join("\n")
      : l.speed > 0 ? fmtSpeed(l.speed) : "";
    r.why.hidden = !l.error && !l.canRetry;
    r.whyText.textContent = l.error || "";
    r.retry.hidden = !l.canRetry;
  }
  while (grid.children.length > k) grid.lastChild.remove();
  for (const ifc of rows.keys()) if (!ids.includes(ifc)) rows.delete(ifc);
}

// What a torrent line's peers sent for nothing, as shares of all they sent, a
// line each under its peers so the row stays narrow: "3% wasted", thrown away
// inside the line, and "2% twice", pieces another line fetched too.
function lossLines(l) {
  if (!(l.read > 0)) return [];
  const pct = (n) => {
    const p = Math.round((n / l.read) * 100);
    return p < 1 ? "<1" : String(p);
  };
  return [l.wasted > 0 ? pct(l.wasted) + "% wasted" : "", l.twice > 0 ? pct(l.twice) + "% twice" : ""].filter(Boolean);
}

// The bytes behind lossLines, and what each means.
function lossTitle(l) {
  if (!(l.read > 0) || !(l.wasted > 0 || l.twice > 0)) return "";
  return [
    `This line's peers sent ${fmtBytes(l.read)}.`,
    l.wasted > 0 ? `${fmtBytes(l.wasted)} wasted: blocks the line threw away, as another of its peers had sent them first (a request moved to a faster peer).` : "",
    l.twice > 0 ? `${fmtBytes(l.twice)} twice: blocks of pieces another line fetched as well, used but downloaded twice.` : "",
  ].filter(Boolean).join("\n");
}
