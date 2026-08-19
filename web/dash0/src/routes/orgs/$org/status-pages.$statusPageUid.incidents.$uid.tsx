import { useState } from "react";
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

export const Route = createFileRoute(
  "/orgs/$org/status-pages/$statusPageUid/incidents/$uid",
)({
  component: PublicationEditorPage,
});

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
              {publication.state}
            </Badge>
            {publication.autoCreated && (
              <Badge variant="secondary">auto-published</Badge>
            )}
            {publication.humanTouched && (
              <Badge variant="outline">edited</Badge>
            )}
          </div>
        </div>
      </div>

      {publication.autoCreated && !publication.humanTouched && (
        <Alert>
          <AlertTitle>This incident is on autopilot</AlertTitle>
          <AlertDescription>
            It was published automatically and will resolve itself when the
            underlying incident recovers. The moment you edit it or post an
            update, it becomes yours — the automation will then only report the
            recovery and leave the final resolve to you.
          </AlertDescription>
        </Alert>
      )}

      <Card>
        <CardHeader>
          <CardTitle>Public details</CardTitle>
          <CardDescription>
            Everything here is shown to customers. Never paste probe output,
            error strings, internal hostnames or IP addresses into it.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="publicationTitle">Title</Label>
            <Input
              id="publicationTitle"
              value={effectiveTitle}
              onChange={(e) => setTitle(e.target.value)}
              data-testid="publication-title-input"
            />
          </div>

          <div className="space-y-2">
            <Label htmlFor="publicationSeverity">Severity</Label>
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
                <SelectItem value="none">No badge</SelectItem>
                <SelectItem value="minor">Minor</SelectItem>
                <SelectItem value="major">Major</SelectItem>
                <SelectItem value="critical">Critical</SelectItem>
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
              toast.success("Incident updated");
              setTitle(null);
              setSeverity(null);
            }}
          >
            {updatePublication.isPending && (
              <Loader2 className="mr-2 h-4 w-4 animate-spin" />
            )}
            Save changes
          </Button>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Post an update</CardTitle>
          <CardDescription>
            Updates are append-only: there is no edit and no delete. Choosing
            investigating, identified, monitoring or resolved also moves the
            incident to that state.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="updateKind">Kind</Label>
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
                    {kind}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          <div className="space-y-2">
            <Label htmlFor="updateBody">Message (Markdown)</Label>
            <Textarea
              id="updateBody"
              rows={5}
              value={updateBody}
              onChange={(e) => setUpdateBody(e.target.value)}
              placeholder="We have identified the cause and are deploying a fix."
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
              toast.success("Update posted");
              setUpdateBody("");
            }}
          >
            {appendUpdate.isPending ? (
              <Loader2 className="mr-2 h-4 w-4 animate-spin" />
            ) : (
              <Send className="mr-2 h-4 w-4" />
            )}
            Post update
          </Button>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Timeline</CardTitle>
          <CardDescription>
            {publication.affectedResources &&
            publication.affectedResources.length > 0
              ? `Affected: ${publication.affectedResources.join(", ")}`
              : "No affected components resolved for this page."}
          </CardDescription>
        </CardHeader>
        <CardContent>
          {(publication.updates ?? []).length === 0 ? (
            <p className="text-sm text-muted-foreground">No updates yet.</p>
          ) : (
            <ul className="space-y-4" data-testid="publication-timeline">
              {(publication.updates ?? []).map((update) => (
                <li key={update.uid} className="border-l-2 border-border pl-4">
                  <div className="flex flex-wrap items-center gap-2">
                    <Badge variant={stateBadgeVariant(update.kind)}>
                      {update.kind}
                    </Badge>
                    <time
                      dateTime={update.publishedAt}
                      className="text-xs text-muted-foreground"
                    >
                      {new Date(update.publishedAt).toLocaleString()}
                    </time>
                    {!update.authorUid && (
                      <span className="text-xs text-muted-foreground">
                        · automated
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
