import { useTranslation } from "react-i18next";
import { createFileRoute } from "@tanstack/react-router";
import { Loader2 } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { regionDisplayLabel } from "@/lib/region-label";
import { useAllAgents, useRegions, useVersion, type AgentInfo } from "@/api/hooks";
import { AgentVersionCell } from "@/components/shared/agent-version";

export const Route = createFileRoute("/orgs/$org/server/agents")({
  component: AgentsFleetPage,
});

// STALE_AFTER_MS is the client-side staleness threshold used to flag an
// agent whose last_seen_at is suspiciously old. Connected agents refresh
// last_seen_at on every ping tick, so a healthy agent's value is always
// fresh; the GC job only retires a silent system agent after 7 days, so
// without this visual cue a dead-for-days agent looks fine in the list.
const STALE_AFTER_MS = 5 * 60 * 1000;

function isStale(agent: AgentInfo): boolean {
  if (agent.status !== "active") return false;
  if (!agent.lastSeenAt) return true;
  return Date.now() - new Date(agent.lastSeenAt).getTime() > STALE_AFTER_MS;
}

function AgentsFleetPage() {
  const { t } = useTranslation(["server"]);
  const { org } = Route.useParams();
  const { data: agents, isLoading } = useAllAgents();
  // Region defs are org-scoped, but the display label falls back to the raw
  // slug for anything unmatched (e.g. a shared system-agent cloud region),
  // so borrowing the current org's catalog for labeling is fine here.
  const { data: regionsData } = useRegions(org);
  const { data: versionData } = useVersion();

  return (
    <Card data-testid="agents-fleet-card">
      <CardHeader>
        <CardTitle>{t("server:agents.title")}</CardTitle>
        <CardDescription>{t("server:agents.description")}</CardDescription>
      </CardHeader>
      <CardContent>
        {isLoading ? (
          <div className="flex items-center justify-center py-12">
            <Loader2 className="h-8 w-8 animate-spin text-muted-foreground" />
          </div>
        ) : !agents || agents.length === 0 ? (
          <p className="text-sm text-muted-foreground" data-testid="no-fleet-agents">
            {t("server:agents.empty")}
          </p>
        ) : (
          <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("server:agents.kind")}</TableHead>
                  <TableHead>{t("server:agents.org")}</TableHead>
                  <TableHead>{t("server:agents.name")}</TableHead>
                  <TableHead>{t("server:agents.region")}</TableHead>
                  <TableHead>{t("server:agents.fingerprint")}</TableHead>
                  <TableHead>{t("server:agents.version")}</TableHead>
                  <TableHead>{t("server:agents.lastSeen")}</TableHead>
                  <TableHead>{t("server:agents.status")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {agents.map((agent) => {
                  const stale = isStale(agent);

                  return (
                    <TableRow key={agent.uid} data-testid={`fleet-agent-row-${agent.uid}`}>
                      <TableCell>
                        <Badge variant={agent.kind === "system" ? "secondary" : "outline"}>
                          {agent.kind === "system"
                            ? t("server:agents.kindSystem")
                            : t("server:agents.kindOrg")}
                        </Badge>
                      </TableCell>
                      <TableCell className="text-sm text-muted-foreground">
                        {agent.org ?? t("server:agents.noOrg")}
                      </TableCell>
                      <TableCell className="font-medium">{agent.name}</TableCell>
                      <TableCell>
                        <code className="rounded bg-muted px-1.5 py-0.5 text-xs">
                          {regionDisplayLabel(regionsData?.regions, agent.region)}
                        </code>
                      </TableCell>
                      <TableCell>
                        <code className="text-xs text-muted-foreground">{agent.fingerprint}</code>
                      </TableCell>
                      <TableCell>
                        <AgentVersionCell
                          agentVersion={agent.version}
                          serverVersion={versionData?.version}
                          data-testid={`fleet-agent-version-${agent.uid}`}
                        />
                      </TableCell>
                      <TableCell className="text-sm text-muted-foreground">
                        <span
                          className={stale ? "text-yellow-600 dark:text-yellow-500 font-medium" : ""}
                        >
                          {agent.lastSeenAt
                            ? new Date(agent.lastSeenAt).toLocaleString()
                            : t("server:agents.never")}
                        </span>
                      </TableCell>
                      <TableCell>
                        <div className="flex flex-wrap items-center gap-1.5">
                          <Badge variant={agent.status === "active" ? "default" : "destructive"}>
                            {agent.status}
                          </Badge>
                          {stale && (
                            <Badge
                              variant="warning"
                              data-testid={`fleet-agent-stale-${agent.uid}`}
                            >
                              {t("server:agents.stale")}
                            </Badge>
                          )}
                        </div>
                      </TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          </div>
        )}
      </CardContent>
    </Card>
  );
}
