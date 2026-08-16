import { useState } from "react";
import { useTranslation } from "react-i18next";
import { createFileRoute, Link } from "@tanstack/react-router";
import { Bot, Copy, Check as CheckIcon, KeyRound, Plus, Trash2 } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
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
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  useAgents,
  useCreatePrivateRegion,
  useDeleteEnrollmentToken,
  useDeletePrivateRegion,
  useEnrollmentTokens,
  useMintEnrollmentToken,
  usePrivateRegions,
  useRevokeAgent,
  type MintedEnrollmentToken,
  type PrivateRegion,
} from "@/api/hooks";
import { DocsLink } from "@/components/shared/docs-link";
import {
  Ipv6CapabilityBadge,
  ipv6Capability,
} from "@/components/shared/ipv6-capability";
import { LiveDurationAgo } from "@/components/shared/relative-time";

export const Route = createFileRoute("/orgs/$org/organization/private-locations/")({
  component: PrivateLocationsPage,
});

function PrivateLocationsPage() {
  const { org } = Route.useParams();

  return (
    <div className="space-y-6">
      <RegionsCard org={org} />
      <AgentsCard org={org} />
      <TokensCard org={org} />
    </div>
  );
}

// ---------------------------------------------------------------------------
// Private regions
// ---------------------------------------------------------------------------

function RegionsCard({ org }: { org: string }) {
  const { t } = useTranslation(["org"]);
  const { data: regions, isLoading } = usePrivateRegions(org);
  const createRegion = useCreatePrivateRegion(org);
  const deleteRegion = useDeletePrivateRegion(org);
  const mintToken = useMintEnrollmentToken(org);

  const [slug, setSlug] = useState("");
  const [name, setName] = useState("");
  const [formError, setFormError] = useState<string | null>(null);
  const [pendingDelete, setPendingDelete] = useState<PrivateRegion | null>(null);
  const [minted, setMinted] = useState<MintedEnrollmentToken | null>(null);

  const onCreate = async () => {
    setFormError(null);
    try {
      await createRegion.mutateAsync({ slug: slug.trim(), name: name.trim() || undefined });
      setSlug("");
      setName("");
    } catch (err) {
      setFormError(err instanceof Error ? err.message : String(err));
    }
  };

  const onMint = async (regionSlug: string) => {
    setFormError(null);
    try {
      const token = await mintToken.mutateAsync({ regionSlug });
      setMinted(token);
    } catch (err) {
      setFormError(err instanceof Error ? err.message : String(err));
    }
  };

  const confirmDelete = async () => {
    if (!pendingDelete) return;
    setFormError(null);
    try {
      await deleteRegion.mutateAsync(pendingDelete.slug);
    } catch (err) {
      setFormError(err instanceof Error ? err.message : String(err));
    } finally {
      setPendingDelete(null);
    }
  };

  return (
    <Card data-testid="private-regions-card">
      <CardHeader className="flex flex-row flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <CardTitle>{t("privateLocations.regions.title", "Private locations")}</CardTitle>
          <CardDescription>
            {t(
              "privateLocations.regions.description",
              "Org-private regions served by deported agents you run inside your own network. Agents connect outbound only — no inbound ports.",
            )}
          </CardDescription>
        </div>
        <div className="flex items-center gap-2">
          <Button asChild size="sm" data-testid="register-agent-button">
            <Link to="/orgs/$org/organization/private-locations/register" params={{ org }}>
              <Bot className="mr-2 h-4 w-4" />
              {t("privateLocations.regions.registerAgent", "Register an agent")}
            </Link>
          </Button>
          <DocsLink href="/docs/features/private-locations" />
        </div>
      </CardHeader>
      <CardContent className="space-y-4">
        {isLoading ? (
          <Skeleton className="h-16 w-full" />
        ) : (regions?.length ?? 0) === 0 ? (
          <p className="text-sm text-muted-foreground" data-testid="no-private-regions">
            {t(
              "privateLocations.regions.emptyPrefix",
              "No private locations yet. Follow the ",
            )}
            <Link
              to="/orgs/$org/organization/private-locations/register"
              params={{ org }}
              className="text-primary underline-offset-4 hover:underline"
              data-testid="empty-state-register-agent"
            >
              {t("privateLocations.regions.emptyLinkText", "guided setup")}
            </Link>
            {t("privateLocations.regions.emptySuffix", ", or create one manually below.")}
          </p>
        ) : (
          <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("privateLocations.regions.name", "Name")}</TableHead>
                  <TableHead>{t("privateLocations.regions.region", "Region")}</TableHead>
                  <TableHead>{t("privateLocations.regions.agents", "Agents")}</TableHead>
                  <TableHead>{t("privateLocations.regions.ipv6", "IPv6")}</TableHead>
                  <TableHead className="w-24 text-right">
                    {t("privateLocations.regions.actions", "Actions")}
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {regions?.map((region) => (
                  <TableRow key={region.slug} data-testid={`private-region-${region.slug}`}>
                    <TableCell className="font-medium">
                      <span aria-hidden className="mr-1.5">
                        {region.emoji}
                      </span>
                      {region.name}
                    </TableCell>
                    <TableCell>
                      <code className="rounded bg-muted px-1.5 py-0.5 text-xs">{region.region}</code>
                    </TableCell>
                    <TableCell>
                      <Badge variant={region.agentCount > 0 ? "default" : "secondary"}>
                        {region.agentCount}
                      </Badge>
                    </TableCell>
                    {/* Egress families this location's live agents report
                        (spec 2026-08-15-11). This is the one case the user can
                        actually fix — by enabling IPv6 on a host they own — so
                        it belongs on the location's own page. Three states:
                        "unknown" (no live agent reporting, or an older agent)
                        is never rendered as "no". */}
                    <TableCell>
                      <Ipv6CapabilityBadge
                        capability={ipv6Capability(region.capabilities)}
                        data-testid={`private-region-ipv6-${region.slug}`}
                      />
                    </TableCell>
                    <TableCell className="text-right">
                      <div className="flex justify-end gap-1">
                        <Button
                          variant="ghost"
                          size="icon"
                          title={t("privateLocations.regions.mint", "New enrollment token")}
                          data-testid={`mint-token-${region.slug}`}
                          onClick={() => onMint(region.slug)}
                          disabled={mintToken.isPending}
                        >
                          <KeyRound className="h-4 w-4" />
                        </Button>
                        <Button
                          variant="ghost"
                          size="icon"
                          title={t("privateLocations.regions.delete", "Delete location")}
                          data-testid={`delete-region-${region.slug}`}
                          onClick={() => setPendingDelete(region)}
                        >
                          <Trash2 className="h-4 w-4 text-destructive" />
                        </Button>
                      </div>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        )}

        <div className="flex flex-wrap items-end gap-2 border-t pt-4">
          <div className="space-y-1.5">
            <Label htmlFor="new-region-slug">{t("privateLocations.regions.slug", "Slug")}</Label>
            <Input
              id="new-region-slug"
              data-testid="new-region-slug"
              placeholder="dc1"
              value={slug}
              onChange={(e) => setSlug(e.target.value)}
              className="w-40"
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="new-region-name">
              {t("privateLocations.regions.displayName", "Display name")}
            </Label>
            <Input
              id="new-region-name"
              data-testid="new-region-name"
              placeholder="Paris DC"
              value={name}
              onChange={(e) => setName(e.target.value)}
              className="w-48"
            />
          </div>
          <Button
            onClick={onCreate}
            disabled={!slug.trim() || createRegion.isPending}
            data-testid="create-region"
          >
            <Plus className="mr-1 h-4 w-4" />
            {t("privateLocations.regions.create", "Add location")}
          </Button>
        </div>
        {formError && (
          <p className="text-sm text-destructive" data-testid="private-regions-error">
            {formError}
          </p>
        )}
      </CardContent>

      <AlertDialog open={!!pendingDelete} onOpenChange={(open) => !open && setPendingDelete(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t("privateLocations.regions.deleteTitle", "Delete private location?")}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(
                "privateLocations.regions.deleteDescription",
                "Checks targeting this location will stop being scheduled there. Locations with enrolled agents cannot be deleted — revoke the agents first.",
              )}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("privateLocations.cancel", "Cancel")}</AlertDialogCancel>
            <AlertDialogAction onClick={confirmDelete} data-testid="confirm-delete-region">
              {t("privateLocations.regions.deleteConfirm", "Delete")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <TokenRevealDialog minted={minted} onClose={() => setMinted(null)} />
    </Card>
  );
}

// TokenRevealDialog shows a freshly minted enrollment token EXACTLY ONCE —
// the server stores only its hash and can never display it again.
function TokenRevealDialog({
  minted,
  onClose,
}: {
  minted: MintedEnrollmentToken | null;
  onClose: () => void;
}) {
  const { t } = useTranslation(["org"]);
  const [copied, setCopied] = useState(false);

  const copy = async () => {
    if (!minted) return;
    await navigator.clipboard.writeText(minted.token);
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  };

  return (
    <Dialog open={!!minted} onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("privateLocations.token.title", "Enrollment token")}</DialogTitle>
          <DialogDescription>
            {t(
              "privateLocations.token.description",
              "Copy it now — this token is shown only once and expires in 24 hours. It enrolls exactly one agent.",
            )}
          </DialogDescription>
        </DialogHeader>
        {minted && (
          <div className="min-w-0 space-y-3">
            <div className="flex min-w-0 items-center gap-2">
              <code
                className="min-w-0 flex-1 overflow-x-auto rounded bg-muted px-2 py-1.5 text-xs"
                data-testid="minted-token"
              >
                {minted.token}
              </code>
              <Button variant="outline" size="icon" onClick={copy} data-testid="copy-token">
                {copied ? <CheckIcon className="h-4 w-4" /> : <Copy className="h-4 w-4" />}
              </Button>
            </div>
            <p className="text-xs text-muted-foreground">
              {t("privateLocations.token.run", "Run the agent with:")}
            </p>
            <pre
              className="max-w-full overflow-x-auto rounded bg-muted p-2 text-xs"
              data-testid="docker-run-command"
            >
              {`docker run -v agent-data:/data \\
  -e SP_NODE_ROLE=agent \\
  -e SP_AGENT_SERVER_URL=${window.location.origin} \\
  -e SP_AGENT_ENROLLMENT_TOKEN=${minted.token} \\
  ghcr.io/fclairamb/solidping:latest`}
            </pre>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Agents
// ---------------------------------------------------------------------------

function AgentsCard({ org }: { org: string }) {
  const { t } = useTranslation(["org"]);
  const { data: agents, isLoading } = useAgents(org);
  const revokeAgent = useRevokeAgent(org);
  const [pendingRevoke, setPendingRevoke] = useState<string | null>(null);

  const confirmRevoke = async () => {
    if (!pendingRevoke) return;
    try {
      await revokeAgent.mutateAsync(pendingRevoke);
    } finally {
      setPendingRevoke(null);
    }
  };

  return (
    <Card data-testid="agents-card">
      <CardHeader>
        <CardTitle>{t("privateLocations.agents.title", "Agents")}</CardTitle>
        <CardDescription>
          {t(
            "privateLocations.agents.description",
            "Enrolled agents and their status. Revoking an agent cuts its access immediately; rotate any credentials it could decrypt.",
          )}
        </CardDescription>
      </CardHeader>
      <CardContent>
        {isLoading ? (
          <Skeleton className="h-16 w-full" />
        ) : (agents?.length ?? 0) === 0 ? (
          <p className="text-sm text-muted-foreground" data-testid="no-agents">
            {t(
              "privateLocations.agents.empty",
              "No agents enrolled yet. Mint an enrollment token above and start an agent with it.",
            )}
          </p>
        ) : (
          <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("privateLocations.agents.name", "Name")}</TableHead>
                  <TableHead>{t("privateLocations.agents.region", "Region")}</TableHead>
                  <TableHead>{t("privateLocations.agents.fingerprint", "Fingerprint")}</TableHead>
                  <TableHead>{t("privateLocations.agents.lastSeen", "Last seen")}</TableHead>
                  <TableHead>{t("privateLocations.agents.status", "Status")}</TableHead>
                  <TableHead className="w-16 text-right">
                    {t("privateLocations.agents.actions", "Actions")}
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {agents?.map((agent) => (
                  <TableRow key={agent.uid} data-testid={`agent-row-${agent.uid}`}>
                    <TableCell className="font-medium">{agent.name}</TableCell>
                    <TableCell>
                      <code className="rounded bg-muted px-1.5 py-0.5 text-xs">{agent.region}</code>
                    </TableCell>
                    <TableCell>
                      <code className="text-xs text-muted-foreground">{agent.fingerprint}</code>
                    </TableCell>
                    <TableCell
                      className="text-sm text-muted-foreground"
                      data-testid={`agent-last-seen-${agent.uid}`}
                    >
                      {agent.lastSeenAt ? (
                        <span title={new Date(agent.lastSeenAt).toLocaleString()}>
                          <LiveDurationAgo since={agent.lastSeenAt} />
                        </span>
                      ) : (
                        t("privateLocations.agents.never", "never")
                      )}
                    </TableCell>
                    <TableCell>
                      <Badge variant={agent.status === "active" ? "default" : "destructive"}>
                        {agent.status}
                      </Badge>
                    </TableCell>
                    <TableCell className="text-right">
                      {agent.status === "active" && (
                        <Button
                          variant="ghost"
                          size="icon"
                          title={t("privateLocations.agents.revoke", "Revoke agent")}
                          data-testid={`revoke-agent-${agent.uid}`}
                          onClick={() => setPendingRevoke(agent.uid)}
                        >
                          <Trash2 className="h-4 w-4 text-destructive" />
                        </Button>
                      )}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        )}
      </CardContent>

      <AlertDialog open={!!pendingRevoke} onOpenChange={(open) => !open && setPendingRevoke(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t("privateLocations.agents.revokeTitle", "Revoke this agent?")}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(
                "privateLocations.agents.revokeDescription",
                "The agent loses access immediately and stops receiving checks. It already saw the credentials sealed to it — rotate them. This cannot be undone; enroll a new agent to replace it.",
              )}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("privateLocations.cancel", "Cancel")}</AlertDialogCancel>
            <AlertDialogAction onClick={confirmRevoke} data-testid="confirm-revoke-agent">
              {t("privateLocations.agents.revokeConfirm", "Revoke")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Pending enrollment tokens
// ---------------------------------------------------------------------------

function TokensCard({ org }: { org: string }) {
  const { t } = useTranslation(["org"]);
  const { data: tokens, isLoading } = useEnrollmentTokens(org);
  const deleteToken = useDeleteEnrollmentToken(org);

  // The API also returns recently-used tokens (for the register wizard's
  // benefit); this card only shows the ones still waiting for an agent.
  const pendingTokens = tokens?.filter((token) => token.status !== "used") ?? [];

  if (isLoading || pendingTokens.length === 0) {
    return null; // nothing pending — keep the page quiet
  }

  return (
    <Card data-testid="pending-tokens-card">
      <CardHeader>
        <CardTitle>{t("privateLocations.tokens.title", "Pending enrollment tokens")}</CardTitle>
        <CardDescription>
          {t(
            "privateLocations.tokens.description",
            "Unused tokens waiting for an agent. Each enrolls exactly one agent, then expires.",
          )}
        </CardDescription>
      </CardHeader>
      <CardContent>
        <div className="overflow-x-auto">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("privateLocations.tokens.region", "Region")}</TableHead>
                <TableHead>{t("privateLocations.tokens.expires", "Expires")}</TableHead>
                <TableHead className="w-16 text-right">
                  {t("privateLocations.tokens.actions", "Actions")}
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {pendingTokens.map((token) => (
                <TableRow key={token.uid} data-testid={`token-row-${token.uid}`}>
                  <TableCell>
                    <code className="rounded bg-muted px-1.5 py-0.5 text-xs">{token.region}</code>
                  </TableCell>
                  <TableCell className="text-sm text-muted-foreground">
                    {new Date(token.expiresAt).toLocaleString()}
                  </TableCell>
                  <TableCell className="text-right">
                    <Button
                      variant="ghost"
                      size="icon"
                      title={t("privateLocations.tokens.cancel", "Cancel token")}
                      data-testid={`delete-token-${token.uid}`}
                      onClick={() => deleteToken.mutate(token.uid)}
                    >
                      <Trash2 className="h-4 w-4 text-destructive" />
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      </CardContent>
    </Card>
  );
}
