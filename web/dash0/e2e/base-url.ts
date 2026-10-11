/**
 * The dashboard URL this Playwright process targets.
 *
 * `E2E_BASE_URLS` (comma-separated) gives every worker its OWN server: worker N
 * (its TEST_PARALLEL_INDEX, stable across worker restarts) uses entry N. Each
 * of those servers has its own database, so workers share no state, exactly
 * like CI's shards do. That is what makes more than one worker safe: the
 * suite shares the seeded `test` org and several specs mutate it, so two
 * workers against ONE server would interfere.
 *
 * Without it, `E2E_BASE_URL` (one server, e.g. a local side-car) or the CI
 * default. Every value must end in `/d/`.
 */
export function e2eBaseUrls(): string[] {
  const list = (process.env.E2E_BASE_URLS ?? "")
    .split(",")
    .map((url) => url.trim())
    .filter(Boolean);

  if (list.length > 0) {
    return list;
  }

  return [process.env.E2E_BASE_URL ?? "http://localhost:4000/d/"];
}

export function e2eBaseUrl(): string {
  const urls = e2eBaseUrls();
  const index = Number(process.env.TEST_PARALLEL_INDEX ?? "0");

  return urls[(Number.isFinite(index) ? index : 0) % urls.length];
}
