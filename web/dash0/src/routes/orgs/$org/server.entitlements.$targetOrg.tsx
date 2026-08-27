import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { createFileRoute, Link } from "@tanstack/react-router";
import { AlertTriangle, ArrowLeft, Loader2, Undo2 } from "lucide-react";
import { toast } from "sonner";

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { TimeAgo } from "@/components/ui/time-ago";
import {
  useAdminEntitlementsDetail,
  useReleaseAdminEntitlements,
  useSetAdminEntitlements,
} from "@/api/hooks";
import type { AdminEntitlementsDetail, EntitlementAudit } from "@/api/hooks";
import { formatCheckRateDemand } from "@/lib/check-rate-limit";
import {
  ADMIN_LIMIT_KEYS,
  fieldsFromLimits,
  formatLimit,
  isLimitFieldValid,
  limitsDiff,
  limitsFromFields,
  provenanceOf,
  whiteLabelFrom,
} from "@/lib/entitlements-admin";
import type {
  AdminLimitKey,
  LimitChange,
  LimitField,
  WhiteLabelChoice,
} from "@/lib/entitlements-admin";

export const Route = createFileRoute(
  "/orgs/$org/server/entitlements/$targetOrg",
)({
  component: EntitlementsDetailPage,
});

function EntitlementsDetailPage() {
  const { t } = useTranslation("server");
  const { org, targetOrg } = Route.useParams();
  const { data, isLoading, error } = useAdminEntitlementsDetail(targetOrg);

  return (
    <div className="space-y-6">
      <Button variant="ghost" size="sm" asChild className="-ml-2">
        <Link to="/orgs/$org/server/entitlements" params={{ org }}>
          <ArrowLeft className="mr-2 h-4 w-4" />
          {t("entitlements.detail.back")}
        </Link>
      </Button>

      {isLoading ? (
        <div className="flex items-center justify-center py-8">
          <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
        </div>
      ) : error || !data ? (
        <p className="text-sm text-destructive">
          {t("entitlements.detail.loadError")}
        </p>
      ) : (
        // Keyed on the server's own view of the row: a save or a release
        // changes the key, which remounts the editor and re-seeds every field
        // from what was actually stored. A background refetch that returns the
        // same row leaves the key alone, so it never eats what is being typed.
        <EntitlementsEditor
          key={`${data.source}:${data.stored?.updatedAt ?? "none"}`}
          detail={data}
          targetOrg={targetOrg}
        />
      )}
    </div>
  );
}

function EntitlementsEditor({
  detail,
  targetOrg,
}: {
  detail: AdminEntitlementsDetail;
  targetOrg: string;
}) {
  const { t } = useTranslation("server");
  const unlimited = t("entitlements.unlimited");

  // The form is seeded from the RESOLVED values, not the stored row: the
  // editor saves a whole admin row, so pre-filling from what the org actually
  // has today is what keeps a save from silently unsetting a default.
  const [fields, setFields] = useState<Record<AdminLimitKey, LimitField>>(() =>
    fieldsFromLimits(detail.limits),
  );
  const [whiteLabel, setWhiteLabel] = useState<WhiteLabelChoice>(() =>
    whiteLabelFrom(detail.limits.whiteLabel),
  );
  const [displayName, setDisplayName] = useState(detail.displayName ?? "");
  const [displayEmoji, setDisplayEmoji] = useState(detail.displayEmoji ?? "");
  const [reason, setReason] = useState("");

  const setMutation = useSetAdminEntitlements(targetOrg);
  const releaseMutation = useReleaseAdminEntitlements(targetOrg);

  const allValid = ADMIN_LIMIT_KEYS.every((key) => isLimitFieldValid(fields[key]));
  const nextLimits = useMemo(
    () => limitsFromFields(fields, whiteLabel),
    [fields, whiteLabel],
  );
  const changes = useMemo(
    () => (allValid ? limitsDiff(detail.limits, nextLimits) : []),
    [allValid, detail.limits, nextLimits],
  );

  const provenance = provenanceOf({
    source: detail.source,
    displayName: detail.displayName,
    stored: detail.stored,
  });

  const onSave = () => {
    setMutation.mutate(
      {
        limits: nextLimits,
        displayName: displayName.trim() === "" ? null : displayName.trim(),
        displayEmoji: displayEmoji.trim() === "" ? null : displayEmoji.trim(),
        reason: reason.trim() || undefined,
      },
      {
        onSuccess: () => {
          setReason("");
          toast.success(t("entitlements.saved", { org: detail.slug }));
        },
        onError: () => toast.error(t("entitlements.saveError")),
      },
    );
  };

  const onRelease = () => {
    releaseMutation.mutate(reason.trim() || undefined, {
      onSuccess: (result) => {
        setReason("");
        toast.success(
          result.released
            ? t("entitlements.released", { org: detail.slug })
            : t("entitlements.notReleased", { org: detail.slug }),
        );
      },
      onError: () => toast.error(t("entitlements.saveError")),
    });
  };

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="flex flex-wrap items-center gap-2">
            <span>{detail.name || detail.slug}</span>
            <span className="text-sm font-normal text-muted-foreground">
              {detail.slug}
            </span>
          </CardTitle>
          <p className="text-sm text-muted-foreground" data-testid="entitlements-provenance">
            <ProvenanceText provenance={provenance} />
          </p>
        </CardHeader>
        <CardContent className="space-y-3">
          {!detail.stored ? (
            <p className="text-sm text-muted-foreground">
              {t("entitlements.detail.defaultsNote")}
            </p>
          ) : null}
          {detail.overCheckRate && detail.checksPerMinute ? (
            <Alert variant="warning">
              <AlertTriangle className="h-4 w-4" />
              <AlertDescription>
                {t("entitlements.overLimit", {
                  demand: formatCheckRateDemand(detail.checksPerMinute.demand),
                  limit: detail.checksPerMinute.limit ?? 0,
                })}
              </AlertDescription>
            </Alert>
          ) : null}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{t("entitlements.detail.limitsTitle")}</CardTitle>
          <p className="text-sm text-muted-foreground">
            {t("entitlements.detail.limitsDescription")}
          </p>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid gap-4 sm:grid-cols-2">
            {ADMIN_LIMIT_KEYS.map((key) => (
              <LimitRow
                key={key}
                limitKey={key}
                field={fields[key]}
                onChange={(next) =>
                  setFields((current) => ({ ...current, [key]: next }))
                }
              />
            ))}
            <div className="space-y-2">
              <Label htmlFor="ent-white-label">
                {t("entitlements.limits.whiteLabel")}
              </Label>
              <Select
                value={whiteLabel}
                onValueChange={(value) =>
                  setWhiteLabel(value as WhiteLabelChoice)
                }
              >
                <SelectTrigger id="ent-white-label" data-testid="entitlements-white-label">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="default">
                    {t("entitlements.whiteLabel.default")}
                  </SelectItem>
                  <SelectItem value="allowed">
                    {t("entitlements.whiteLabel.allowed")}
                  </SelectItem>
                  <SelectItem value="denied">
                    {t("entitlements.whiteLabel.denied")}
                  </SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{t("entitlements.detail.identityTitle")}</CardTitle>
          <p className="text-sm text-muted-foreground">
            {t("entitlements.detail.identityDescription")}
          </p>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-2">
              <Label htmlFor="ent-display-name">
                {t("entitlements.detail.displayName")}
              </Label>
              <Input
                id="ent-display-name"
                value={displayName}
                onChange={(event) => setDisplayName(event.target.value)}
                data-testid="entitlements-display-name"
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="ent-display-emoji">
                {t("entitlements.detail.displayEmoji")}
              </Label>
              <Input
                id="ent-display-emoji"
                value={displayEmoji}
                onChange={(event) => setDisplayEmoji(event.target.value)}
                data-testid="entitlements-display-emoji"
              />
            </div>
          </div>
          <div className="space-y-2">
            <Label htmlFor="ent-reason">{t("entitlements.detail.reason")}</Label>
            <Input
              id="ent-reason"
              value={reason}
              placeholder={t("entitlements.detail.reasonPlaceholder")}
              onChange={(event) => setReason(event.target.value)}
              data-testid="entitlements-reason"
            />
          </div>

          {!allValid ? (
            <p className="text-sm text-destructive">
              {t("entitlements.detail.invalid")}
            </p>
          ) : null}

          <div className="flex flex-wrap gap-2">
            <AlertDialog>
              <AlertDialogTrigger asChild>
                <Button
                  disabled={!allValid || setMutation.isPending}
                  data-testid="entitlements-save"
                >
                  {setMutation.isPending ? (
                    <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                  ) : null}
                  {t("entitlements.detail.save")}
                </Button>
              </AlertDialogTrigger>
              <AlertDialogContent>
                <AlertDialogHeader>
                  <AlertDialogTitle>
                    {t("entitlements.confirmSave.title", { org: detail.slug })}
                  </AlertDialogTitle>
                  <AlertDialogDescription>
                    {t("entitlements.confirmSave.body", { org: detail.slug })}
                  </AlertDialogDescription>
                </AlertDialogHeader>
                <ChangeSummary changes={changes} unlimited={unlimited} />
                <AlertDialogFooter>
                  <AlertDialogCancel>{t("common:cancel")}</AlertDialogCancel>
                  <AlertDialogAction
                    onClick={onSave}
                    data-testid="entitlements-save-confirm"
                  >
                    {t("entitlements.confirmSave.confirm")}
                  </AlertDialogAction>
                </AlertDialogFooter>
              </AlertDialogContent>
            </AlertDialog>

            <AlertDialog>
              <AlertDialogTrigger asChild>
                <Button
                  variant="outline"
                  disabled={releaseMutation.isPending}
                  data-testid="entitlements-release"
                >
                  <Undo2 className="mr-2 h-4 w-4" />
                  {t("entitlements.detail.release")}
                </Button>
              </AlertDialogTrigger>
              <AlertDialogContent>
                <AlertDialogHeader>
                  <AlertDialogTitle>
                    {t("entitlements.confirmRelease.title", {
                      org: detail.slug,
                    })}
                  </AlertDialogTitle>
                  <AlertDialogDescription>
                    {t("entitlements.confirmRelease.body", {
                      org: detail.slug,
                    })}
                  </AlertDialogDescription>
                </AlertDialogHeader>
                <AlertDialogFooter>
                  <AlertDialogCancel>{t("common:cancel")}</AlertDialogCancel>
                  <AlertDialogAction
                    onClick={onRelease}
                    data-testid="entitlements-release-confirm"
                  >
                    {t("entitlements.confirmRelease.confirm")}
                  </AlertDialogAction>
                </AlertDialogFooter>
              </AlertDialogContent>
            </AlertDialog>
          </div>
        </CardContent>
      </Card>

      <AuditTrail audits={detail.audits} />
    </div>
  );
}

function LimitRow({
  limitKey,
  field,
  onChange,
}: {
  limitKey: AdminLimitKey;
  field: LimitField;
  onChange: (next: LimitField) => void;
}) {
  const { t } = useTranslation("server");
  const inputId = `ent-limit-${limitKey}`;

  return (
    <div className="space-y-2">
      <Label htmlFor={inputId}>{t(`entitlements.limits.${limitKey}`)}</Label>
      <div className="flex items-center gap-3">
        <Input
          id={inputId}
          inputMode="numeric"
          value={field.value}
          disabled={field.unlimited}
          onChange={(event) =>
            onChange({ ...field, value: event.target.value })
          }
          data-testid={`entitlements-input-${limitKey}`}
        />
        <div className="flex shrink-0 items-center gap-2">
          <Switch
            id={`${inputId}-unlimited`}
            checked={field.unlimited}
            onCheckedChange={(checked) =>
              onChange({ ...field, unlimited: checked })
            }
            data-testid={`entitlements-unlimited-${limitKey}`}
          />
          <Label
            htmlFor={`${inputId}-unlimited`}
            className="text-xs text-muted-foreground"
          >
            {t("entitlements.detail.unlimitedToggle")}
          </Label>
        </div>
      </div>
    </div>
  );
}

function ProvenanceText({
  provenance,
}: {
  provenance: ReturnType<typeof provenanceOf>;
}) {
  const { t } = useTranslation("server");

  if (provenance.kind === "admin") {
    return provenance.since ? (
      <>
        {t("entitlements.provenance.adminPlain")} ·{" "}
        <TimeAgo date={provenance.since} />
      </>
    ) : (
      <>{t("entitlements.provenance.adminPlain")}</>
    );
  }

  if (provenance.kind === "billing") {
    return (
      <>
        {provenance.planName
          ? t("entitlements.provenance.billing", { plan: provenance.planName })
          : t("entitlements.provenance.billingPlain")}
      </>
    );
  }

  if (provenance.kind === "other") {
    return <>{t("entitlements.provenance.other", { source: provenance.source })}</>;
  }

  return <>{t("entitlements.provenance.default")}</>;
}

function ChangeSummary({
  changes,
  unlimited,
}: {
  changes: LimitChange[];
  unlimited: string;
}) {
  const { t } = useTranslation("server");

  if (changes.length === 0) {
    return (
      <p className="text-sm text-muted-foreground">
        {t("entitlements.confirmSave.noChanges")}
      </p>
    );
  }

  return (
    <ul className="space-y-1 text-sm" data-testid="entitlements-diff">
      {changes.map((change) => (
        <li key={change.key} className="flex items-center gap-2">
          <span className="text-muted-foreground">
            {t(`entitlements.limits.${change.key}`)}
          </span>
          <span className="font-mono text-xs">
            {renderChangeValue(change.from, unlimited, t)} →{" "}
            {renderChangeValue(change.to, unlimited, t)}
          </span>
        </li>
      ))}
    </ul>
  );
}

function renderChangeValue(
  value: number | boolean | null,
  unlimited: string,
  t: (key: string) => string,
): string {
  if (typeof value === "boolean") {
    return value
      ? t("entitlements.whiteLabel.allowed")
      : t("entitlements.whiteLabel.denied");
  }

  return formatLimit(value, unlimited);
}

function AuditTrail({ audits }: { audits: EntitlementAudit[] }) {
  const { t } = useTranslation("server");

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("entitlements.detail.auditTitle")}</CardTitle>
      </CardHeader>
      <CardContent>
        {audits.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            {t("entitlements.detail.auditEmpty")}
          </p>
        ) : (
          <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("entitlements.columns.source")}</TableHead>
                  <TableHead>{t("entitlements.detail.reason")}</TableHead>
                  <TableHead>{t("entitlements.columns.override")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {audits.map((audit) => (
                  <TableRow key={audit.uid}>
                    <TableCell>
                      <AuditSourceBadge source={audit.source} />
                      <div className="text-xs text-muted-foreground">
                        {audit.actor}
                      </div>
                    </TableCell>
                    <TableCell className="text-sm">
                      {audit.reason || "—"}
                    </TableCell>
                    <TableCell className="text-sm">
                      <TimeAgo date={audit.createdAt} />
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function AuditSourceBadge({ source }: { source: string }) {
  const { t } = useTranslation("server");

  if (source === "billing-service:suppressed") {
    return (
      <Badge variant="warning" data-testid="audit-suppressed">
        {t("entitlements.audit.suppressed")}
      </Badge>
    );
  }

  if (source === "admin:released") {
    return <Badge variant="outline">{t("entitlements.audit.released")}</Badge>;
  }

  if (source === "admin") {
    return <Badge variant="secondary">{t("entitlements.audit.admin")}</Badge>;
  }

  if (source === "billing-service") {
    return <Badge variant="outline">{t("entitlements.audit.billing")}</Badge>;
  }

  return <Badge variant="outline">{source}</Badge>;
}
