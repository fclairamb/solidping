import { useState } from "react";
import { Link } from "@tanstack/react-router";
import { Globe, Loader2, Trash2 } from "lucide-react";
import { toast } from "sonner";
import {
  useIncidentPublicationsForIncident,
  usePublishIncident,
  useUnpublishIncident,
  useStatusPages,
  type PublicationSeverity,
} from "@/api/hooks";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
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
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";

function stateBadgeVariant(state: string) {
  if (state === "resolved") return "success" as const;
  if (state === "monitoring") return "default" as const;
  return "warning" as const;
}

/**
 * "Published on …" — which status pages this incident is visible on, and the
 * controls to publish it somewhere else or take it down.
 *
 * This is deliberately separate from the Status updates panel above it. A
 * status update is a loose note pinned to a page; a PUBLICATION is the public
 * incident object itself, with its own title, state and severity. Conflating
 * the two is what made automatic incidents invisible in the first place.
 */
export function IncidentPublicationsPanel({
  org,
  incidentUid,
}: {
  org: string;
  incidentUid: string;
}) {
  const { data: publications, isLoading } = useIncidentPublicationsForIncident(
    org,
    incidentUid,
  );
  const { data: statusPages } = useStatusPages(org);
  const publish = usePublishIncident(org, incidentUid);
  const unpublish = useUnpublishIncident(org, incidentUid);

  const [targetPage, setTargetPage] = useState("");
  const [severity, setSeverity] = useState("none");
  const [unpublishUid, setUnpublishUid] = useState<string | null>(null);

  const publishedPageUids = new Set(
    (publications ?? []).map((publication) => publication.statusPageUid),
  );
  const availablePages = (statusPages ?? []).filter(
    (page) => !publishedPageUids.has(page.uid),
  );

  return (
    <Card data-testid="incident-publications-panel">
      <CardHeader>
        <div className="flex items-center gap-2">
          <Globe className="h-4 w-4 text-muted-foreground" />
          <CardTitle>Published on</CardTitle>
        </div>
        <CardDescription>
          Status pages where customers can see this incident. Publishing writes
          a customer-readable title — never the internal one.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        {isLoading ? (
          <Skeleton className="h-16 w-full" />
        ) : (publications ?? []).length === 0 ? (
          <p className="text-sm text-muted-foreground">
            Not published on any status page.
          </p>
        ) : (
          <ul className="space-y-2" data-testid="incident-publications-list">
            {(publications ?? []).map((publication) => (
              <li
                key={publication.uid}
                className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-border p-3"
              >
                <div className="min-w-0 space-y-1">
                  <Link
                    to="/orgs/$org/status-pages/$statusPageUid/incidents/$uid"
                    params={{
                      org,
                      statusPageUid: publication.statusPageUid,
                      uid: publication.uid,
                    }}
                    className="text-sm font-medium hover:underline break-words"
                  >
                    {publication.title}
                  </Link>
                  <div className="flex flex-wrap items-center gap-2">
                    <Badge variant={stateBadgeVariant(publication.state)}>
                      {publication.state}
                    </Badge>
                    {publication.severity && (
                      <Badge variant="secondary">{publication.severity}</Badge>
                    )}
                    {publication.autoCreated && (
                      <Badge variant="outline">auto-published</Badge>
                    )}
                  </div>
                </div>
                <Button
                  variant="ghost"
                  size="icon"
                  aria-label="Unpublish"
                  className="text-destructive"
                  onClick={() => setUnpublishUid(publication.uid)}
                  data-testid="incident-unpublish-button"
                >
                  <Trash2 className="h-4 w-4" />
                </Button>
              </li>
            ))}
          </ul>
        )}

        {availablePages.length > 0 && (
          <div className="space-y-3 border-t border-border pt-4">
            <div className="space-y-2">
              <Label htmlFor="publishTargetPage">Publish on</Label>
              <Select value={targetPage} onValueChange={setTargetPage}>
                <SelectTrigger
                  id="publishTargetPage"
                  data-testid="incident-publish-page-select"
                >
                  <SelectValue placeholder="Select a status page" />
                </SelectTrigger>
                <SelectContent>
                  {availablePages.map((page) => (
                    <SelectItem key={page.uid} value={page.uid}>
                      {page.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            <div className="space-y-2">
              <Label htmlFor="publishSeverity">Severity</Label>
              <Select value={severity} onValueChange={setSeverity}>
                <SelectTrigger
                  id="publishSeverity"
                  data-testid="incident-publish-severity-select"
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
              disabled={!targetPage || publish.isPending}
              data-testid="incident-publish-button"
              onClick={async () => {
                try {
                  await publish.mutateAsync({
                    statusPageUid: targetPage,
                    severity:
                      severity === "none"
                        ? undefined
                        : (severity as PublicationSeverity),
                  });
                  toast.success("Incident published");
                  setTargetPage("");
                } catch {
                  toast.error("Failed to publish incident");
                }
              }}
            >
              {publish.isPending && (
                <Loader2 className="mr-2 h-4 w-4 animate-spin" />
              )}
              Publish
            </Button>
          </div>
        )}
      </CardContent>

      <AlertDialog
        open={unpublishUid !== null}
        onOpenChange={(open) => !open && setUnpublishUid(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Unpublish this incident?</AlertDialogTitle>
            <AlertDialogDescription>
              It disappears from the public status page immediately. The
              publication and its updates are kept for audit, and the incident
              can be published again later.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
              onClick={async () => {
                if (!unpublishUid) return;
                try {
                  await unpublish.mutateAsync(unpublishUid);
                  toast.success("Incident unpublished");
                } catch {
                  toast.error("Failed to unpublish incident");
                } finally {
                  setUnpublishUid(null);
                }
              }}
            >
              Unpublish
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </Card>
  );
}
