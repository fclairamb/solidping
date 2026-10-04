import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { AlertCircle, History, Sparkles } from "lucide-react";
import { useEvents, type AICheckBlock, type Check } from "@/api/hooks";
import { useAIChecksEnabled, usePublicConfigLoading } from "@/api/public-config";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Label } from "@/components/ui/label";
import { TimeAgo } from "@/components/ui/time-ago";

/** Reads the `ai` block of a js check's config, or undefined. */
export function aiBlockOf(check: Pick<Check, "type" | "config">): AICheckBlock | undefined {
  if (check.type !== "js") return undefined;
  const block = check.config?.ai;
  if (!block || typeof block !== "object") return undefined;
  return block as AICheckBlock;
}

/**
 * The prompt and contract an AI-authored js check was written from (spec
 * 2026-10-03-07). History, diffs and repair proposals live on the History
 * page (spec 2026-10-03-06), linked from here. Renders nothing for any other
 * check.
 */
const REPAIR_OUTCOMES = ["no_candidate", "rejected", "proposed", "applied", "error"];

export function AIAuthoredDetail({ org, check }: { org: string; check: Check }) {
  const { t } = useTranslation("checks");
  const block = aiBlockOf(check);
  const aiEnabled = useAIChecksEnabled();
  const configLoading = usePublicConfigLoading();
  const repair = block?.repair ?? "propose";
  const lastAttempt = useEvents(org, {
    checkUid: check.uid,
    eventType: "check.ai_repair_attempted",
    size: 1,
    enabled: !!block && repair !== "off" && aiEnabled,
    refetchInterval: 60_000,
  });

  if (!block) return null;

  const attempt = lastAttempt.data?.data?.[0];
  const outcome = typeof attempt?.payload?.outcome === "string" ? attempt.payload.outcome : "";
  const reason =
    typeof attempt?.payload?.error === "string"
      ? attempt.payload.error
      : typeof attempt?.payload?.reason === "string"
        ? attempt.payload.reason
        : "";

  // Same panel, header and field order as AIBlockEditor on the edit form.
  return (
    <div
      className="space-y-4 rounded-md border border-primary/40 bg-primary/5 p-4"
      data-testid="ai-authored-detail"
    >
      <div className="flex flex-wrap items-center gap-2">
        <Sparkles className="h-4 w-4 text-primary" />
        <span className="text-sm font-medium">{t("ai.detailTitle")}</span>
        {block.model && (
          <span className="text-xs text-muted-foreground">
            {t("ai.detailModel", { model: block.model })}
            {block.generated_at && (
              <>
                {" · "}
                <TimeAgo date={block.generated_at} />
              </>
            )}
          </span>
        )}
      </div>

      {block.prompt && (
        <div className="space-y-2">
          <Label>{t("ai.detailPrompt")}</Label>
          <p
            className="whitespace-pre-wrap break-words rounded-md border border-input bg-control px-3 py-2 text-sm"
            data-testid="ai-detail-prompt"
          >
            {block.prompt}
          </p>
        </div>
      )}

      {block.contract && block.contract.length > 0 && (
        <div className="space-y-2">
          <Label>{t("ai.detailContract")}</Label>
          <ol
            className="list-decimal space-y-0.5 rounded-md border border-input bg-control py-2 pl-8 pr-3 text-sm"
            data-testid="ai-detail-contract"
          >
            {block.contract.map((item, idx) => (
              <li key={idx} className="break-words">
                {item}
              </li>
            ))}
          </ol>
        </div>
      )}

      <div className="space-y-2">
        <Label>{t("ai.repairLabel")}</Label>
        <p
          className="w-full rounded-md border border-input bg-control px-3 py-2 text-sm sm:w-72"
          data-testid="ai-detail-repair"
        >
          {t(`ai.repair.${repair}`)}
        </p>
        {repair !== "off" && !configLoading && !aiEnabled && (
          <Alert variant="warning" data-testid="ai-detail-repair-unavailable">
            <AlertCircle className="h-4 w-4" />
            <AlertDescription>{t("ai.repairUnavailable")}</AlertDescription>
          </Alert>
        )}
        {repair !== "off" && aiEnabled && (
          <p className="text-xs text-muted-foreground" data-testid="ai-detail-last-repair">
            {attempt?.createdAt ? (
              <>
                {t("ai.lastRepair")} <TimeAgo date={attempt.createdAt} />
                {": "}
                {REPAIR_OUTCOMES.includes(outcome) ? t(`ai.repairOutcome.${outcome}`) : outcome}
                {reason && <span className="block break-words">{reason}</span>}
              </>
            ) : (
              t("ai.noRepairYet")
            )}
          </p>
        )}
      </div>

      <Link
        to="/orgs/$org/checks/$checkUid/history"
        params={{ org, checkUid: check.uid }}
        search={{}}
        className="inline-flex items-center gap-1 text-sm text-primary hover:underline"
        data-testid="ai-detail-history"
      >
        <History className="h-4 w-4" />
        {t("ai.detailHistory")}
      </Link>
    </div>
  );
}
