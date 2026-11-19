"use strict";

// Injected into resolver tabs only. Replays a remembered click, or records the
// user's clicks so they can be replayed next time. Watches for captchas, which
// only a person can solve, and holds everything until one is done.
(() => {
  if (window.__dmsResolver) return;
  window.__dmsResolver = true;

  const CLICKABLE = "a,button,input[type=submit],input[type=button],input[type=image],[role=button],[onclick]";
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
  // Widgets of the common captcha services, plus generic captcha markers.
  const CAPTCHA = [
    "iframe[src*='recaptcha']", "iframe[src*='hcaptcha']", "iframe[src*='challenges.cloudflare.com']",
    "iframe[src*='turnstile']", "iframe[src*='captcha' i]", ".g-recaptcha", ".h-captcha", ".cf-turnstile",
    "[data-sitekey]", "#cf-challenge-running", "img[src*='captcha' i]", "img[alt*='captcha' i]",
    "input[name*='captcha' i]", "[id*='captcha' i]", "[class*='captcha' i]",
  ].join(",");
  // Fields that hold the answer: filled in by the services once solved, or typed
  // by you for a "type the characters" captcha.
  const TOKENS = "[name='g-recaptcha-response'],[name='h-captcha-response'],[name='cf-turnstile-response'],input[name*='captcha' i]:not([type=hidden])";
  // Invisible reCAPTCHA's corner badge: it only gets an answer when you press the
  // site's own button, so it never needs (or gets) your attention.
  const BACKGROUND_CAPTCHA = ".grecaptcha-badge, iframe[src*='size=invisible']";
  // Many captchas pass by themselves, some only after several seconds (a
  // Turnstile in a slow VM); only ask for help when one is still there after this long.
  const CAPTCHA_GRACE = 12000;
  let recording = false;
  let captcha = false;
  let pendingSince = 0;

  const norm = (s) => (s || "").replace(/\s+/g, " ").trim().slice(0, 80);
  const textOf = (el) => norm(el.innerText || el.value || el.getAttribute("aria-label") || el.title || "");

  function visible(el) {
    if (!el || !el.isConnected) return false;
    const r = el.getBoundingClientRect();
    if (r.width === 0 && r.height === 0) return false;
    const cs = getComputedStyle(el);
    return cs.visibility !== "hidden" && cs.display !== "none";
  }

  // What lib/ready.js needs to know about an element, read from the DOM.
  function factsOf(el) {
    const r = el.getBoundingClientRect();
    const top = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
    return {
      visible: visible(el),
      covered: !!top && top !== el && !el.contains(top),
      disabled: !!el.disabled,
      ariaDisabled: el.getAttribute("aria-disabled") === "true",
      pointerEvents: getComputedStyle(el).pointerEvents,
      isLink: el.tagName === "A",
      href: el.getAttribute("href"),
    };
  }

  function cssPath(el) {
    const parts = [];
    for (let e = el; e && e.nodeType === 1 && e !== document.documentElement; e = e.parentElement) {
      if (e.id && /^[A-Za-z][\w-]*$/.test(e.id)) {
        parts.unshift("#" + e.id);
        break;
      }
      let i = 1;
      for (let s = e.previousElementSibling; s; s = s.previousElementSibling) if (s.tagName === e.tagName) i++;
      parts.unshift(`${e.tagName.toLowerCase()}:nth-of-type(${i})`);
    }
    return parts.join(" > ");
  }

  function describe(target) {
    const el = target.closest(CLICKABLE) || target;
    return { tag: el.tagName.toLowerCase(), id: el.id || "", text: textOf(el), selector: cssPath(el) };
  }

  // Prefer elements whose text matches what was clicked last time; layouts shift, labels rarely do.
  function find(step) {
    const sameText = (el) => !step.text || textOf(el) === step.text;
    if (step.id) {
      const el = document.getElementById(step.id);
      if (visible(el) && sameText(el)) return el;
    }
    let el = null;
    try { el = document.querySelector(step.selector); } catch (e) { /* bad selector */ }
    if (visible(el) && sameText(el)) return el;
    if (step.text) {
      for (const c of document.querySelectorAll(step.tag)) {
        if (visible(c) && textOf(c) === step.text) return c;
      }
    }
    return null;
  }

  function captchaPending() {
    const widget = [...document.querySelectorAll(CAPTCHA)].some((el) =>
      !el.closest(BACKGROUND_CAPTCHA) && (visible(el) || el.tagName === "IFRAME" && el.isConnected && el.offsetWidth > 0));
    const challengePage = /just a moment|attention required|verify you are human/i.test(document.title);
    if (!widget && !challengePage) return false;
    const tokens = [...document.querySelectorAll(TOKENS)];
    return !(tokens.length && tokens.every((t) => t.value)); // solved once every token is filled
  }

  // Tell the background when a captcha needs you, and when it's solved.
  function watchCaptcha() {
    const pending = captchaPending();
    if (!pending) pendingSince = 0;
    else if (!pendingSince) pendingSince = Date.now();
    const now = pending && Date.now() - pendingSince >= CAPTCHA_GRACE;
    if (now !== captcha) {
      captcha = now;
      browser.runtime.sendMessage({ resolver: true, type: now ? "captcha" : "captchaDone" }).catch(() => {}); // background not listening
    }
  }
  watchCaptcha();
  setInterval(watchCaptcha, 700);

  async function untilNoCaptcha() {
    while (captchaPending()) await sleep(500);
  }

  let replaying = 0; // a newer replay command stops an older one

  // Clicks a step's element as soon as the page makes it ready, then watches
  // whether the next step's element shows up (the background sees the rest).
  // budget: how long the element may take to appear, from the recipe's waits.
  async function replay(step, index, next, budget) {
    const run = ++replaying;
    await untilNoCaptcha();
    const start = Date.now();
    let el = null;
    while (!(el = find(step)) && Date.now() - start < budget) {
      await sleep(300);
      if (run !== replaying) return;
    }
    if (!el) {
      browser.runtime.sendMessage({ resolver: true, type: "notFound", index }).catch(() => {}); // background not listening
      return;
    }
    el.scrollIntoView({ block: "center" });
    while (!isReady(factsOf(el)) || captchaPending()) {
      await sleep(250);
      if (run !== replaying) return;
      if (!el.isConnected && !(el = find(step))) return replay(step, index, next, budget - (Date.now() - start));
    }
    browser.runtime.sendMessage({ resolver: true, type: "clicked", index }).catch(() => {}); // background not listening
    el.click();
    if (!next) return;
    const clicked = Date.now();
    while (Date.now() - clicked < EFFECT_WAIT && run === replaying) {
      await sleep(200);
      if (find(next)) {
        browser.runtime.sendMessage({ resolver: true, type: "effect", kind: "nextStep", index }).catch(() => {}); // background not listening
        return;
      }
    }
  }

  // The bar lives in a shadow root so page CSS can't touch it, and takes the
  // pages' tokens (ui.css) and the stored accent, light and dark.
  const BAR_CSS = (t) => `
    :host { all: initial; position: fixed; top: 0; left: 0; right: 0; z-index: 2147483647; }
    .bar { --accent: ${t.light.accent}; --on-accent: ${t.light.onAccent}; --soft: ${t.light.accentSoft};
      --panel: #fff; --text: #16181d; --muted: #646b78; --line: #e2e5ea;
      display: flex; flex-wrap: wrap; align-items: center; gap: 8px 12px; padding: 8px 16px;
      font: 14px/1.4 Rubik, system-ui, sans-serif; color: var(--text); background: var(--panel);
      border-bottom: 2px solid var(--accent); box-shadow: 0 2px 10px rgba(0,0,0,.15); }
    @media (prefers-color-scheme: dark) { .bar { --accent: ${t.dark.accent}; --on-accent: ${t.dark.onAccent};
      --soft: ${t.dark.accentSoft}; --panel: #1d2026; --text: #e8eaee; --muted: #9aa1ad; --line: #2c3038; } }
    b { color: var(--accent); }
    .more { flex-basis: 100%; margin: 0; color: var(--muted); }
    button { font: inherit; cursor: pointer; padding: 3px 10px; border-radius: 4px; color: var(--text);
      background: transparent; border: 1px solid var(--line); }
    button.q { padding: 3px 8px; font-weight: 600; color: var(--accent); background: var(--soft); border-color: transparent; }
    .sp { flex: 1; }`;

  function startRecording(msg) {
    if (recording) return;
    recording = true;
    const { ifaceName, prev, next, n, of, learn, tokens } = msg;
    const host = document.createElement("div");
    host.id = "dms-resolver-bar";
    const root = host.attachShadow({ mode: "closed" });
    const el = (tag, text, cls) => Object.assign(document.createElement(tag), { textContent: text, className: cls || "" });
    const style = el("style", BAR_CSS(tokens));
    const bar = el("div", "", "bar");
    const count = of > 1 ? `${n} of ${of}, ` : "";
    const title = el("span", "");
    title.append(el("b", `DMS² is watching (${count}${ifaceName}): `), document.createTextNode("click to the download as you normally do."));
    const more = el("p", `Wrong page? ${prev || next ? "Go to the page you started from with the buttons here, or paste it" : "Paste the page you started from"} into the address bar. ` +
      "Start on the page of the file you want, not a list of files: DMS² repeats your clicks for every download from this site. " +
      (learn ? "Skip closes this window; nothing changes for the download." : "Skip closes this window and this connection's download fails."), "more");
    more.hidden = true;
    const q = el("button", "?", "q");
    q.title = "More";
    q.addEventListener("click", () => { more.hidden = !more.hidden; });
    bar.append(title, q, el("span", "", "sp"));
    if (prev) {
      const back = el("button", "◀ Previous page");
      back.addEventListener("click", () => { location.href = prev; });
      bar.append(back);
    }
    if (next) {
      const fwd = el("button", "Next page ▶");
      fwd.addEventListener("click", () => { location.href = next; });
      bar.append(fwd);
    }
    const skip = el("button", "Skip this connection");
    skip.addEventListener("click", () => browser.runtime.sendMessage({ resolver: true, type: "skip" }).catch(() => {}));
    bar.append(skip, more);
    root.append(style, bar);
    document.documentElement.appendChild(host);
    document.addEventListener("click", (e) => {
      // Clicks on the captcha are yours alone; they must not become steps to replay.
      if (!e.isTrusted || e.target.closest("#dms-resolver-bar") || e.target.closest(CAPTCHA)) return;
      browser.runtime.sendMessage({ resolver: true, type: "recorded", step: describe(e.target) }).catch(() => {}); // background not listening
    }, true);
  }

  browser.runtime.onMessage.addListener((msg) => {
    if (msg.cmd === "replay") replay(msg.step, msg.index, msg.next, msg.budget);
    if (msg.cmd === "record") startRecording(msg);
  });
})();
