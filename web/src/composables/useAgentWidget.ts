import { ref, onMounted, onUnmounted } from "vue";
import { api } from "../api/client";
import { errMessage } from "../lib/errors";
import { toast } from "../lib/toast";
import { mountAgentWidget } from "../lib/widget";

// Mounts the floating call panel (once per page load; see lib/widget.ts) and
// mirrors the user's availability for the header toggle. The panel speaks for
// itself, so only a failure to load it is surfaced, as a toast.
export function useAgentWidget() {
  // No GET for own availability: the widget's Tring instance carries it and
  // emits `availability` on every change, so the toggle mirrors that.
  const available = ref(true);
  const savingAvail = ref(false);
  let offAvail: (() => void) | undefined;
  // The mount is async: a late resolve must not register a listener (or touch
  // state) after the view is gone.
  let unmounted = false;

  async function setAvailable(v: boolean) {
    const prev = available.value;
    available.value = v;
    savingAvail.value = true;
    try {
      await api.post("/v1/me/availability", { available: v });
    } catch (e) {
      available.value = prev;
      toast.error(errMessage(e, "could not change availability"));
    } finally {
      savingAvail.value = false;
    }
  }

  onMounted(() => {
    mountAgentWidget()
      .then((w) => {
        if (unmounted) return;
        available.value = w.available;
        offAvail = w.on("availability", (e) => (available.value = e.available));
      })
      .catch((e) => !unmounted && toast.error(`Call panel unavailable: ${errMessage(e, "could not load")}`));
  });
  onUnmounted(() => {
    unmounted = true;
    offAvail?.();
  });
  return { available, savingAvail, setAvailable };
}
