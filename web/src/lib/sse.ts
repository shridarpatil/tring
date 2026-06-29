import { auth } from "../stores/auth";

export interface SSEHandlers {
  onEvent: (type: string, data: unknown) => void;
  onOpen?: () => void;
  onError?: () => void;
}

// streamEvents opens an authenticated SSE stream via fetch (so we can send the
// Authorization header — native EventSource cannot). Auto-reconnects with capped
// backoff until the returned stop() is called.
export function streamEvents(path: string, h: SSEHandlers): () => void {
  let stopped = false;
  let ctrl: AbortController | null = null;
  let backoff = 1000;

  function parseFrame(raw: string) {
    let ev = "message";
    const dataLines: string[] = [];
    for (const line of raw.split("\n")) {
      if (line.startsWith(":")) continue; // keepalive comment
      if (line.startsWith("event:")) ev = line.slice(6).trim();
      else if (line.startsWith("data:")) dataLines.push(line.slice(5).trim());
    }
    if (!dataLines.length) return;
    try {
      h.onEvent(ev, JSON.parse(dataLines.join("\n")));
    } catch {
      /* ignore malformed frame */
    }
  }

  async function connect() {
    if (stopped) return;
    ctrl = new AbortController();
    try {
      const token = auth.token();
      const res = await fetch(path, {
        headers: token ? { Authorization: `Bearer ${token}` } : {},
        signal: ctrl.signal,
      });
      if (!res.ok || !res.body) {
        if (res.status === 401) {
          auth.logout();
          stopped = true;
          return;
        }
        throw new Error(`stream ${res.status}`);
      }
      h.onOpen?.();
      backoff = 1000;
      const reader = res.body.getReader();
      const decoder = new TextDecoder();
      let buf = "";
      try {
        for (;;) {
          const { value, done } = await reader.read();
          if (done) break;
          buf += decoder.decode(value, { stream: true });
          let idx: number;
          while ((idx = buf.indexOf("\n\n")) >= 0) {
            parseFrame(buf.slice(0, idx));
            buf = buf.slice(idx + 2);
          }
        }
      } finally {
        reader.releaseLock();
      }
    } catch {
      /* fall through to reconnect */
    }
    if (stopped) return;
    h.onError?.();
    setTimeout(connect, backoff);
    backoff = Math.min(backoff * 2, 15000);
  }

  void connect();
  return () => {
    stopped = true;
    ctrl?.abort();
  };
}
