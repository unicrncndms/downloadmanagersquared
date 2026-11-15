"use strict";

// Joins the chunk messages dms-host cuts a long engine line into (a state over
// 1 MiB), so the pages see the whole message. Pure: the background loads it as
// a script, the Node tests require it.

// chunkJoiner(max) gives add(msg): the parsed line once its last chunk is in,
// else null. At most max lines are kept half joined (the oldest is dropped), so
// a chunk that never comes costs a bounded amount.
function chunkJoiner(max = 4) {
  const open = new Map(); // id -> { of, parts, got }
  return function add(msg) {
    let p = open.get(msg.id);
    if (!p) {
      if (!(msg.of > 0) || typeof msg.data !== "string") return null;
      p = { of: msg.of, parts: new Array(msg.of), got: 0 };
      open.set(msg.id, p);
      if (open.size > max) open.delete(open.keys().next().value);
    }
    if (!(msg.chunk >= 0 && msg.chunk < p.of) || msg.of !== p.of || p.parts[msg.chunk] !== undefined) return null;
    p.parts[msg.chunk] = msg.data;
    if (++p.got < p.of) return null;
    open.delete(msg.id);
    try { return JSON.parse(p.parts.join("")); } catch (e) { return null; }
  };
}

if (typeof module !== "undefined") module.exports = { chunkJoiner };
