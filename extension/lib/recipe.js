"use strict";

// Recipes: the clicks recorded on a landing-page host. Pure functions over
// plain, serializable data, no browser.*: the extension loads this as a
// background script and the Node tests require it.

// The steps after a recording: the first recordFrom steps of the old recipe
// (those replayed before the user had to take over), then the recorded ones.
function mergeSteps(oldSteps, recordFrom, recorded) {
  return (oldSteps || []).slice(0, recordFrom).concat(recorded);
}

if (typeof module !== "undefined") module.exports = { mergeSteps };
