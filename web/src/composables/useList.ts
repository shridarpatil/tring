import { ref, onMounted, type Ref } from "vue";
import { api } from "../api/client";
import { errMessage } from "../lib/errors";

// useList is the shared "load a list from an endpoint" scaffold: it owns the
// items/loading/error refs and the GET + try/catch that every list view
// repeated. Per-view create/update/delete stay in the view (their bodies and
// resets differ too much to generalise); call load() again after a mutation.
//
//   const { items: agents, loading, error, load } = useList<Agent>("/v1/agents");
//
// Pass { auto: false } to skip the onMounted load. load({ silent: true }) skips
// the spinner and swallows errors — for background polling that shouldn't flash
// a loading state or surface a transient failure.
export function useList<T>(endpoint: string, opts: { auto?: boolean } = {}) {
  const items = ref<T[]>([]) as Ref<T[]>;
  const loading = ref(true);
  const error = ref("");

  async function load(o: { silent?: boolean } = {}) {
    if (!o.silent) loading.value = true;
    try {
      items.value = await api.get<T[]>(endpoint);
    } catch (e) {
      if (!o.silent) error.value = errMessage(e, "failed to load");
    } finally {
      if (!o.silent) loading.value = false;
    }
  }

  if (opts.auto !== false) onMounted(() => load());
  return { items, loading, error, load };
}
