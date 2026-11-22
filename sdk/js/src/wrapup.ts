// Post-call wrap-up panel: after the agent's call ends the gateway opens a
// timed wrap-up window (`wrap-up` over /rtc); this card collects the
// disposition, tags and notes and saves them via the agent-JWT REST API. It is
// its own card (not the call panel) because the call panel hides on call end.
// The gateway re-sends `wrap-up` on reconnect with the original ends_at, so a
// page refresh restores the panel with no client-side state.

export interface WrapUpOption {
  id: string;
  name: string;
}

export interface WrapUpOptions {
  dispositions: WrapUpOption[];
  tags: WrapUpOption[];
}

export interface WrapUpSave {
  call_id: string;
  disposition_id?: string;
  notes?: string;
  tag_ids?: string[];
}

/** REST access for the panel; the widget builds it from its agent-token flow. */
export interface WrapUpApi {
  /** The labels this call's wrap-up may use (shared + the call's wrap-up team). */
  options(callId: string): Promise<WrapUpOptions | null>;
  /** Resolves with the HTTP status (0 on a network error) and any server message. */
  save(body: WrapUpSave): Promise<{ status: number; error?: string }>;
}

export interface WrapUpEvent {
  callId: string;
  endsAt: string;
  requireDisposition: boolean;
}

export interface WrapUpPanel {
  root: HTMLElement;
  /** Open the panel, or replace it when a different call's wrap-up is shown. */
  show(ev: WrapUpEvent): void;
  /** End this call's wrap-up: close the panel if it is showing it, and ignore any later show of it. */
  end(callId: string): void;
}

export const WRAPUP_ID = "tring-wrapup";
const MAX_NOTES = 2000;
const MAX_TAGS = 10;
// If the server's wrap-up-end never arrives, close this long after ends_at.
const END_GRACE_MS = 5000;

export function createWrapUpPanel(api?: WrapUpApi): WrapUpPanel {
  const root = document.createElement("div");
  root.id = WRAPUP_ID;
  root.setAttribute("role", "dialog");
  root.setAttribute("aria-label", "Call wrap-up");

  // Options depend on the call (its wrap-up team picks the labels), so they
  // are cached for the call on screen only; a failed fetch is not cached so a
  // later show retries.
  let cached: { callId: string; p: Promise<WrapUpOptions | null> } | null = null;
  const loadOptions = (callId: string): Promise<WrapUpOptions | null> => {
    if (!api) return Promise.resolve(null);
    if (cached?.callId !== callId) {
      const entry = {
        callId,
        p: api.options(callId).then((o) => {
          if (!o && cached === entry) cached = null;
          return o;
        }),
      };
      cached = entry;
    }
    return cached.p;
  };

  let current: string | null = null; // call_id on screen
  let tick: number | undefined;
  let fallback: number | undefined;
  let closeTimer: number | undefined;

  const close = () => {
    window.clearInterval(tick);
    window.clearTimeout(fallback);
    window.clearTimeout(closeTimer);
    current = null;
    root.classList.remove("tring-wrapup-visible");
    root.textContent = "";
  };
  // Calls whose wrap-up ended here (wrap-up-end, save, or the safety timer).
  // A wrap-up for one of them arriving later is stale — e.g. sent after the
  // end that raced it, while the agent is already on a new call — and must not
  // reopen the card over the call panel.
  // Capped at the most recent 50 (a Set iterates oldest first) so a long-lived
  // agent page doesn't grow it by one id per call forever.
  const finished = new Set<string>();
  const finish = (callId: string) => {
    finished.add(callId);
    if (finished.size > 50) finished.delete(finished.values().next().value as string);
    if (current === callId) close();
  };

  const el = <K extends keyof HTMLElementTagNameMap>(
    tag: K,
    cls?: string,
    text?: string,
  ): HTMLElementTagNameMap[K] => {
    const n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text !== undefined) n.textContent = text;
    return n;
  };

  function show(ev: WrapUpEvent): void {
    // A resend for the call on screen (reconnect) must not wipe what was typed.
    if (current === ev.callId || finished.has(ev.callId)) return;
    close();
    current = ev.callId;
    const callId = ev.callId;
    const endsAtMs = Date.parse(ev.endsAt);

    let opts: WrapUpOptions = { dispositions: [], tags: [] };
    const tagIds: string[] = [];
    let busy = false;

    const card = el("div", "tring-wrapup-card");
    const head = el("div", "tring-wrapup-head");
    head.append(el("span", "tring-wrapup-title", "Call ended — wrap-up"));
    const timeEl = el("span", "tring-wrapup-time");
    head.append(timeEl);

    const dispLabel = el("label", "tring-wrapup-label", ev.requireDisposition ? "Disposition *" : "Disposition");
    const dispSel = el("select", "tring-wrapup-field");
    dispLabel.append(dispSel);

    const tagsLabel = el("div", "tring-wrapup-label", "Tags");
    const chips = el("div", "tring-wrapup-chips");
    const tagSel = el("select", "tring-wrapup-field");
    tagsLabel.append(chips, tagSel);

    const notesLabel = el("label", "tring-wrapup-label", "Notes");
    const notes = el("textarea", "tring-wrapup-field tring-wrapup-notes");
    notes.maxLength = MAX_NOTES;
    notes.rows = 3;
    notesLabel.append(notes);

    const errEl = el("div", "tring-wrapup-error");
    errEl.setAttribute("role", "alert");
    const done = el("button", "tring-wrapup-done", "Done");
    done.type = "button";

    card.append(head, dispLabel, tagsLabel, notesLabel, errEl, done);
    root.append(card);
    root.classList.add("tring-wrapup-visible");

    const nameOf = (id: string) => opts.tags.find((t) => t.id === id)?.name ?? id;
    const refreshDone = () => {
      done.disabled = busy || (ev.requireDisposition && !dispSel.value);
    };
    const renderTags = () => {
      chips.textContent = "";
      for (const id of tagIds) {
        const chip = el("button", "tring-wrapup-chip", nameOf(id) + " ×");
        chip.type = "button";
        chip.title = "Remove tag";
        chip.addEventListener("click", () => {
          tagIds.splice(tagIds.indexOf(id), 1);
          renderTags();
        });
        chips.append(chip);
      }
      tagSel.textContent = "";
      tagSel.append(new Option(tagIds.length >= MAX_TAGS ? "Tag limit reached" : "Add tag…", ""));
      for (const t of opts.tags) if (!tagIds.includes(t.id)) tagSel.append(new Option(t.name, t.id));
      tagSel.disabled = tagIds.length >= MAX_TAGS || opts.tags.length === 0;
    };
    const renderDispositions = () => {
      dispSel.textContent = "";
      dispSel.append(new Option(ev.requireDisposition ? "Select…" : "None", ""));
      for (const d of opts.dispositions) dispSel.append(new Option(d.name, d.id));
      refreshDone();
    };
    renderDispositions();
    renderTags();

    void loadOptions(callId).then((o) => {
      if (!o || current !== callId) return;
      opts = o;
      const keep = dispSel.value;
      renderDispositions();
      dispSel.value = keep;
      renderTags();
      refreshDone();
    });

    dispSel.addEventListener("change", refreshDone);
    tagSel.addEventListener("change", () => {
      if (tagSel.value && tagIds.length < MAX_TAGS) tagIds.push(tagSel.value);
      renderTags();
    });

    done.addEventListener("click", async () => {
      if (busy || !api) return;
      busy = true;
      errEl.textContent = "";
      refreshDone();
      const body: WrapUpSave = { call_id: callId };
      if (dispSel.value) body.disposition_id = dispSel.value;
      if (notes.value.trim()) body.notes = notes.value;
      if (tagIds.length) body.tag_ids = [...tagIds];
      const res = await api.save(body);
      if (current !== callId) return; // closed or replaced while saving
      busy = false;
      if (res.status === 200) {
        finish(callId);
      } else if (res.status === 409) {
        errEl.textContent = "Wrap-up already ended";
        done.disabled = true;
        closeTimer = window.setTimeout(() => finish(callId), 2000);
      } else {
        errEl.textContent = res.error || "Could not save wrap-up, try again";
        refreshDone();
      }
    });

    // The countdown stops at 0:00 and disables nothing: the server's
    // wrap-up-end closes the panel (the timer below is only a safety net).
    const renderTime = () => {
      const left = Math.max(0, Math.ceil((endsAtMs - Date.now()) / 1000));
      timeEl.textContent = `${Math.floor(left / 60)}:${String(left % 60).padStart(2, "0")}`;
    };
    renderTime();
    tick = window.setInterval(renderTime, 500);
    fallback = window.setTimeout(() => finish(callId), Math.max(0, endsAtMs - Date.now()) + END_GRACE_MS);
  }

  return {
    root,
    show,
    end: finish,
  };
}

// Same light-card look as the call panel (see injectStyles in widget.ts).
export const WRAPUP_CSS = `
  #${WRAPUP_ID} {
    position: fixed; right: 1.5rem; bottom: 1.5rem;
    width: 400px; max-width: calc(100vw - 2rem); box-sizing: border-box;
    color: #1f2420; font: 14px/1.4 system-ui, -apple-system, "Segoe UI", sans-serif;
    transform: translateY(180%); opacity: 0; pointer-events: none;
    transition: transform .2s ease, opacity .2s ease;
    z-index: 1000000;
  }
  #${WRAPUP_ID} * { box-sizing: border-box; }
  #${WRAPUP_ID}.tring-wrapup-visible { transform: translateY(0); opacity: 1; pointer-events: auto; }
  #${WRAPUP_ID} .tring-wrapup-card {
    background: #fbfaf7; border: 1px solid #e4e0d5; border-radius: 22px;
    box-shadow: 0 18px 40px rgba(31,36,32,.12), 0 2px 6px rgba(31,36,32,.06);
    padding: 20px; display: flex; flex-direction: column; gap: 12px;
  }
  #${WRAPUP_ID} .tring-wrapup-head { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
  #${WRAPUP_ID} .tring-wrapup-title { font-weight: 700; font-size: 15px; }
  #${WRAPUP_ID} .tring-wrapup-time {
    font: 13px ui-monospace, SFMono-Regular, Menlo, monospace; color: #5c6058;
    font-variant-numeric: tabular-nums;
  }
  #${WRAPUP_ID} .tring-wrapup-label {
    display: flex; flex-direction: column; gap: 6px;
    font-size: 12px; font-weight: 700; color: #5c6058; letter-spacing: .05em; text-transform: uppercase;
  }
  #${WRAPUP_ID} .tring-wrapup-field {
    width: 100%; padding: 8px 10px; border: 1px solid #dcd8cc; border-radius: 10px;
    background: #fff; color: #1f2420; font: inherit; text-transform: none; letter-spacing: 0; font-weight: 400;
  }
  #${WRAPUP_ID} .tring-wrapup-notes { resize: vertical; }
  #${WRAPUP_ID} .tring-wrapup-field:focus-visible,
  #${WRAPUP_ID} button:focus-visible { outline: 2px solid #0f5c46; outline-offset: 2px; }
  #${WRAPUP_ID} .tring-wrapup-chips { display: flex; flex-wrap: wrap; gap: 6px; }
  #${WRAPUP_ID} .tring-wrapup-chips:empty { display: none; }
  #${WRAPUP_ID} .tring-wrapup-chip {
    padding: 3px 10px; border: 0; border-radius: 999px; cursor: pointer;
    background: #e3efe9; color: #0f5c46; font: 600 12px/1.4 system-ui, sans-serif;
    text-transform: none; letter-spacing: 0;
  }
  #${WRAPUP_ID} .tring-wrapup-error { min-height: 0; color: #a3271b; font-size: 13px; }
  #${WRAPUP_ID} .tring-wrapup-error:empty { display: none; }
  #${WRAPUP_ID} .tring-wrapup-done {
    height: 46px; border: 0; border-radius: 14px; cursor: pointer;
    background: #0f5c46; color: #fff; font: inherit; font-size: 16px; font-weight: 700;
  }
  #${WRAPUP_ID} .tring-wrapup-done:hover { background: #0b4736; }
  #${WRAPUP_ID} .tring-wrapup-done:disabled { opacity: .5; cursor: default; }
`;
