/**
 * The ONLY module in the app that imports `posthog-js`.
 *
 * It exists so the bundler emits the analytics code as a separate chunk with a
 * recognizable `posthog-loader-<hash>.js` filename (importing `posthog-js`
 * directly would produce an opaque `module-<hash>.js`, which nothing could
 * assert on). `src/lib/analytics.ts` reaches it exclusively through a dynamic
 * `import()` that only runs after the server reports PostHog as enabled, so on
 * an unconfigured deployment this chunk is never requested.
 *
 * Never import this module statically from anywhere.
 */
import posthog from "posthog-js";

export default posthog;
