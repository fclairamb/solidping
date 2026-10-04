import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  AlertCircle,
  CheckCircle2,
  ChevronDown,
  FileCode,
  Globe,
  Loader2,
  MessageSquare,
  MonitorSmartphone,
  XCircle,
} from "lucide-react";
import type { AIGenerationFailed } from "@/api/hooks";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import type { AIGenerationState, AIGenerationStep } from "./use-ai-generation-progress";

// Live view of a script generation (POST /checks/ai/generate streamed as
// NDJSON): what the AI says, each page it opens, each script it tests. A
// generation takes from 20 s to a few minutes, mostly spent waiting on the
// model, so the user always sees where it stands.

function useNow(running: boolean): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!running) return;
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [running]);
  return now;
}

function formatElapsed(ms: number): string {
  const total = Math.max(0, Math.floor(ms / 1000));
  return `${Math.floor(total / 60)}:${String(total % 60).padStart(2, "0")}`;
}

/** After this long without an event, say the model is just slow. */
const SLOW_AFTER_MS = 20_000;

interface AIGenerationProgressProps {
  state: AIGenerationState;
  running: boolean;
}

export function AIGenerationProgress({ state, running }: AIGenerationProgressProps) {
  const { t } = useTranslation("checks");
  const now = useNow(running);
  const listRef = useRef<HTMLOListElement>(null);

  useEffect(() => {
    const list = listRef.current;
    if (list) list.scrollTop = list.scrollHeight;
  }, [state.steps.length, running]);

  if (!state.startedAt) return null;

  const pendingTool = state.steps.some((step) => step.kind === "tool" && !step.done);
  const slow = running && state.lastEventAt !== null && now - state.lastEventAt > SLOW_AFTER_MS;

  return (
    <div className="space-y-3 rounded-md border bg-muted/30 p-3" data-testid="ai-progress">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm">
        {running && <Loader2 className="h-4 w-4 animate-spin text-primary" />}
        <span className="font-medium">{running ? t("ai.progress.running") : t("ai.progress.finished")}</span>
        {running && (
          <span className="tabular-nums text-muted-foreground" data-testid="ai-progress-elapsed">
            {formatElapsed(now - state.startedAt)}
          </span>
        )}
        {state.turn > 0 && (
          <span className="text-muted-foreground" data-testid="ai-progress-turn">
            {t("ai.progress.turn", { turn: state.turn, max: state.maxTurns })}
          </span>
        )}
      </div>

      <ol ref={listRef} className="max-h-80 space-y-2 overflow-y-auto text-sm" aria-live="polite">
        {state.steps.map((step, i) => (
          <StepRow key={i} step={step} />
        ))}
        {running && !pendingTool && (
          <li className="flex items-start gap-2 text-muted-foreground" data-testid="ai-progress-thinking">
            <Loader2 className="mt-0.5 h-4 w-4 shrink-0 animate-spin" />
            <span>
              {state.turn === 0 ? t("ai.progress.starting") : t("ai.progress.thinking")}
              {slow && <span className="block text-xs">{t("ai.progress.slowHint")}</span>}
            </span>
          </li>
        )}
      </ol>
    </div>
  );
}

function StepRow({ step }: { step: AIGenerationStep }) {
  const { t } = useTranslation("checks");

  if (step.kind === "message") {
    return (
      <li className="flex items-start gap-2 text-muted-foreground">
        <MessageSquare className="mt-0.5 h-4 w-4 shrink-0" />
        <span className="whitespace-pre-wrap break-words italic">{step.text}</span>
      </li>
    );
  }

  const Icon = step.tool === "fetch_page" ? Globe : step.tool === "browser_snapshot" ? MonitorSmartphone : FileCode;
  const label =
    step.tool === "fetch_page"
      ? t("ai.progress.fetch", { url: step.url })
      : step.tool === "browser_snapshot"
        ? t("ai.progress.snapshot", { url: step.url })
        : step.final
          ? t("ai.progress.final")
          : t("ai.progress.probe");
  const ok = step.done && !step.error && step.status === "up";
  const failed = step.done && !ok;

  return (
    <li className="flex items-start gap-2" data-testid="ai-progress-step">
      {!step.done ? (
        <Loader2 className="mt-0.5 h-4 w-4 shrink-0 animate-spin text-primary" />
      ) : ok ? (
        <CheckCircle2 className="mt-0.5 h-4 w-4 shrink-0 text-status-ok" />
      ) : (
        <XCircle className="mt-0.5 h-4 w-4 shrink-0 text-status-error" />
      )}
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-2">
          <Icon className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
          <span className="break-all">{label}</span>
          {step.done && step.status && (
            <Badge variant={ok ? "success" : "destructive"}>{step.status}</Badge>
          )}
          {step.done && step.durationMs !== undefined && (
            <span className="text-xs tabular-nums text-muted-foreground">{step.durationMs} ms</span>
          )}
        </div>
        {(step.error || (failed && step.detail) || (ok && step.detail && step.tool !== "run_script")) && (
          <p className="break-words text-xs text-muted-foreground">{step.error ?? step.detail}</p>
        )}
      </div>
    </li>
  );
}

/** What a generation that produced no passing script left behind. */
export function AIGenerationFailure({ failure }: { failure: AIGenerationFailed }) {
  const { t } = useTranslation("checks");
  const [scriptOpen, setScriptOpen] = useState(false);

  return (
    <div className="space-y-3" data-testid="ai-generation-failure">
      <Alert variant="destructive">
        <AlertCircle className="h-4 w-4" />
        <AlertTitle>{t("ai.failure.title")}</AlertTitle>
        {failure.detail && <AlertDescription className="break-words">{failure.detail}</AlertDescription>}
      </Alert>

      {failure.explanation && (
        <div className="space-y-1">
          <p className="text-sm font-medium">{t("ai.failure.explanation")}</p>
          <p
            className="whitespace-pre-wrap break-words rounded-md border-l-2 border-muted-foreground/40 pl-3 text-sm text-muted-foreground"
            data-testid="ai-failure-explanation"
          >
            {failure.explanation}
          </p>
        </div>
      )}

      {failure.lastRun && (
        <div className="space-y-1">
          <p className="flex items-center gap-2 text-sm font-medium">
            {t("ai.failure.lastRun")}
            <Badge variant={failure.lastRun.status === "up" ? "success" : "destructive"}>
              {failure.lastRun.status}
            </Badge>
          </p>
          {failure.lastRun.output && (
            <pre className="max-h-48 overflow-auto rounded-md bg-muted p-3 text-xs" data-testid="ai-failure-output">
              {JSON.stringify(failure.lastRun.output, null, 2)}
            </pre>
          )}
        </div>
      )}

      {failure.lastScript && (
        <Collapsible open={scriptOpen} onOpenChange={setScriptOpen}>
          <CollapsibleTrigger asChild>
            <Button variant="ghost" size="sm" className="-ml-2" data-testid="ai-failure-script-toggle">
              <ChevronDown className={`mr-1 h-4 w-4 transition-transform ${scriptOpen ? "rotate-180" : ""}`} />
              {t("ai.failure.lastScript")}
            </Button>
          </CollapsibleTrigger>
          <CollapsibleContent>
            <pre className="max-h-96 overflow-auto rounded-md bg-muted p-3 text-xs" data-testid="ai-failure-script">
              {failure.lastScript}
            </pre>
          </CollapsibleContent>
        </Collapsible>
      )}
    </div>
  );
}
