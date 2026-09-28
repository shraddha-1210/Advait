export function cx(...c: (string | false | null | undefined)[]): string {
  return c.filter(Boolean).join(' ')
}

/** Shared text-input styling. */
export const inputClass =
  'h-10 rounded-xl bg-surface px-3 text-fg shadow-[0_1px_2px_rgba(17,24,56,0.05)] ring-1 ring-inset ring-line-strong transition-shadow placeholder:text-muted/70 focus:outline-none focus:ring-2 focus:ring-accent aria-[invalid=true]:ring-bad-line'
