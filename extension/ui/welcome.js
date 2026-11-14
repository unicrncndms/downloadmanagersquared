"use strict";

// The welcome page's two live bits: the engine download button, and the switch to "Connected" once the engine says hello.

// The extension never runs the file: the browser downloads it and the user opens it.
$("get").onclick = () => Host.openUrl(ENGINE_ZIP_URL);

// While this page is open the background retries the engine within seconds (Engine.fast in background/engine.js).
Host.subscribe((m) => {
  $("engine").hidden = m.connected;
  $("ready").hidden = !m.connected;
});
