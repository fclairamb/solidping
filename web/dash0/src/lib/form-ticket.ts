/**
 * Margin added to the server's minimum ticket age before a submission is
 * allowed out, so clock granularity never makes a ticket look too young.
 */
export const FORM_TICKET_MARGIN_MS = 250;

/**
 * How long a submission must still wait for its form ticket to be old enough
 * (auth.form_guard), given the ticket's `minAgeMs` and the moment the browser
 * received it. 0 when it can go now.
 *
 * Only the password-reset form waits: it is often submitted at once, with the
 * email already filled in from the login page, and it only ever emails an
 * existing account. The sign-up form does not wait: no human fills a name, an
 * email and a password in 3 s, so the server's check stays meaningful there.
 */
export function formTicketDelayMs(minAgeMs: number, fetchedAt: number, now: number): number {
  return Math.max(0, minAgeMs + FORM_TICKET_MARGIN_MS - (now - fetchedAt));
}

/** Resolves once {@link formTicketDelayMs} has elapsed. */
export function waitForFormTicket(minAgeMs: number, fetchedAt: number): Promise<void> {
  const delay = formTicketDelayMs(minAgeMs, fetchedAt, Date.now());
  if (delay === 0) return Promise.resolve();

  return new Promise((resolve) => setTimeout(resolve, delay));
}
