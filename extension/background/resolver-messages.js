"use strict";

// The resolver's messages from its tabs' content script: clicks, captchas, recorded steps.

Object.assign(Resolver, {
  onContentMessage(msg, sender) {
    if (!sender.tab || !msg || !msg.resolver) return;
    const job = this.tabJobs.get(sender.tab.id);
    if (!job || job.done) return;
    const steps = this.steps(job);
    switch (msg.type) {
      case "clicked": {
        if (job.mode !== "replay") return;
        const prev = job.clicked && job.clicked.index === msg.index ? job.clicked : null;
        job.clicked = { index: msg.index, at: Date.now(), clicks: prev ? prev.clicks + 1 : 1, effects: {}, mark: prev ? prev.mark : job.mark };
        job.lastClick = job.clicked.at;
        this.setTimer(job, "effect", EFFECT_WAIT, "judgeClick");
        break;
      }
      case "effect":
        if (job.clicked && job.clicked.index === msg.index) this.clickEffect(job, msg.kind);
        break;
      case "notFound":
        if (job.mode !== "replay") return;
        // A remembered click may have been optional (e.g. a banner): try skipping it once.
        if (!job.skipped && msg.index + 1 < steps.length) {
          job.skipped = true;
          job.step = msg.index + 1;
          this.sendStep(job);
        } else {
          quiet(this.budgetOut(job, msg.index, budget(steps[msg.index].wait), "Couldn't find the button to click"));
        }
        break;
      case "captcha":
        this.captchaShown(job);
        break;
      case "captchaDone":
        if (job.captcha) this.captchaDone(job);
        break;
      case "skip": // the same as closing the window
        if (job.windowId !== null) browser.windows.remove(job.windowId).catch(() => {});
        break;
      case "recorded":
        if (job.mode !== "record") return;
        // The page of your first click is the real landing page: you may have had
        // to navigate there yourself if the page DMS² opened was the wrong one.
        if (job.recordFrom === 0 && job.recorded.length === 0) job.startUrl = sender.tab.url;
        job.recorded.push({ ...msg.step, wait: Date.now() - job.mark });
        job.mark = job.lastClick = Date.now();
        this.touch(job);
        break;
    }
    this.save();
  }
});
