// Minimal typed event emitter. Keeping this in-tree avoids pulling a
// dependency just for a one-screen helper.

type AnyEvent = { type: string };
type Listener<E> = (event: E) => void;

export class Emitter<E extends AnyEvent> {
  private listeners = new Map<string, Set<Listener<any>>>();

  on<T extends E["type"]>(
    type: T,
    fn: Listener<Extract<E, { type: T }>>,
  ): () => void {
    let set = this.listeners.get(type);
    if (!set) {
      set = new Set();
      this.listeners.set(type, set);
    }
    set.add(fn);
    return () => this.off(type, fn);
  }

  off<T extends E["type"]>(
    type: T,
    fn: Listener<Extract<E, { type: T }>>,
  ): void {
    this.listeners.get(type)?.delete(fn);
  }

  protected emit(event: E): void {
    const set = this.listeners.get(event.type);
    if (!set) return;
    for (const fn of set) {
      try {
        fn(event);
      } catch (e) {
        // Listener errors must not break event dispatch.
        console.error("[tring] listener error:", e);
      }
    }
  }
}
