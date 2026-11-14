"use strict";

// The engine's method list against the extension's calls: every method the
// extension pages and background call is in the engine's `methods` table
// (engine/internal/engine/api.go), and the table has no method that nothing
// calls, unless it is listed here as one for other clients.

const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");

const root = path.join(__dirname, "..", "..");

function engineMethods() {
  const src = fs.readFileSync(path.join(root, "engine/internal/engine/api.go"), "utf8");
  const table = src.slice(src.indexOf("var methods = map[string]handler{"));
  return new Set([...table.matchAll(/^\t"(\w+)":/gm)].map((m) => m[1]));
}

function jsFiles(dir) {
  return fs.readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) return e.name === "dev" || e.name === "test" ? [] : jsFiles(p);
    return e.name.endsWith(".js") ? [p] : [];
  });
}

// Methods the extension calls: call("x"), act("x") and send("x") with the name as a literal.
function extensionCalls() {
  const calls = new Map();
  for (const f of jsFiles(path.join(root, "extension"))) {
    const src = fs.readFileSync(f, "utf8");
    for (const m of src.matchAll(/\b(?:call|act|send)\(\s*"(\w+)"/g)) calls.set(m[1], path.relative(root, f));
  }
  return calls;
}

// Not called by the extension: the installer and tray (ipc.Call), the Downloads
// window, and the engine's own tests.
const NOT_FOR_THE_EXTENSION = new Set(["getState", "getSettings", "getAutostart", "version", "restartCost", "quit"]);

test("every method the extension calls exists in the engine", () => {
  const have = engineMethods();
  assert.ok(have.size > 40, "found the method table");
  for (const [m, file] of extensionCalls()) assert.ok(have.has(m), `${file} calls "${m}", which the engine does not have`);
});

test("every engine method is called by the extension or listed as one for other clients", () => {
  const calls = extensionCalls();
  for (const m of engineMethods()) {
    assert.ok(calls.has(m) || NOT_FOR_THE_EXTENSION.has(m), `"${m}" is in the engine's table but nothing calls it`);
  }
});
