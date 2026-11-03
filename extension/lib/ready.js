"use strict";

// When replay clicks, and what it makes of the page's response. Pure functions
// over plain facts, no browser.* and no DOM: the resolver's content script and
// background read the facts, and the Node tests require this file.

const EFFECT_WAIT = 2000; // ms a click has to show an effect
const MAX_CLICKS = 5;     // per step, before asking for help
const POPUP_GRACE = 4000; // ms a new tab on another site has to start a download

// Whether an element can be clicked now, from generic DOM state only (never
// its text): visible and not covered by another element, not disabled (also
// by aria-disabled or pointer-events: none), and a link that has somewhere to
// go. A link with no href at all is a script's button and counts as ready.
function isReady(f) {
  if (!f.visible || f.covered || f.disabled || f.ariaDisabled || f.pointerEvents === "none") return false;
  return !(f.isLink && f.href !== null && f.href !== undefined && !f.href.trim());
}

// What to do msSince ms after the clicks-th click of a step, given the effects
// seen since (navigation, newTab, download, nextStep: the next step's element
// appeared): "done" ends the step, "wait" for an effect, "retry" clicks again,
// "giveUp" asks the user.
function afterClick(effects, msSince, clicks) {
  if (effects.navigation || effects.newTab || effects.download || effects.nextStep) return "done";
  if (msSince < EFFECT_WAIT) return "wait";
  return clicks < MAX_CLICKS ? "retry" : "giveUp";
}

// A tab a click opened: "keep" one on the same site or one that started a
// download, "close" one on another site that started none in time (a pop-up
// ad), else "wait".
function popupVerdict({ openerSite, site, age, download }) {
  if (download || site === openerSite) return "keep";
  return age >= POPUP_GRACE ? "close" : "wait";
}

if (typeof module !== "undefined") module.exports = { isReady, afterClick, popupVerdict, EFFECT_WAIT, MAX_CLICKS, POPUP_GRACE };
