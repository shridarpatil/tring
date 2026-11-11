// Desktop alerts belong to the widget, not the headless calling SDK.
export function supportsCallNotifications(): boolean {
  return window.isSecureContext && typeof Notification !== "undefined";
}

export class IncomingCallNotification {
  private caller: string | null = null;
  private notification: Notification | null = null;
  private notified = false;
  private onOpen: () => void;

  constructor(onOpen: () => void) {
    this.onOpen = onOpen;
  }

  start(caller: string): void {
    this.stop();
    this.caller = caller;
    this.notified = false;
    document.addEventListener("visibilitychange", this.update);
    window.addEventListener("focus", this.update);
    window.addEventListener("blur", this.update);
    window.addEventListener("pagehide", this.stop);
    this.update();
  }

  stop = (): void => {
    this.caller = null;
    this.close();
    document.removeEventListener("visibilitychange", this.update);
    window.removeEventListener("focus", this.update);
    window.removeEventListener("blur", this.update);
    window.removeEventListener("pagehide", this.stop);
  };

  private close(): void {
    const notification = this.notification;
    this.notification = null;
    if (notification) {
      notification.onclick = null;
      notification.close();
    }
  }

  private update = (): void => {
    if (!document.hidden && document.hasFocus()) {
      this.close();
      return;
    }
    if (this.caller === null || this.notified || !supportsCallNotifications() || Notification.permission !== "granted") return;
    try {
      const notification = new Notification("Incoming call", {
        body: `${this.caller} is calling. Click to open the call.`,
        tag: "tring-incoming-call",
        requireInteraction: true,
      });
      this.notification = notification;
      this.notified = true;
      notification.onclick = () => {
        window.focus();
        this.close();
        this.onOpen();
      };
    } catch {
      // Some browsers expose the API but cannot construct desktop alerts.
      // Notification failures must never interrupt ringing or answering.
    }
  };
}
