import assert from "node:assert/strict";
import { beforeEach, afterEach, test } from "node:test";
import { IncomingCallNotification, supportsCallNotifications } from "../src/call-notifications.ts";

let alerts;
let notifier;
let opened;
const saved = new Map(["window", "document", "Notification"].map(key => [key, Object.getOwnPropertyDescriptor(globalThis, key)]));

beforeEach(() => {
  alerts = [];
  opened = 0;
  globalThis.document = Object.assign(new EventTarget(), {
    hidden: true,
    focused: false,
    hasFocus() { return this.focused; },
  });
  globalThis.window = Object.assign(new EventTarget(), {
    isSecureContext: true,
    focus() {
      document.hidden = false;
      document.focused = true;
      this.dispatchEvent(new Event("focus"));
    },
  });
  globalThis.Notification = class {
    static permission = "granted";
    constructor(title, options) {
      this.title = title;
      this.options = options;
      this.closed = false;
      alerts.push(this);
    }
    close() { this.closed = true; }
  };
  notifier = new IncomingCallNotification(() => opened++);
});

afterEach(() => {
  notifier.stop();
  for (const [key, descriptor] of saved) {
    if (descriptor) Object.defineProperty(globalThis, key, descriptor);
    else delete globalThis[key];
  }
});

test("incoming call in a background tab shows the caller and opens the existing tab", () => {
  notifier.start("Web Caller");
  assert.equal(alerts.length, 1);
  assert.equal(alerts[0].title, "Incoming call");
  assert.match(alerts[0].options.body, /Web Caller/);
  assert.equal(alerts[0].options.requireInteraction, true);
  alerts[0].onclick();
  assert.equal(document.hasFocus(), true);
  assert.equal(alerts[0].closed, true);
  assert.equal(opened, 1);
});

test("a foreground call alerts when the agent switches tabs, once per ring", () => {
  document.hidden = false;
  document.focused = true;
  notifier.start("Caller");
  assert.equal(alerts.length, 0);
  document.hidden = true;
  document.focused = false;
  document.dispatchEvent(new Event("visibilitychange"));
  window.dispatchEvent(new Event("blur"));
  assert.equal(alerts.length, 1);
  window.focus();
  assert.equal(alerts[0].closed, true);
  document.hidden = true;
  window.dispatchEvent(new Event("blur"));
  assert.equal(alerts.length, 1);
});

test("switching to another app alerts even when the tab remains visible", () => {
  document.hidden = false;
  notifier.start("Caller");
  assert.equal(alerts.length, 1);
});

test("ending or accepting a ring clears the alert and detaches background listeners", () => {
  notifier.start("Caller");
  notifier.stop();
  assert.equal(alerts[0].closed, true);
  assert.equal(alerts[0].onclick, null);
  window.dispatchEvent(new Event("blur"));
  document.dispatchEvent(new Event("visibilitychange"));
  assert.equal(alerts.length, 1);
  notifier.start("Next caller");
  assert.equal(alerts.length, 2);
});

test("replacing a ring and leaving the page close stale notifications", () => {
  notifier.start("First caller");
  notifier.start("Second caller");
  assert.equal(alerts[0].closed, true);
  assert.equal(alerts[0].onclick, null);
  assert.match(alerts[1].options.body, /Second caller/);
  window.dispatchEvent(new Event("pagehide"));
  assert.equal(alerts[1].closed, true);
});

test("default and denied permissions never create alerts or prompt during a call", () => {
  for (const permission of ["default", "denied"]) {
    Notification.permission = permission;
    notifier.start("Caller");
  }
  assert.equal(alerts.length, 0);
});

test("unsupported and insecure browsers do not interrupt ringing", () => {
  window.isSecureContext = false;
  assert.equal(supportsCallNotifications(), false);
  notifier.start("Caller");
  assert.equal(alerts.length, 0);
  window.isSecureContext = true;
  delete globalThis.Notification;
  assert.equal(supportsCallNotifications(), false);
  assert.doesNotThrow(() => notifier.start("Caller"));
});

test("notification constructor failures do not interrupt ringing", () => {
  globalThis.Notification = class {
    static permission = "granted";
    constructor() { throw new TypeError("Desktop notifications unavailable"); }
  };
  assert.doesNotThrow(() => notifier.start("Caller"));
  assert.doesNotThrow(() => notifier.stop());
});
