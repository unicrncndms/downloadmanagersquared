"use strict";

const test = require("node:test");
const assert = require("node:assert");
const { isReady, afterClick, popupVerdict, EFFECT_WAIT, MAX_CLICKS, POPUP_GRACE } = require("../lib/ready.js");

const ready = { visible: true, covered: false, disabled: false, ariaDisabled: false, pointerEvents: "auto", isLink: false, href: null };

test("a visible, uncovered, enabled element is ready", () => {
  assert.equal(isReady(ready), true);
  assert.equal(isReady({ ...ready, isLink: true, href: "/get" }), true);
});

test("each readiness rule holds a click back", () => {
  assert.equal(isReady({ ...ready, visible: false }), false);
  assert.equal(isReady({ ...ready, covered: true }), false);
  assert.equal(isReady({ ...ready, disabled: true }), false);
  assert.equal(isReady({ ...ready, ariaDisabled: true }), false);
  assert.equal(isReady({ ...ready, pointerEvents: "none" }), false);
  assert.equal(isReady({ ...ready, isLink: true, href: "" }), false);
  assert.equal(isReady({ ...ready, isLink: true, href: "   " }), false);
});

test("a link without any href is a script's button: ready", () => {
  assert.equal(isReady({ ...ready, isLink: true, href: null }), true);
});

test("an effect within the wait ends the step", () => {
  for (const effect of ["navigation", "newTab", "download", "nextStep"]) {
    assert.equal(afterClick({ [effect]: true }, 500, 1), "done", effect);
  }
});

test("no effect yet: wait, then click again, then give up", () => {
  assert.equal(afterClick({}, EFFECT_WAIT - 1, 1), "wait");
  assert.equal(afterClick({}, EFFECT_WAIT, 1), "retry");
  assert.equal(afterClick({}, EFFECT_WAIT, MAX_CLICKS - 1), "retry");
  assert.equal(afterClick({}, EFFECT_WAIT, MAX_CLICKS), "giveUp");
});

test("a new tab on another site is closed unless it starts a download", () => {
  const t = { openerSite: "host.com", site: "ads.net", age: POPUP_GRACE, download: false };
  assert.equal(popupVerdict(t), "close");
  assert.equal(popupVerdict({ ...t, age: POPUP_GRACE - 1 }), "wait");
  assert.equal(popupVerdict({ ...t, download: true }), "keep");
  assert.equal(popupVerdict({ ...t, site: "host.com" }), "keep");
});
