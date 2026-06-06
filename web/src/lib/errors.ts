// errMessage unwraps a caught value into a display string. Replaces the
// `e instanceof Error ? e.message : "…"` idiom that was repeated across every
// view's try/catch.
export function errMessage(e: unknown, fallback: string): string {
  return e instanceof Error ? e.message : fallback;
}
