/**
 * Ambient declarations for the bits of `docusaurus-theme-openapi-docs@5.0.2`
 * our swizzles import.
 *
 * `@docusaurus/module-type-aliases` only declares `@theme/*` for the classic
 * theme's own components, so a swizzle of this theme has no types otherwise.
 * Re-check these signatures on a theme upgrade.
 */

declare module "@theme/ApiItem/hooks" {
  export function useTypedDispatch(): (action: unknown) => unknown;
  export function useTypedSelector<T>(selector: (state: never) => T): T;
}
