import { api } from "../api/client";

// The slice of the SDK's Tring instance the console uses.
export interface AgentWidget {
  available: boolean;
  on(type: "availability", fn: (e: { available: boolean }) => void): () => void;
}

interface TringWidgetGlobal {
  bootstrap(opts: { apiBase: string; credentials: () => Promise<unknown> }): Promise<AgentWidget>;
}

// bootstrap() has no teardown, so the widget is mounted once per page load and
// its floating panel outlives route changes. Cached as a promise so concurrent
// callers share one script tag and one registration. A failure clears the cache
// so a later visit can retry.
let mounted: Promise<AgentWidget> | null = null;

export function mountAgentWidget(): Promise<AgentWidget> {
  mounted ??= load().catch((e) => {
    mounted = null;
    throw e;
  });
  return mounted;
}

async function load(): Promise<AgentWidget> {
  const g = globalThis as unknown as { TringWidget?: TringWidgetGlobal };
  if (!g.TringWidget) {
    await new Promise<void>((resolve, reject) => {
      const s = document.createElement("script");
      s.src = "/sdk/agent.js";
      s.setAttribute("data-manual", "");
      s.onload = () => resolve();
      s.onerror = () => reject(new Error("could not load /sdk/agent.js"));
      document.head.appendChild(s);
    });
  }
  if (!g.TringWidget) throw new Error("agent widget did not initialise");
  return g.TringWidget.bootstrap({ apiBase: "", credentials: () => api.post("/v1/me/token") });
}
