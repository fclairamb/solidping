import { useState } from "react";
import { useTranslation } from "react-i18next";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { ArrowLeft, Loader2, Send } from "lucide-react";
import { toast } from "sonner";
import {
  useIncidentPublication,
  useUpdateIncidentPublication,
  useAppendPublicationUpdate,
  type PublicationSeverity,
} from "@/api/hooks";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { QueryErrorView } from "@/components/shared/error-views";
import {
  publicationSeverityLabel,
  publicationStateLabel,
} from "@/lib/publication-labels";

export const Route = createFileRoute(
  "/orgs/$org/status-pages/$statusPageUid/incidents/$uid",
)({
  component: PublicationEditorPage,
});

// Mirrors the panel on the incident page: one list feeds the picker, and the
// same helper labels both it and the badge, so the two can never disagree.
const SEVERITIES: PublicationSeverity[] = ["minor", "major", "critical"];

const UPDATE_KINDS = [
  "investigating",
  "identified",
  "monitoring",
  "resolved",
  "maintenance",
  "info",
] as const;

function stateBadgeVariant(state: string) {
  if (state === "resolved") return "success" as const;
  if (state === "monitoring") return "default" as const;
  return "warning" as const;
}

/**
 * The publication editor. It lives on its own route rather than in a modal,
 * per repo convention — and because posting a public incident update deserves
 * a page you can re-read, not a dialog you dismiss by clicking outside it.
 */
function PublicationEditorPage() {
  const { t } = useTranslation("incidents");
  const navigate = useNavigate();
  const { org, statusPageUid, uid } = Route.useParams();
  const {
    data: publication,
    isLoading,
    error,
    refetch,
  } = useIncidentPublication(org, statusPageUid, uid);
  const updatePublication = useUpdateIncidentPublication(
    org,
    statusPageUid,
    uid,
  );
  const appendUpdate = useAppendPublicationUpdate(org, statusPageUid, uid);

  const [title, setTitle] = useState<string | null>(null);
  const [severity, setSeverity] = useState<string | null>(null);
  const [updateKind, setUpdateKind] = useState<string>("investigating");
  const [updateBody, setUpdateBody] = useState("");

  if (isLoading) {
    return (
      <div className="space-y-4 max-w-2xl">
        <Skeleton className="h-8 w-64" />
        <Skeleton className="h-48 w-full" />
      </div>
    );
  }

  if (error) {
    return (
      <QueryErrorView
        error={error}
        org={org}
        resource="incident publication"
        onRetry={() => void refetch()}
      />
    );
  }

  if (!publication) return null;

  const effectiveTitle = title ?? publication.title;
  const effectiveSeverity = severity ?? publication.severity ?? "none";

  const backToPage = () =>
    navigate({
      to: "/orgs/$org/status-pages/$statusPageUid",
      params: { org, statusPageUid },
    });

  return (
    <div className="space-y-6 max-w-2xl">
      <div className="flex items-center gap-4">
        <Button type="button" variant="ghost" size="icon" onClick={backToPage}>
          <ArrowLeft className="h-4 w-4" />
        </Button>
        <div className="min-w-0">
          <h1 className="text-2xl font-bold tracking-tight break-words">
            {publication.title}
          </h1>
          <div className="mt-1 flex flex-wrap items-center gap-2">
            <Badge
              variant={stateBadgeVariant(publication.state)}
              data-testid="publication-state-badge"
            >
              {publicationStateLabel(t, publication.state)}
            </Badge>
            {publication.autoCreated && (
              <Badge variant="secondary">
                {t("publications.autoPublished")}
              </Badge>
            )}
            {publication.humanTouched && (
              <Badge variant="outline">{t("publications.edited")}</Badge>
            )}
          </div>
        </div>
      </div>

      {publication.autoCreated && !publication.humanTouched && (
        <Alert>
          <AlertTitle>{t("publications.autopilotTitle")}</AlertTitle>
          <AlertDescription>
            {t("publications.autopilotDescription")}
          </AlertDescription>
        </Alert>
      )}

      <Card>
        <CardHeader>
          <CardTitle>{t("publications.publicDetails")}</CardTitle>
          <CardDescription>
            {t("publications.publicDetailsDescription")}
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="publicationTitle">{t("statusUpdates:form.title")}</Label>
            <Input
              id="publicationTitle"
              value={effectiveTitle}
              onChange={(e) => setTitle(e.target.value)}
              data-testid="publication-title-input"
            />
          </div>

          <div className="space-y-2">
            <Label htmlFor="publicationSeverity">{t("publications.severity")}</Label>
            <Select
              value={effectiveSeverity}
              onValueChange={(v) => setSeverity(v)}
            >
              <SelectTrigger
                id="publicationSeverity"
                data-testid="publication-severity-select"
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="none">{t("publications.noBadge")}</SelectItem>
                {SEVERITIES.map((value) => (
                  <SelectItem key={value} value={value}>
                    {publicationSeverityLabel(t, value)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          <Button
            type="button"
            disabled={updatePublication.isPending}
            data-testid="publication-save"
            onClick={async () => {
              await updatePublication.mutateAsync({
                title: effectiveTitle,
                severity:
                  effectiveSeverity === "none"
                    ? ""
                    : (effectiveSeverity as PublicationSeverity),
              });
              toast.success(t("publications.updated"));
              setTitle(null);
              setSeverity(null);
            }}
          >
            {updatePublication.isPending && (
              <Loader2 className="mr-2 h-4 w-4 animate-spin" />
            )}
            {t("statusUpdates:form.saveChanges")}
          </Button>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{t("publications.postUpdate")}</CardTitle>
          <CardDescription>
            {t("publications.postUpdateDescription")}
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="updateKind">{t("statusUpdates:form.kind")}</Label>
            <Select value={updateKind} onValueChange={setUpdateKind}>
              <SelectTrigger
                id="updateKind"
                data-testid="publication-update-kind"
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {UPDATE_KINDS.map((kind) => (
                  <SelectItem key={kind} value={kind}>
                    {publicationStateLabel(t, kind)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          <div className="space-y-2">
            <Label htmlFor="updateBody">{t("publications.updateBody")}</Label>
            <Textarea
              id="updateBody"
              rows={5}
              value={updateBody}
              onChange={(e) => setUpdateBody(e.target.value)}
              placeholder={t("publications.updateBodyPlaceholder")}
              data-testid="publication-update-body"
            />
          </div>

          <Button
            type="button"
            disabled={appendUpdate.isPending || !updateBody.trim()}
            data-testid="publication-update-submit"
            onClick={async () => {
              await appendUpdate.mutateAsync({
                kind: updateKind,
                bodyMarkdown: updateBody,
              });
              toast.success(t("publications.updatePosted"));
              setUpdateBody("");
            }}
          >
            {appendUpdate.isPending ? (
              <Loader2 className="mr-2 h-4 w-4 animate-spin" />
            ) : (
              <Send className="mr-2 h-4 w-4" />
            )}
            {t("publications.postUpdateSubmit")}
          </Button>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{t("publications.timeline")}</CardTitle>
          <CardDescription>
            {publication.affectedResources &&
            publication.affectedResources.length > 0
              ? t("publications.affected", {
                  resources: publication.affectedResources.join(", "),
                })
              : t("publications.noAffected")}
          </CardDescription>
        </CardHeader>
        <CardContent>
          {(publication.updates ?? []).length === 0 ? (
            <p className="text-sm text-muted-foreground">
              {t("publications.noUpdates")}
            </p>
          ) : (
            <ul className="space-y-4" data-testid="publication-timeline">
              {(publication.updates ?? []).map((update) => (
                <li key={update.uid} className="border-l-2 border-border pl-4">
                  <div className="flex flex-wrap items-center gap-2">
                    <Badge variant={stateBadgeVariant(update.kind)}>
                      {publicationStateLabel(t, update.kind)}
                    </Badge>
                    <time
                      dateTime={update.publishedAt}
                      className="text-xs text-muted-foreground"
                    >
                      {new Date(update.publishedAt).toLocaleString()}
                    </time>
                    {!update.authorUid && (
                      <span className="text-xs text-muted-foreground">
                        · {t("publications.automated")}
                      </span>
                    )}
                  </div>
                  <p className="mt-1 text-sm whitespace-pre-wrap break-words">
                    {update.bodyMarkdown}
                  </p>
                </li>
              ))}
            </ul>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
