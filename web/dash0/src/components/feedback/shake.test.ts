/**
 * @vitest-environment jsdom
 *
 * Shake-to-report (fclairamb/solidping-business#7): shaking a phone opens the
 * bug report dialog, as spec 2026-05-03-50 asked.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";

vi.mock("html-to-image", () => ({ toBlob: vi.fn(async () => null) }));

import { createShakeDetector, listenForShake, type MotionSample } from "./shake";
import { useFeedback } from "./useFeedback";

const still: MotionSample = { x: 0, y: 0, z: 0, withGravity: false };
const jolt: MotionSample = { x: 20, y: 0, z: 0, withGravity: false };

// shake feeds `strokes` back-and-forth strokes, 100ms apart, starting at t0.
function shake(detect: ReturnType<typeof createShakeDetector>, t0: number, strokes = 3) {
  let fired = 0;
  for (let i = 0; i < strokes; i++) {
    const t = t0 + i * 100;
    if (detect(jolt, t)) fired++;
    if (detect(jolt, t + 16)) fired++; // same stroke, still above threshold
    if (detect(still, t + 50)) fired++;
  }
  return fired;
}

describe("createShakeDetector", () => {
  it("reports one shake for three strokes", () => {
    expect(shake(createShakeDetector(), 0)).toBe(1);
  });

  it("ignores a single bump", () => {
    expect(shake(createShakeDetector(), 0, 1)).toBe(0);
  });

  it("ignores jolts spread over more than a second", () => {
    const detect = createShakeDetector();
    expect(shake(detect, 0, 1) + shake(detect, 700, 1) + shake(detect, 1400, 1)).toBe(0);
  });

  it("ignores gravity alone", () => {
    const detect = createShakeDetector();
    const resting: MotionSample = { x: 0, y: 0, z: 9.81, withGravity: true };
    for (let t = 0; t < 1000; t += 16) expect(detect(resting, t)).toBe(false);
  });

  it("waits two seconds before reporting another shake", () => {
    const detect = createShakeDetector();
    expect(shake(detect, 0)).toBe(1);
    expect(shake(detect, 600)).toBe(0);
    expect(shake(detect, 2500)).toBe(1);
  });
});

function setCoarsePointer(coarse: boolean) {
  window.matchMedia = ((query: string) => ({
    matches: coarse && query === "(pointer: coarse)",
  })) as unknown as typeof window.matchMedia;
}

function dispatchShake() {
  for (let i = 0; i < 3; i++) {
    for (const x of [20, 0]) {
      const event = new Event("devicemotion") as Event & Record<string, unknown>;
      event.acceleration = { x, y: 0, z: 0 };
      event.accelerationIncludingGravity = null;
      Object.defineProperty(event, "timeStamp", { value: i * 100 + (x ? 0 : 50) });
      window.dispatchEvent(event);
    }
  }
}

describe("listenForShake", () => {
  beforeEach(() => {
    (window as unknown as Record<string, unknown>).DeviceMotionEvent = class {};
    setCoarsePointer(true);
  });

  afterEach(() => {
    delete (window as unknown as Record<string, unknown>).DeviceMotionEvent;
  });

  it("opens the bug report dialog when the phone is shaken", async () => {
    const { result, unmount } = renderHook(() => useFeedback({ enabled: true }));
    expect(result.current.isOpen).toBe(false);

    act(() => dispatchShake());

    await waitFor(() => expect(result.current.isOpen).toBe(true));
    unmount();
  });

  it("does not listen when bug reports are disabled", () => {
    const { result, unmount } = renderHook(() => useFeedback({ enabled: false }));
    act(() => dispatchShake());
    expect(result.current.isOpen).toBe(false);
    unmount();
  });

  it("does not listen on a desktop", () => {
    setCoarsePointer(false);
    const onShake = vi.fn();
    const stop = listenForShake(onShake);
    dispatchShake();
    expect(onShake).not.toHaveBeenCalled();
    stop();
  });

  it("asks iOS for motion permission on the first tap, then listens", async () => {
    const requestPermission = vi.fn(async () => "granted" as const);
    (window as unknown as Record<string, unknown>).DeviceMotionEvent = Object.assign(
      class {},
      { requestPermission },
    );
    const onShake = vi.fn();
    const stop = listenForShake(onShake);

    dispatchShake();
    expect(onShake).not.toHaveBeenCalled();
    expect(requestPermission).not.toHaveBeenCalled();

    window.dispatchEvent(new Event("pointerdown"));
    expect(requestPermission).toHaveBeenCalledTimes(1);
    await waitFor(() => {
      dispatchShake();
      expect(onShake).toHaveBeenCalled();
    });
    stop();
  });

  it("stays silent when iOS motion permission is refused", async () => {
    const requestPermission = vi.fn(async () => "denied" as const);
    (window as unknown as Record<string, unknown>).DeviceMotionEvent = Object.assign(
      class {},
      { requestPermission },
    );
    const onShake = vi.fn();
    const stop = listenForShake(onShake);

    window.dispatchEvent(new Event("pointerdown"));
    await Promise.resolve();
    await Promise.resolve();
    dispatchShake();
    expect(onShake).not.toHaveBeenCalled();
    stop();
  });
});
