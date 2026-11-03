"use strict";

// Recipes: the clicks recorded on a landing-page host, each with the wait
// before it (from the page loading or the previous click), plus the wait from
// the last click until the download. Plain, serializable data:
//   { steps: [{ tag, id, text, selector, wait }], downloadWait, learned }
// Pure functions, no browser.*: the extension loads this as a background
// script and the Node tests require it.

const BUDGET_SLACK = 15000; // ms added to twice a recorded wait
const REPLAY_SLACK = 60000; // ms added to the sum of a replay's budgets

// The steps after a recording: the first recordFrom steps of the old recipe
// (those replayed before the user had to take over), then the recorded ones.
function mergeSteps(oldSteps, recordFrom, recorded) {
  return (oldSteps || []).slice(0, recordFrom).concat(recorded);
}

// The recipe a recording leaves: recorded steps (with their waits) after the
// old ones kept, and the wait it saw from the last click to the download.
function recorded(old, recordFrom, steps, downloadWait, now) {
  return { steps: mergeSteps(old && old.steps, recordFrom, steps), downloadWait, learned: now };
}

// How long a step may take at most: twice its recorded wait plus 15 s. A
// ceiling, never a minimum: replay clicks as soon as the page is ready. A
// recipe from before waits were recorded counts 0.
function budget(wait) {
  return (wait || 0) * 2 + BUDGET_SLACK;
}

// How long a whole replay may take: every step's budget, the download's, and a minute.
function replayTimeout(recipe) {
  const steps = (recipe && recipe.steps) || [];
  return steps.reduce((sum, s) => sum + budget(s.wait), 0) + budget(recipe && recipe.downloadWait) + REPLAY_SLACK;
}

// The recipe after a successful replay: the waits measured this time replace
// the recorded ones (measured.steps[i] undefined: that step was skipped).
function withMeasuredWaits(recipe, measured) {
  return {
    ...recipe,
    steps: recipe.steps.map((s, i) => (typeof measured.steps[i] === "number" ? { ...s, wait: measured.steps[i] } : s)),
    downloadWait: typeof measured.download === "number" ? measured.download : recipe.downloadWait,
  };
}

const RECORD_IDLE = 5 * 60000; // ms without a click, page load or captcha change

// Whether a recording has been left alone long enough to give up on it.
function recordingIdle(lastActivity, now) {
  return now - lastActivity >= RECORD_IDLE;
}

if (typeof module !== "undefined") {
  module.exports = { mergeSteps, recorded, budget, replayTimeout, withMeasuredWaits, recordingIdle, RECORD_IDLE };
}
