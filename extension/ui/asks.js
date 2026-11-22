"use strict";

// The questions the engine holds a download back for (state.asks: a file of that name is
// already there, or the link is already in the list): shown above the list, a button per answer.

function renderAsks(m, onShow) {
  const box = $("asks");
  const asks = (m.connected && m.state && m.state.asks) || [];
  box.hidden = !asks.length;
  const sig = JSON.stringify(asks);
  if (box.dataset.sig === sig) return;
  box.dataset.sig = sig;
  box.replaceChildren(...asks.map((a) => el("div", { class: "ask" }, el("span", {}, a.text), " ",
    ...a.choices.map((c, i) => el("button", { type: "button", class: "small" + (i === 0 ? " primary" : ""), onclick: () => answerAsk(a, c.key, onShow) }, c.label)))));
}

// "show" opens the download the question is about; the engine drops the held link for it as for any answer that adds nothing.
function answerAsk(a, choice, onShow) {
  if (choice === "show" && a.existing) onShow(a.existing);
  call("answerAsk", { id: a.id, choice }).catch((e) => toast(e.message, true));
}
