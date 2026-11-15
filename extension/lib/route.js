"use strict";

// Where a DMS² container's traffic may go. Pure, no browser.*: the resolver's
// proxy and cancel listeners both ask it, and the Node tests require it.

// The engine's SOCKS port for the container's interface, or 0 when there is
// none to use (engine not connected, interface unknown or absent): then the
// request must not leave at all.
function containerPort(iface, connected, interfaces) {
  const v = connected && (interfaces || []).find((i) => i.id === iface);
  return v && v.present && v.socksPort > 0 ? v.socksPort : 0;
}

// Which resolver job a request, download or tab belongs to, from what it carries
// ({ tabId, windowId, cookieStoreId }): its tab (or the tab that opened it,
// tabJobs), its window, then, in Firefox only, its container. { job, iface }
// (job null: a DMS² container with no resolve running), or null when it is the
// user's own. A wrong answer sends the user's traffic through a resolver's
// proxy, or the reverse.
function ownerOf({ tabJobs, jobs, ifaceByStore }, { tabId, windowId, cookieStoreId } = {}) {
  const byTab = tabId >= 0 ? tabJobs.get(tabId) : undefined;
  if (byTab) return { job: byTab, iface: byTab.iface };
  if (windowId !== undefined && windowId !== null) {
    for (const j of jobs.values()) if (j.windowId === windowId) return { job: j, iface: j.iface };
  }
  const iface = cookieStoreId ? ifaceByStore[cookieStoreId] : undefined;
  if (!iface) return null;
  let job = null;
  for (const j of jobs.values()) {
    if (j.iface === iface && !j.done && (j.mode === "replay" || j.mode === "record")) job = j;
  }
  return { job, iface };
}

// What a new job does: "endLearn" (a learn job where a recipe already is),
// "replay" (a recipe is there), "waiting" (another job is recording this site's
// recipe first, or the site already has MAX_WINDOWS resolver windows open) or
// "record" (first visit to the site).
const MAX_WINDOWS = 2;
function startChoice(job, hasRecipe, jobs) {
  if (hasRecipe && job.learn) return "endLearn";
  const open = [...jobs].filter((j) => j !== job && !j.done && j.host === job.host && (j.mode === "replay" || j.mode === "record")).length;
  if (open >= MAX_WINDOWS) return "waiting";
  if (hasRecipe) return "replay";
  const recording = [...jobs].some((j) => j !== job && j.host === job.host && j.mode === "record");
  return recording ? "waiting" : "record";
}

// The containers to remove at startup: the stored ones not kept by a job that
// carries on, and any DMS² one the browser lists (named with prefix) that isn't kept.
function staleContainers(stored, kept, listed, prefix) {
  const keepSet = new Set(kept);
  const stale = new Set(Object.values(stored || {}).filter((c) => !keepSet.has(c)));
  for (const c of listed || []) if (c.name.startsWith(prefix) && !keepSet.has(c.cookieStoreId)) stale.add(c.cookieStoreId);
  return [...stale];
}

if (typeof module !== "undefined") module.exports = { containerPort, ownerOf, startChoice, staleContainers };
