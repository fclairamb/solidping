// Shake-to-report (spec 2026-05-03-50): a few strong jolts in a short window
// opens the bug report dialog on a phone. A single bump (phone put down on a
// table) is one jolt and does not count.

export interface MotionSample {
  x: number;
  y: number;
  z: number;
  // True when the sample still includes gravity (accelerationIncludingGravity,
  // the fallback on devices that do not report `acceleration`).
  withGravity: boolean;
}

const GRAVITY = 9.81;
// m/s² above gravity a sample must reach to count as a jolt.
const JOLT_THRESHOLD = 15;
// A shake is this many jolts within JOLT_WINDOW_MS.
const JOLTS_PER_SHAKE = 3;
const JOLT_WINDOW_MS = 1000;
// No second shake is reported for this long after one.
const DEBOUNCE_MS = 2000;

// createShakeDetector returns a function fed with every motion sample. It
// answers true once per shake. A jolt is counted when the acceleration rises
// above the threshold, so the several samples of one stroke count once.
export function createShakeDetector(): (sample: MotionSample, now: number) => boolean {
  let jolts: number[] = [];
  let above = false;
  let lastShake = -Infinity;

  return (sample, now) => {
    let magnitude = Math.hypot(sample.x, sample.y, sample.z);
    if (sample.withGravity) magnitude = Math.abs(magnitude - GRAVITY);

    const isAbove = magnitude > JOLT_THRESHOLD;
    const rising = isAbove && !above;
    above = isAbove;
    if (!rising) return false;

    jolts = jolts.filter((t) => now - t < JOLT_WINDOW_MS);
    jolts.push(now);
    if (jolts.length < JOLTS_PER_SHAKE || now - lastShake < DEBOUNCE_MS) return false;

    jolts = [];
    lastShake = now;
    return true;
  };
}

function sampleOf(event: DeviceMotionEvent): MotionSample | null {
  const a = event.acceleration;
  if (a && a.x !== null && a.y !== null && a.z !== null) {
    return { x: a.x, y: a.y, z: a.z, withGravity: false };
  }
  const g = event.accelerationIncludingGravity;
  if (g && g.x !== null && g.y !== null && g.z !== null) {
    return { x: g.x, y: g.y, z: g.z, withGravity: true };
  }
  return null;
}

type MotionPermission = () => Promise<"granted" | "denied">;

// listenForShake calls onShake on every shake and returns the cleanup. iOS
// Safari only sends motion events after DeviceMotionEvent.requestPermission(),
// which must run from a user gesture: it is asked on the first tap, and a
// refusal is silent (the bug icon still works).
export function listenForShake(onShake: () => void): () => void {
  if (typeof window === "undefined" || !("DeviceMotionEvent" in window)) return () => {};
  // Phones and tablets only: a desktop has no motion to read, and must never
  // see a motion permission prompt.
  if (!window.matchMedia?.("(pointer: coarse)").matches) return () => {};

  const detect = createShakeDetector();
  function onMotion(event: DeviceMotionEvent) {
    const sample = sampleOf(event);
    if (sample && detect(sample, event.timeStamp)) onShake();
  }

  let listening = false;
  let disposed = false;
  function start() {
    if (disposed || listening) return;
    listening = true;
    window.addEventListener("devicemotion", onMotion);
  }

  const requestPermission = (
    window.DeviceMotionEvent as unknown as { requestPermission?: MotionPermission }
  ).requestPermission;

  function onFirstTap() {
    requestPermission!()
      .then((state) => {
        if (state === "granted") start();
      })
      .catch(() => {});
  }

  if (typeof requestPermission === "function") {
    window.addEventListener("pointerdown", onFirstTap, { once: true });
  } else {
    start();
  }

  return () => {
    disposed = true;
    window.removeEventListener("pointerdown", onFirstTap);
    window.removeEventListener("devicemotion", onMotion);
  };
}
