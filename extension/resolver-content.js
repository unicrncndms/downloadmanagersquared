"use strict";

// Injected into resolver tabs only. Replays a remembered click, or records the
// user's clicks so they can be replayed next time. Watches for captchas, which
// only a person can solve, and holds everything until one is done.
(() => {
  if (window.__dmsResolver) return;
  window.__dmsResolver = true;

  const CLICKABLE = "a,button,input[type=submit],input[type=button],input[type=image],[role=button],[onclick]";
  const FIND_TIMEOUT = 10000;
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
  // Many captchas pass by themselves within a second or two; only ask for help
  // when one is still there after this long.
  const CAPTCHA_GRACE = 5000;
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
      browser.runtime.sendMessage({ resolver: true, type: now ? "captcha" : "captchaDone" });
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
  async function replay(step, index, next) {
    const run = ++replaying;
    await untilNoCaptcha();
    const start = Date.now();
    let el = null;
    while (!(el = find(step)) && Date.now() - start < FIND_TIMEOUT) {
      await sleep(300);
      if (run !== replaying) return;
    }
    if (!el) {
      browser.runtime.sendMessage({ resolver: true, type: "notFound", index });
      return;
    }
    el.scrollIntoView({ block: "center" });
    while (!isReady(factsOf(el)) || captchaPending()) {
      await sleep(250);
      if (run !== replaying) return;
      if (!el.isConnected && !(el = find(step))) return replay(step, index, next);
    }
    browser.runtime.sendMessage({ resolver: true, type: "clicked", index });
    el.click();
    if (!next) return;
    const clicked = Date.now();
    while (Date.now() - clicked < EFFECT_WAIT && run === replaying) {
      await sleep(200);
      if (find(next)) {
        browser.runtime.sendMessage({ resolver: true, type: "effect", kind: "nextStep", index });
        return;
      }
    }
  }

  function startRecording(ifaceName, prev) {
    if (recording) return;
    recording = true;
    const bar = document.createElement("div");
    bar.id = "dms-resolver-bar";
    bar.textContent = prev
      ? `DMS² is learning (${ifaceName}): click through to the download as usual. Wrong page? Go back first. `
      : `DMS² is learning (${ifaceName}): click through to the download as usual. Wrong page? Paste the page you started from into the address bar above.`;
    Object.assign(bar.style, {
      position: "fixed", left: "0", right: "0", bottom: "0", zIndex: "2147483647",
      padding: "10px 16px", font: "14px system-ui, sans-serif", color: "#fff",
      background: "#2563eb", textAlign: "center",
    });
    if (prev) {
      const back = document.createElement("button");
      back.textContent = "◀ Previous page";
      Object.assign(back.style, {
        marginLeft: "8px", padding: "3px 10px", font: "inherit", cursor: "pointer",
        color: "#2563eb", background: "#fff", border: "0", borderRadius: "4px",
      });
      back.addEventListener("click", () => { location.href = prev; });
      bar.appendChild(back);
    }
    document.documentElement.appendChild(bar);
    document.addEventListener("click", (e) => {
      // Clicks on the captcha are yours alone; they must not become steps to replay.
      if (!e.isTrusted || e.target.closest("#dms-resolver-bar") || e.target.closest(CAPTCHA)) return;
      browser.runtime.sendMessage({ resolver: true, type: "recorded", step: describe(e.target) });
    }, true);
  }

  browser.runtime.onMessage.addListener((msg) => {
    if (msg.cmd === "replay") replay(msg.step, msg.index, msg.next);
    if (msg.cmd === "record") startRecording(msg.ifaceName, msg.prev);
  });
})();
