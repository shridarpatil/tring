import { ref, watch, onMounted, type Ref } from "vue";
import { api } from "../api/client";
import { errMessage } from "../lib/errors";
import type { Paginated } from "../api/types";

// usePagedList is the paged sibling of useList: it owns the page/total/items
// state and fetches the {items,total,limit,offset} envelope, so a view only
// wires up the table + <Pagination>.
//
//   const { items, page, pageSize, total, loading, load } =
//     usePagedList<Call>("/v1/calls", { params });
//
// `params` is an optional reactive ref of extra query params (e.g. { status });
// changing it resets to page 1 and reloads. After a mutation call load() to
// refresh the current page — if it ends up empty and page > 1, it clamps back.
export function usePagedList<T>(
  endpoint: string,
  opts: { pageSize?: number; params?: Ref<Record<string, string>>; auto?: boolean } = {},
) {
  const items = ref<T[]>([]) as Ref<T[]>;
  const page = ref(1);
  const pageSize = ref(opts.pageSize ?? 25);
  const total = ref(0);
  const loading = ref(true);
  const error = ref("");

  function buildQuery(): string {
    const q = new URLSearchParams({
      page: String(page.value),
      limit: String(pageSize.value),
    });
    const extra = opts.params?.value ?? {};
    for (const [k, v] of Object.entries(extra)) {
      if (v) q.set(k, v);
    }
    return q.toString();
  }

  async function load(o: { silent?: boolean } = {}) {
    if (!o.silent) loading.value = true;
    try {
      const res = await api.get<Paginated<T>>(`${endpoint}?${buildQuery()}`);
      items.value = res.items;
      total.value = res.total;
      // A deletion can leave the current page past the end (e.g. the last row
      // of the last page) — clamp back one page and refetch.
      if (res.items.length === 0 && page.value > 1) {
        page.value -= 1;
        await load(o);
        return;
      }
    } catch (e) {
      if (!o.silent) error.value = errMessage(e, "failed to load");
    } finally {
      if (!o.silent) loading.value = false;
    }
  }

  function setPage(n: number) {
    if (n === page.value) return;
    page.value = n;
    load();
  }

  // Param changes (e.g. a status filter) start over from page 1.
  if (opts.params) {
    watch(
      opts.params,
      () => {
        page.value = 1;
        load();
      },
      { deep: true },
    );
  }

  if (opts.auto !== false) onMounted(() => load());
  return { items, page, pageSize, total, loading, error, load, setPage };
}
