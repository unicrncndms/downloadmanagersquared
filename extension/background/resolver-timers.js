"use strict";

// The resolver's deadlines: stored in each job, run by one timeout for the
// nearest, so they hold across a restart of the background.

Object.assign(Resolver, {
  // In ms, call this[action](job, ...args): a deadline in the job's data, so
  // it holds across a restart of the background (see recheck).
  setTimer(job, name, ms, action, ...args) {
    job.deadlines[name] = { at: Date.now() + ms, action, args };
    this.schedule();
  },

  clearTimer(job, name) {
    delete job.deadlines[name];
    this.schedule();
  },

  clearTimers(job) {
    job.deadlines = {};
    this.schedule();
  },

  // One timeout, for the nearest deadline of any job. Losing it loses
  // nothing: load() rechecks.
  schedule() {
    clearTimeout(this.wake);
    let next = Infinity;
    for (const j of this.jobs.values()) for (const d of Object.values(j.deadlines)) next = Math.min(next, d.at);
    if (next < Infinity) this.wake = setTimeout(() => this.recheck(), Math.max(0, next - Date.now()));
    this.save();
  },

  // Runs what is due.
  recheck() {
    const now = Date.now();
    for (const job of [...this.jobs.values()]) {
      for (const [name, d] of Object.entries(job.deadlines)) {
        if (job.done || d.at > now || job.deadlines[name] !== d) continue;
        delete job.deadlines[name];
        quiet(this[d.action](job, ...d.args));
      }
    }
    this.schedule();
  }
});
