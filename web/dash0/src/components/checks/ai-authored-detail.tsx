import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { History, Sparkles } from "lucide-react";
import type { AICheckBlock, Check } from "@/api/hooks";
import { Badge } from "@/components/ui/badge";
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
export function AIAuthoredDetail({ org, check }: { org: string; check: Check }) {
  const { t } = useTranslation("checks");
  const block = aiBlockOf(check);

  if (!block) return null;

  const repair = block.repair ?? "propose";

  return (
    <div className="space-y-3 border-t pt-4" data-testid="ai-authored-detail">
      <div className="flex flex-wrap items-center gap-2">
        <Sparkles className="h-4 w-4 text-primary" />
        <span className="text-sm font-medium">{t("ai.detailTitle")}</span>
        <Badge variant="outline" data-testid="ai-detail-repair">
          {t("ai.detailRepair", { mode: t(`ai.repair.${repair}`) })}
        </Badge>
      </div>
      {block.prompt && (
        <div>
          <Label>{t("ai.detailPrompt")}</Label>
          <p className="text-sm text-muted-foreground whitespace-pre-wrap" data-testid="ai-detail-prompt">
            {block.prompt}
          </p>
        </div>
      )}
      {block.contract && block.contract.length > 0 && (
        <div>
          <Label>{t("ai.detailContract")}</Label>
          <ol className="ml-5 list-decimal text-sm text-muted-foreground" data-testid="ai-detail-contract">
            {block.contract.map((item, idx) => (
              <li key={idx}>{item}</li>
            ))}
          </ol>
        </div>
      )}
      {block.model && (
        <p className="text-xs text-muted-foreground">
          {t("ai.detailModel", { model: block.model })}
          {block.generated_at && (
            <>
              {" · "}
              <TimeAgo date={block.generated_at} />
            </>
          )}
        </p>
      )}
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
