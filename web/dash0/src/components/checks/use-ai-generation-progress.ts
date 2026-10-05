import { useCallback, useState } from "react";
import type { AIProgressEvent } from "@/api/hooks";

// State of one streamed script generation, rendered by AIGenerationProgress.

export type AIGenerationStep =
  | { kind: "message"; text: string }
  | {
      kind: "tool";
      tool: string;
      url?: string;
      final?: boolean;
      status?: string;
      detail?: string;
      error?: string;
      durationMs?: number;
      done: boolean;
    };

export interface AIGenerationState {
  steps: AIGenerationStep[];
  turn: number;
  maxTurns: number;
  startedAt: number | null;
  lastEventAt: number | null;
}

const EMPTY: AIGenerationState = { steps: [], turn: 0, maxTurns: 0, startedAt: null, lastEventAt: null };

/** Reduces the progress events of one generation into displayable steps. */
export function useAIGenerationProgress() {
  const [state, setState] = useState<AIGenerationState>(EMPTY);

  const start = useCallback(() => {
    const now = Date.now();
    setState({ ...EMPTY, startedAt: now, lastEventAt: now });
  }, []);

  const onProgress = useCallback((event: AIProgressEvent) => {
    setState((prev) => {
      const next = { ...prev, lastEventAt: Date.now() };
      switch (event.type) {
        case "turn":
          return { ...next, turn: event.turn ?? prev.turn, maxTurns: event.maxTurns ?? prev.maxTurns };
        case "message":
          return event.text ? { ...next, steps: [...prev.steps, { kind: "message", text: event.text }] } : next;
        case "tool":
          return {
            ...next,
            steps: [
              ...prev.steps,
              { kind: "tool", tool: event.tool ?? "", url: event.url, final: event.final, done: false },
            ],
          };
        case "toolResult": {
          const steps = [...prev.steps];
          for (let i = steps.length - 1; i >= 0; i--) {
            const step = steps[i];
            if (step.kind === "tool" && !step.done && step.tool === event.tool) {
              steps[i] = {
                ...step,
                status: event.status,
                detail: event.detail,
                error: event.error,
                durationMs: event.durationMs,
                done: true,
              };
              break;
            }
          }
          return { ...next, steps };
        }
        default:
          return next;
      }
    });
  }, []);

  return { state, start, onProgress };
}
