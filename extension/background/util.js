"use strict";

// The browser.* seams the background scripts share. The pure rules they use
// are in lib/.

// Cancels a browser download and removes it from the list.
async function discardDownload(id) {
  try {
    await browser.downloads.cancel(id);
    await browser.downloads.erase({ id });
  } catch (e) { /* already gone */ }
}

// Cookie header the site would get from this container for this URL.
async function cookieHeader(url, storeId) {
  let cookies = [];
  try {
    cookies = await browser.cookies.getAll({ url, storeId, firstPartyDomain: null });
  } catch (e) {
    try { cookies = await browser.cookies.getAll({ url, storeId }); } catch (e2) { /* none */ }
  }
  return cookies.map((c) => `${c.name}=${c.value}`).join("; ");
}

function notify(title, message) {
  browser.notifications.create({
    type: "basic",
    iconUrl: browser.runtime.getURL("icons/icon.svg"),
    title,
    message,
  });
}
