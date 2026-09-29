export function cx(...c: (string | false | null | undefined)[]): string {
  return c.filter(Boolean).join(' ')
}

/** Shared text-input styling. */
export const inputClass =
  'h-10 rounded-xl bg-surface-2 px-3 text-fg font-medium shadow-[inset_0_1px_2px_rgba(76,29,149,0.06)] ring-1 ring-inset ring-line-strong transition-all duration-150 placeholder:text-muted/60 hover:bg-surface hover:ring-accent/50 focus:outline-none focus:bg-surface focus:ring-2 focus:ring-accent focus:shadow-[0_0_0_3px_rgba(109,40,217,0.15)] aria-[invalid=true]:ring-bad-line'
