import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Loader2, Sparkles } from "lucide-react";
import { AIGenerationFailedError, ApiError } from "@/api/client";
import {
  useAIGenerate,
  type AIGenerateResponse,
  type AIGenerationFailed,
  type AIRepairMode,
} from "@/api/hooks";
import { useAIChecksEnabled } from "@/api/public-config";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { TimeAgo } from "@/components/ui/time-ago";
import { REPAIR_MODES, aiBlockStateFromConfig, contractLines, type AIBlockState } from "./ai-block";
import { AIGenerationFailure, AIGenerationProgress } from "./ai-generation-progress";
import { useAIGenerationProgress } from "./use-ai-generation-progress";

function errorMessage(err: unknown): string {
  if (err instanceof ApiError) {
    return err.detail ? `${err.message}: ${err.detail}` : err.message;
  }
  return err instanceof Error ? err.message : String(err);
}

interface AIBlockEditorProps {
  org: string;
  /** Set when editing a saved check: the server fills its stored secrets. */
  checkUid?: string;
  value: AIBlockState;
  onChange: (value: AIBlockState) => void;
  env: Record<string, string>;
  /** Only the secrets the user typed in this form (dirty), else undefined. */
  typedSecrets?: Record<string, string>;
  /** A regenerated script that passed its test run, with its new `ai` block. */
  onRegenerated: (script: string, ai: AIBlockState) => void;
}

/**
 * Shows what an AI-authored js check was written from, on its edit form. The
 * prompt and contract stay editable, and "Regenerate" writes and tests a new
 * script from them. Nothing is saved until the form is.
 */
export function AIBlockEditor({
  org,
  checkUid,
  value,
  onChange,
  env,
  typedSecrets,
  onRegenerated,
}: AIBlockEditorProps) {
  const { t } = useTranslation("checks");
  const aiEnabled = useAIChecksEnabled();
  const generate = useAIGenerate(org);
  const [result, setResult] = useState<AIGenerateResponse | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [failure, setFailure] = useState<AIGenerationFailed | null>(null);
  const progress = useAIGenerationProgress();

  const canRegenerate = value.prompt.trim() !== "" && contractLines(value.contract).length > 0;

  async function regenerate() {
    setError(null);
    setFailure(null);
    setResult(null);
    progress.start();
    try {
      const resp = await generate.mutateAsync({
        prompt: value.prompt,
        contract: contractLines(value.contract),
        env,
        repair: value.repair,
        // Typed secrets replace the stored ones (that is what saving them
        // does), so they run alone. Untouched, the server reads the stored ones.
        ...(typedSecrets ? { secrets: typedSecrets } : checkUid ? { checkUid } : {}),
        onProgress: progress.onProgress,
      });
      setResult(resp);
      onRegenerated(resp.script, aiBlockStateFromConfig(resp.config.ai) ?? value);
    } catch (err) {
      if (err instanceof AIGenerationFailedError) {
        setFailure(err.failure);
      } else {
        setError(errorMessage(err));
      }
    }
  }

  return (
    <div className="space-y-4 rounded-md border border-primary/40 bg-primary/5 p-4" data-testid="ai-block-editor">
      <div className="flex flex-wrap items-center gap-2">
        <Sparkles className="h-4 w-4 text-primary" />
        <span className="text-sm font-medium">{t("ai.detailTitle")}</span>
        {value.model && (
          <span className="text-xs text-muted-foreground">
            {t("ai.detailModel", { model: value.model })}
            {value.generatedAt && (
              <>
                {" · "}
                <TimeAgo date={value.generatedAt} />
              </>
            )}
          </span>
        )}
      </div>

      <div className="space-y-2">
        <Label htmlFor="ai-edit-prompt">{t("ai.detailPrompt")}</Label>
        <Textarea
          id="ai-edit-prompt"
          rows={3}
          value={value.prompt}
          onChange={(e) => onChange({ ...value, prompt: e.target.value })}
          data-testid="ai-edit-prompt"
        />
      </div>

      <div className="space-y-2">
        <Label htmlFor="ai-edit-contract">{t("ai.detailContract")}</Label>
        <Textarea
          id="ai-edit-contract"
          rows={Math.max(3, contractLines(value.contract).length + 1)}
          value={value.contract}
          onChange={(e) => onChange({ ...value, contract: e.target.value })}
          data-testid="ai-edit-contract"
        />
        <p className="text-xs text-muted-foreground">{t("ai.contractHelp")}</p>
      </div>

      <div className="space-y-2">
        <Label htmlFor="ai-edit-repair">{t("ai.repairLabel")}</Label>
        <Select value={value.repair} onValueChange={(mode) => onChange({ ...value, repair: mode as AIRepairMode })}>
          <SelectTrigger id="ai-edit-repair" className="w-full sm:w-72" data-testid="ai-edit-repair">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {REPAIR_MODES.map((mode) => (
              <SelectItem key={mode} value={mode}>
                {t(`ai.repair.${mode}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>

      {aiEnabled && (
        <div className="space-y-2">
          <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
            <Button
              type="button"
              variant="outline"
              onClick={() => void regenerate()}
              disabled={!canRegenerate || generate.isPending}
              data-testid="ai-regenerate"
            >
              {generate.isPending ? (
                <Loader2 className="mr-2 h-4 w-4 animate-spin" />
              ) : (
                <Sparkles className="mr-2 h-4 w-4" />
              )}
              {generate.isPending ? t("ai.generating") : t("ai.regenerate")}
            </Button>
            {result && (
              <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground" data-testid="ai-regenerated">
                <Badge variant={result.lastRun.status === "up" ? "success" : "destructive"}>
                  {t("ai.lastRun", { status: result.lastRun.status })}
                </Badge>
                {t("ai.writtenBy", { model: result.model, turns: result.turns })}
              </div>
            )}
          </div>
          <p className="text-xs text-muted-foreground">
            {result ? t("ai.regeneratedHelp") : t("ai.regenerateHelp")}
          </p>
          <AIGenerationProgress state={progress.state} running={generate.isPending} />
          {failure && <AIGenerationFailure failure={failure} />}
        </div>
      )}

      {error && (
        <Alert variant="destructive" data-testid="ai-regenerate-error">
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}
    </div>
  );
}
