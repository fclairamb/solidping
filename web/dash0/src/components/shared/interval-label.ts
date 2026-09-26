/**
 * translateIntervalLabel renders one of buildIntervalOptions' English labels
 * ("5 minutes", "1 week") in the UI language via checks:intervalUnits.*, so the
 * period select is not English in every locale. An unrecognized label is
 * returned unchanged.
 */
export function translateIntervalLabel(
  label: string,
  t: (key: string, options: { count: number }) => string,
): string {
  const match = /^(\d+) (second|minute|hour|day|week)s?$/.exec(label);
  if (!match) return label;
  return t(`intervalUnits.${match[2]}`, { count: Number(match[1]) });
}
