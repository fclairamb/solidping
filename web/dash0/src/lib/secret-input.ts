/** The secret input is editable while editing, or when nothing is stored yet. */
export function secretInputVisible(editing: boolean, stored: boolean): boolean {
  return editing || !stored;
}

/**
 * A secret is sent when its input is visible and non-empty. An empty value on
 * first setup is never sent (it would store an empty secret). The value is
 * never trimmed.
 */
export function shouldSendSecret(
  editing: boolean,
  stored: boolean,
  value: string,
): boolean {
  return secretInputVisible(editing, stored) && value.length > 0;
}
