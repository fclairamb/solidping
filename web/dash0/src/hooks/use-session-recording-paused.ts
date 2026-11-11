import { useEffect } from "react";
import { pauseSessionRecording, resumeSessionRecording } from "@/lib/analytics";

/**
 * Pauses PostHog session replay while `active` is true (a dialog that shows
 * a secret is open), and resumes it when `active` turns false or the
 * component unmounts. A no-op when analytics is off. Defence in depth behind
 * `SecretText` (spec 2026-11-11-05).
 */
export function useSessionRecordingPaused(active: boolean): void {
  useEffect(() => {
    if (!active) return;
    pauseSessionRecording();
    return () => resumeSessionRecording();
  }, [active]);
}
