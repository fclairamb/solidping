/**
 * @vitest-environment jsdom
 *
 * Spec 2026-09-25-13 follow-up. error-boundary.test.tsx mocks `@/lib/analytics`
 * and stubs `console.error`, so it never exercises the real ordering bug: with
 * no `onCaughtError`, React's OWN `defaultOnCaughtError` calls
 * `console.error(error)` for every error a class boundary catches — and it
 * runs BEFORE `componentDidCatch`. So the autocaptured console.error fires
 * with no signature registered yet (analytics.ts's
 * `dedupeAutocapturedBoundaryExceptions` has nothing to match it against yet)
 * and ships as an extra, unlabeled `$exception` alongside the explicit
 * `captureException()` call `componentDidCatch` makes right after — two
 * events per crash instead of one.
 *
 * This file exercises the real sequence end to end:
 * - the real `analytics.ts` (`captureException`, `initAnalytics`, and the
 *   `before_send` array it composes — captured from the real `posthog.init`
 *   call, not re-listed here, so a reordering or a dropped hook would show up)
 * - the real `analytics-redaction.ts` (`redactCredentialsBeforeSend`) inside
 *   that chain
 * - the real `ErrorBoundary` (`componentDidCatch`)
 * - the real `reactRootErrorOptions` main.tsx installs on `createRoot`
 * - a faithful stand-in for posthog-js's `console.error` wrapper, built from
 *   reading node_modules/posthog-js/dist/exception-autocapture.js's
 *   `wrapConsoleError` (1.409.5): it joins multiple console args, or — when
 *   one of them is an `Error` instance, exactly our componentDidCatch /
 *   RouteErrorFallback shape (`console.error("...", error, ...)`) — reports
 *   THAT Error object instead of the joined text, with
 *   `mechanism: { handled: false }`.
 */
import { act } from "react";
import { createRoot } from "react-dom/client";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterContextProvider,
} from "@tanstack/react-router";

// This file drives `react-dom/client` directly (not through
// @testing-library/react, which sets this for you) so it can pass the exact
// `createRoot` options main.tsx uses. Without it, React's `act()` itself logs
// "The current testing environment is not configured to support act(...)"
// via console.error — which our console-error autocapture stand-in would
// then (correctly) report as an unrelated $exception, poisoning every count
// below.
declare global {
  var IS_REACT_ACT_ENVIRONMENT: boolean | undefined;
}
globalThis.IS_REACT_ACT_ENVIRONMENT = true;
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import "@/i18n";
import { ErrorBoundary, RouteErrorFallback } from "./error-boundary";
import { reactRootErrorOptions } from "@/lib/react-root-error-handling";
import { captureException, initAnalytics, __resetAnalyticsForTests } from "@/lib/analytics";

interface ExceptionProps {
  type: string;
  value: string;
  mechanism: { handled: boolean; type: string; synthetic: boolean };
}

interface ExceptionEvent {
  event: string;
  properties: Record<string, unknown> & { $exception_list: ExceptionProps[] };
}

/**
 * Faithful stand-in for posthog-js's `wrapConsoleError` (see the module
 * comment). Reports the Error instance among the console.error arguments
 * when there is one — discarding every other argument, joined text included —
 * exactly like the real wrapper does.
 */
function installConsoleErrorAutocapture(report: (props: ExceptionProps) => void): () => void {
  const original = console.error;
  console.error = (...args: unknown[]) => {
    const joined = args.length === 1 ? args[0] : args.map(String).join(" ");
    const errorArg = args.find((a): a is Error => a instanceof Error);
    const source: unknown = errorArg ?? joined;
    report({
      type: source instanceof Error ? source.name : "Error",
      value: source instanceof Error ? source.message : String(source),
      mechanism: { handled: false, type: "generic", synthetic: false },
    });
    original(...args);
  };
  return () => {
    console.error = original;
  };
}

/** Faithful stand-in for posthog-js's own `captureException` (mechanism.handled: true). */
function fakePostHogCaptureException(
  deliver: (event: ExceptionEvent) => void,
): (error: unknown, additionalProperties?: Record<string, unknown>) => void {
  return (error, additionalProperties) => {
    deliver({
      event: "$exception",
      properties: {
        $exception_list: [
          {
            type: error instanceof Error ? error.name : "Error",
            value: error instanceof Error ? error.message : String(error),
            mechanism: { handled: true, type: "generic", synthetic: false },
          },
        ],
        $exception_level: "error",
        ...additionalProperties,
      },
    });
  };
}

describe("ErrorBoundary crash -> $exception, real ordering", () => {
  let container: HTMLDivElement;
  let root: ReturnType<typeof createRoot> | null = null;
  let restoreConsoleError: (() => void) | null = null;
  let delivered: ExceptionEvent[];
  let beforeSendChain: Array<(e: ExceptionEvent | null) => ExceptionEvent | null>;

  function deliver(event: ExceptionEvent) {
    const sent = beforeSendChain.reduce<ExceptionEvent | null>((e, fn) => fn(e), event);
    if (sent) delivered.push(sent);
  }

  /** Wires the real analytics.ts up to our fake PostHog delivery pipeline. */
  async function setUpAnalytics() {
    delivered = [];
    vi.doMock("@/lib/posthog-loader", () => ({
      default: {
        init: (_key: string, opts: Record<string, unknown>) => {
          beforeSendChain = opts.before_send as typeof beforeSendChain;
        },
        identify: () => {},
        reset: () => {},
        capture: () => {},
        captureException: fakePostHogCaptureException(deliver),
      },
    }));
    expect(await initAnalytics({ posthog: { enabled: true, projectApiKey: "phc_k" } })).toBe(true);
    vi.doUnmock("@/lib/posthog-loader");
    restoreConsoleError = installConsoleErrorAutocapture((props) =>
      deliver({ event: "$exception", properties: { $exception_list: [props], $exception_level: "error" } }),
    );
  }

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    __resetAnalyticsForTests();
    // componentDidCatch's OWN console.error call (for the bug-report ring
    // buffer) still runs and, through our stand-in, is correctly deduped —
    // no need to silence it, but keep test output clean by letting the
    // installed wrapper's passthrough to `original` be a no-op logger.
    vi.spyOn(console, "error").mockImplementation(() => {});
  });

  afterEach(() => {
    if (root) {
      act(() => root!.unmount());
      root = null;
    }
    container.remove();
    restoreConsoleError?.();
    restoreConsoleError = null;
    vi.restoreAllMocks();
    __resetAnalyticsForTests();
  });

  it("delivers exactly one $exception, the explicit one with componentStack, when onCaughtError is silenced", async () => {
    await setUpAnalytics();

    function Boom(): never {
      throw new Error("kaboom");
    }

    root = createRoot(container, reactRootErrorOptions);
    await act(async () => {
      root!.render(
        <ErrorBoundary>
          <Boom />
        </ErrorBoundary>,
      );
    });

    expect(delivered).toHaveLength(1);
    const [event] = delivered;
    const [exception] = event.properties.$exception_list;
    expect(exception.value).toBe("kaboom");
    // Ours: reported explicitly via captureException, with real context —
    // not React's generic, context-free defaultOnCaughtError console.error.
    expect(exception.mechanism.handled).toBe(true);
    expect(event.properties.componentStack).toEqual(expect.stringContaining("Boom"));
  });

  // Positive control: this is what the bug looked like. Without
  // onCaughtError silenced, React's own defaultOnCaughtError console.error
  // races ahead of componentDidCatch (see the module comment) and gets
  // autocaptured as a second, unlabeled event — proving the assertion above
  // is not vacuous, and demonstrating exactly what installing
  // reactRootErrorOptions in main.tsx fixes.
  it("without onCaughtError silenced, the same crash is reported twice", async () => {
    await setUpAnalytics();

    function Boom(): never {
      throw new Error("kaboom");
    }

    root = createRoot(container); // default options — the pre-fix behavior
    await act(async () => {
      root!.render(
        <ErrorBoundary>
          <Boom />
        </ErrorBoundary>,
      );
    });

    expect(delivered).toHaveLength(2);
    const handled = delivered.map((e) => e.properties.$exception_list[0].mechanism.handled);
    expect(handled.sort()).toEqual([false, true]);
    // The unhandled one is React's own defaultOnCaughtError console.error —
    // context-free, unlike the explicit report.
    const unhandledEvent = delivered.find((e) => !e.properties.$exception_list[0].mechanism.handled)!;
    expect(unhandledEvent.properties.componentStack).toBeUndefined();
  });

  it("reports nothing when children render fine", async () => {
    await setUpAnalytics();

    root = createRoot(container, reactRootErrorOptions);
    await act(async () => {
      root!.render(
        <ErrorBoundary>
          <div>all good</div>
        </ErrorBoundary>,
      );
    });

    expect(delivered).toHaveLength(0);
  });
});

// TanStack Router's own CatchBoundaryImpl (CatchBoundary.tsx) is a plain
// class component with getDerivedStateFromError — its componentDidCatch only
// calls `this.props.onCatch?.(...)`, no console logging of its own — so the
// onCaughtError race is identical to ErrorBoundary's and already fully
// covered, generically, by the block above (onCaughtError is a single
// createRoot-level option that applies to every boundary in the tree, not a
// per-boundary one) plus the "main.tsx wiring" pin below.
//
// What's specific to RouteErrorFallback is its OWN effect's call order
// (captureException before console.error — see error-boundary.tsx). A full
// render through <RouterProvider> to reach it via a REAL thrown route
// component turned out to be unusable here: TanStack Router's initial
// `router.load()` commits matches (and bumps `stores.loadedAt`, which feeds
// CatchBoundaryImpl's `getResetKey`) across more than one microtask/
// transition, so this bare test harness — no SSR, no hydration — observes a
// second commit with a new resetKey shortly after the first, which resets
// and re-triggers CatchBoundaryImpl's catch a second time regardless of
// onCaughtError. That is a router test-harness timing artifact, not a
// product bug (a real page load only goes through this once), so it is not
// asserted on here. Instead, this exercises the real RouteErrorFallback
// component directly — a real React render, the real analytics.ts /
// analytics-redaction.ts before_send chain, the real console-error
// autocapture stand-in — just without TanStack's CatchBoundary in the loop,
// which is not what changed.
describe("RouteErrorFallback effect -> $exception, real ordering", () => {
  let container: HTMLDivElement;
  let root: ReturnType<typeof createRoot> | null = null;
  let restoreConsoleError: (() => void) | null = null;
  let delivered: ExceptionEvent[];
  let beforeSendChain: Array<(e: ExceptionEvent | null) => ExceptionEvent | null>;

  function deliver(event: ExceptionEvent) {
    const sent = beforeSendChain.reduce<ExceptionEvent | null>((e, fn) => fn(e), event);
    if (sent) delivered.push(sent);
  }

  async function setUpAnalytics() {
    delivered = [];
    vi.doMock("@/lib/posthog-loader", () => ({
      default: {
        init: (_key: string, opts: Record<string, unknown>) => {
          beforeSendChain = opts.before_send as typeof beforeSendChain;
        },
        identify: () => {},
        reset: () => {},
        capture: () => {},
        captureException: fakePostHogCaptureException(deliver),
      },
    }));
    expect(await initAnalytics({ posthog: { enabled: true, projectApiKey: "phc_k" } })).toBe(true);
    vi.doUnmock("@/lib/posthog-loader");
    restoreConsoleError = installConsoleErrorAutocapture((props) =>
      deliver({ event: "$exception", properties: { $exception_list: [props], $exception_level: "error" } }),
    );
  }

  // A real router instance, loaded, purely so useRouter()/router.state.matches
  // inside RouteErrorFallback has something real to read — no CatchBoundary,
  // no <Matches>, nothing route-component-rendering involved.
  async function buildLoadedRouterContext() {
    const rootRoute = createRootRoute({ component: () => null });
    const indexRoute = createRoute({ getParentRoute: () => rootRoute, path: "/", component: () => null });
    const router = createRouter({
      routeTree: rootRoute.addChildren([indexRoute]),
      history: createMemoryHistory({ initialEntries: ["/"] }),
    });
    await router.load();
    return router;
  }

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    __resetAnalyticsForTests();
    vi.spyOn(console, "error").mockImplementation(() => {});
  });

  afterEach(() => {
    if (root) {
      act(() => root!.unmount());
      root = null;
    }
    container.remove();
    restoreConsoleError?.();
    restoreConsoleError = null;
    vi.restoreAllMocks();
    __resetAnalyticsForTests();
  });

  it("delivers exactly one $exception, the explicit one with routeId", async () => {
    await setUpAnalytics();
    const router = await buildLoadedRouterContext();
    const error = new Error("route kaboom");

    root = createRoot(container);
    await act(async () => {
      root!.render(
        <RouterContextProvider router={router}>
          <RouteErrorFallback error={error} reset={() => {}} />
        </RouterContextProvider>,
      );
    });

    expect(delivered).toHaveLength(1);
    const [event] = delivered;
    const [exception] = event.properties.$exception_list;
    expect(exception.value).toBe("route kaboom");
    // Ours: reported explicitly via captureException, with real route context.
    expect(exception.mechanism.handled).toBe(true);
    expect(event.properties.routeId).toBeTruthy();
  });

  // Positive control: prove the single-event result above depends on the
  // real ordering (captureException before console.error), by rendering a
  // component that fires the SAME two calls in the OPPOSITE order. The
  // autocaptured console.error then races ahead of the signature being
  // registered — exactly the bug this whole file guards against — and
  // dedupeAutocapturedBoundaryExceptions has nothing to match it against.
  it("the opposite call order (console.error before captureException) reports twice", async () => {
    await setUpAnalytics();
    const error = new Error("route kaboom");

    function ReversedOrder() {
      console.error("Route error boundary caught an error:", error);
      captureException(error, { routeId: "/broken-order" });
      return null;
    }

    root = createRoot(container);
    await act(async () => {
      root!.render(<ReversedOrder />);
    });

    expect(delivered).toHaveLength(2);
    const handled = delivered.map((e) => e.properties.$exception_list[0].mechanism.handled);
    expect(handled.sort()).toEqual([false, true]);
  });
});

// Guards against a regression at the wiring level: the dedupe correctness
// above only holds if main.tsx actually installs reactRootErrorOptions on
// its createRoot call. main.tsx itself is not imported here (it mounts to a
// real #root element, reads window.location, registers a service worker,
// etc. — the wrong shape for a unit test), so this pins its source text
// instead, the same way sidebar.test.tsx pins a class-name contract.
describe("main.tsx wiring", () => {
  it("passes reactRootErrorOptions from lib/react-root-error-handling to createRoot", () => {
    const source = readFileSync(join(__dirname, "..", "..", "main.tsx"), "utf8");
    expect(source).toContain('from "@/lib/react-root-error-handling"');
    expect(source).toMatch(/createRoot\(\s*container\s*,\s*reactRootErrorOptions\s*\)/);
  });
});
