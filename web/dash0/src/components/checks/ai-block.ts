import type { AIRepairMode } from "@/api/hooks";

export const REPAIR_MODES: AIRepairMode[] = ["propose", "auto", "off"];

/**
 * The editable `ai` block of a js check (spec 2026-10-03-07). The contract is
 * one assertion per line, as on the Describe it page.
 */
export interface AIBlockState {
  prompt: string;
  contract: string;
  repair: AIRepairMode;
  model?: string;
  generatedAt?: string;
}

/** Seeds the editor from a config `ai` block; undefined when there is none. */
export function aiBlockStateFromConfig(raw: unknown): AIBlockState | undefined {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return undefined;
  const block = raw as Record<string, unknown>;
  const repair = REPAIR_MODES.includes(block.repair as AIRepairMode)
    ? (block.repair as AIRepairMode)
    : "propose";
  return {
    prompt: typeof block.prompt === "string" ? block.prompt : "",
    contract: Array.isArray(block.contract)
      ? block.contract.filter((item) => typeof item === "string").join("\n")
      : "",
    repair,
    model: typeof block.model === "string" ? block.model : undefined,
    generatedAt: typeof block.generated_at === "string" ? block.generated_at : undefined,
  };
}

export function contractLines(contract: string): string[] {
  return contract
    .split("\n")
    .map((line) => line.trim())
    .filter(Boolean);
}

/** Serializes the editor back into the config `ai` block. */
export function aiBlockStateToConfig(state: AIBlockState): Record<string, unknown> {
  return {
    prompt: state.prompt.trim(),
    contract: contractLines(state.contract),
    repair: state.repair,
    ...(state.model && { model: state.model }),
    ...(state.generatedAt && { generated_at: state.generatedAt }),
  };
}
