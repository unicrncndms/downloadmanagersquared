"use strict";

// A torrent's add step on its card (card.js makeCard): the VPN question, the
// files to take and the folder, shown while the torrent waits for them
// (status "choosing").

// pickStep is the step's box for download id: root goes on the card, and
// update(d, choosing) shows it, drawing the step again only when what it asks
// has changed.
function pickStep(id) {
  const root = el("div", { class: "pick", hidden: true });
  let picking = ""; // what the add step shows: the VPN question and/or the files

  // The state carries only how many files a torrent has, so a big one stays
  // within what Firefox passes on: the list is asked for here.
  async function renderPick(d) {
    let files = [];
    if (d.fileCount) {
      const step = picking;
      try { files = await call("torrentFiles", { id }); } catch (e) { return toast(e.message, true); }
      if (picking !== step) return; // a newer render took over meanwhile
    }
    // Before the torrent first meets peers: suggest a VPN, or stay on this connection.
    const vpnChoice = d.askVpn ? el("div", { class: "vpnask" },
      el("div", {}, el("b", {}, "Join the swarm through a VPN?"), " Every peer sees the address you join from."),
      el("label", {}, el("input", { type: "radio", name: "vpn-" + id, value: "vpn", checked: hasVpn(), disabled: !hasVpn() }),
        hasVpn() ? " VPN only (recommended)" : " VPN only: none is set up yet ",
        hasVpn() ? null : el("a", { href: "#", onclick: (e) => { e.preventDefault(); Host.openSettings("#pia"); } }, "Set one up")),
      el("label", {}, el("input", { type: "radio", name: "vpn-" + id, value: "off", checked: !hasVpn() }), " Off (stay on the current connection)")) : ""; // not null: replaceChildren would print "null"
    const vpnValue = () => (root.querySelector(`input[name="vpn-${id}"]:checked`) || {}).value;
    if (!files.length) { // a magnet link: the question comes before fetching the files
      root.replaceChildren(vpnChoice, el("div", { class: "actions" },
        el("button", { class: "small primary", onclick: () => act("chooseVpn", { id, vpn: vpnValue() }) }, "Continue"),
        el("button", { class: "small", onclick: () => act("remove", { id, deleteFile: false }, "Cancelled") }, "Cancel")));
      return;
    }
    const boxes = files.map((f) => el("input", { type: "checkbox", checked: f.selected }));
    const dir = folderControl(d.dir);
    const total = el("span", { class: "muted num" });
    const sizes = files.map((f) => f.size);
    const count = () => { total.textContent = pickTotal(sizes, boxes.map((b) => b.checked)); };
    boxes.forEach((b) => b.addEventListener("change", count));
    count();
    const row = (i) => el("label", {}, boxes[i], el("span", { class: "path" }, files[i].path), el("span", { class: "muted num" }, fmtBytes(sizes[i])));
    const tick = (idx, on) => idx.forEach((i) => { boxes[i].checked = on; boxes[i].dispatchEvent(new Event("change")); }); // each box tells its folder and the total
    const groups = fileFolders(files.map((f) => f.path));
    const list = groups.length === 1 ? groups[0].items.map(row) : groups.map((g) => {
      if (!g.name) return g.items.map(row);
      const all = el("input", { type: "checkbox", checked: g.items.every((i) => boxes[i].checked), "aria-label": "Every file in " + g.name,
        onclick: (e) => { e.stopPropagation(); tick(g.items, e.target.checked); } });
      // The folder's own box follows its files.
      g.items.forEach((i) => boxes[i].addEventListener("change", () => { all.checked = g.items.every((j) => boxes[j].checked); }));
      return el("details", {}, el("summary", {}, all, el("span", { class: "path" }, g.name + "/"), el("span", { class: "muted num" }, plural(g.items.length, "file"))),
        el("div", {}, g.items.map(row)));
    });
    root.replaceChildren(vpnChoice,
      files.length > 1 ? el("div", { class: "bulk" },
        el("button", { class: "small", onclick: () => tick(files.map((_, i) => i), true) }, "Select all"),
        el("button", { class: "small", onclick: () => tick(files.map((_, i) => i), false) }, "Select none"), total) : "",
      el("div", { class: "files" }, list),
      dir.root,
      files.length > 1 ? el("div", { class: "muted" }, "In a folder named after the torrent") : "",
      el("div", { class: "actions" },
        el("button", { class: "small primary", onclick: () => act("chooseFiles", { id, files: boxes.map((b) => b.checked), dir: dir.get() || latest.settings.saveDir, ...(d.askVpn ? { vpn: vpnValue() } : {}) }, "Download queued") }, "Download"),
        el("button", { class: "small", onclick: () => act("remove", { id, deleteFile: false }, "Cancelled") }, "Cancel")));
  }

  function update(d, choosing) {
    root.hidden = !choosing;
    const step = choosing ? `${d.askVpn}|${d.fileCount || 0}|${hasVpn()}` : "";
    const newStep = step && step !== picking;
    picking = step;
    if (newStep) renderPick(d);
  }

  return { root, update };
}
