import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { Minus, Plus } from "lucide-react";
import { useUpdateCheck } from "@/api/hooks";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";

type Output = Record<string, unknown>;

export interface DnsChanges {
  added: string[];
  removed: string[];
}

function asStringArray(value: unknown): string[] {
  if (!Array.isArray(value)) return [];
  return value.filter((v): v is string => typeof v === "string");
}

// dnsChangesOf reads the `changes` output a dns check with change detection
// reports when its answer differs from the region's baseline (spec
// 2026-10-03-04). Null when there is nothing to show.
export function dnsChangesOf(output: Output | undefined): DnsChanges | null {
  const raw = output?.changes;
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return null;
  const record = raw as Record<string, unknown>;
  const added = asStringArray(record.added);
  const removed = asStringArray(record.removed);
  if (added.length === 0 && removed.length === 0) return null;
  return { added, removed };
}

// acceptedConfig is the config PATCHed by "Accept current records": the same
// config with an empty baseline. The server then makes every region run right
// away, and each run captures its current answer as the new baseline.
export function acceptedConfig(config: Record<string, unknown> | undefined): Record<string, unknown> {
  return { ...(config ?? {}), baseline: {} };
}

export function DnsChangesCard({
  org,
  checkUid,
  config,
  output,
  canEdit,
}: {
  org: string;
  checkUid: string;
  config: Record<string, unknown> | undefined;
  output: Output | undefined;
  canEdit: boolean;
}) {
  const { t } = useTranslation("checks");
  const update = useUpdateCheck(org, checkUid);
  const changes = dnsChangesOf(output);
  if (!changes) return null;

  const accept = () => {
    update.mutate(
      { config: acceptedConfig(config) },
      {
        onSuccess: () => toast.success(t("dnsChanges.accepted")),
        onError: (err) =>
          toast.error(err instanceof Error ? err.message : t("dnsChanges.acceptFailed")),
      },
    );
  };

  return (
    <Card data-testid="dns-changes-card">
      <CardHeader>
        <CardTitle>{t("dnsChanges.title")}</CardTitle>
        <CardDescription>{t("dnsChanges.description")}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        {changes.added.length > 0 && (
          <div className="space-y-1" data-testid="dns-changes-added">
            <p className="text-sm font-medium">{t("dnsChanges.added")}</p>
            <ul className="space-y-1">
              {changes.added.map((value) => (
                <li key={value} className="flex items-start gap-2 font-mono text-xs break-all">
                  <Plus className="h-3.5 w-3.5 shrink-0 text-emerald-600" aria-hidden />
                  {value}
                </li>
              ))}
            </ul>
          </div>
        )}
        {changes.removed.length > 0 && (
          <div className="space-y-1" data-testid="dns-changes-removed">
            <p className="text-sm font-medium">{t("dnsChanges.removed")}</p>
            <ul className="space-y-1">
              {changes.removed.map((value) => (
                <li key={value} className="flex items-start gap-2 font-mono text-xs break-all">
                  <Minus className="h-3.5 w-3.5 shrink-0 text-destructive" aria-hidden />
                  {value}
                </li>
              ))}
            </ul>
          </div>
        )}
        {canEdit && (
          <Button
            type="button"
            onClick={accept}
            disabled={update.isPending}
            className="w-full sm:w-auto"
            data-testid="dns-changes-accept"
          >
            {t("dnsChanges.accept")}
          </Button>
        )}
      </CardContent>
    </Card>
  );
}
