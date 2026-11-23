"use strict";

// The engine's requests as both of its clients here make them: the background
// page over the native port, and the Downloads window over its bridge. Pure,
// no browser.*: pages and the background load it as a script, the Node tests
// require it.

// Calls the engine answers only once the user is done: Windows' folder dialog
// (engine BrowseFolder), the administrator prompt for keepAllConnections. They
// wait as long as the user does.
const WAITS_FOR_USER = new Set(["browseFolder", "keepAllConnections"]);

// post(message) sends one request; later(fn, ms) is setTimeout.
function rpcClient(post, later) {
  let nextId = 1;
  const pending = new Map();
  return {
    pending,
    // call's promise settles with the engine's answer, or fails after a minute
    // without one (but for WAITS_FOR_USER).
    call(method, params) {
      const id = nextId++;
      return new Promise((resolve, reject) => {
        pending.set(id, { resolve, reject });
        post({ id, method, params: params || {} });
        if (WAITS_FOR_USER.has(method)) return;
        later(() => {
          if (pending.delete(id)) reject(new Error("engine did not answer"));
        }, 60000);
      });
    },
    // answer settles the call that msg answers; any other message is ignored.
    answer(msg) {
      const p = pending.get(msg.id);
      if (!p) return;
      pending.delete(msg.id);
      if (msg.error) p.reject(new Error(msg.error));
      else p.resolve(msg.result);
    },
    // drop fails every call still waiting: the connection is gone.
    drop(why) {
      for (const { reject } of pending.values()) reject(new Error(why));
      pending.clear();
    },
  };
}

if (typeof module !== "undefined") module.exports = { rpcClient };
