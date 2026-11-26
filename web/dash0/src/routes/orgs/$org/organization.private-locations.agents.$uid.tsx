import { useState } from "react";
import { useTranslation } from "react-i18next";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { ArrowLeft, Loader2 } from "lucide-react";
import { toast } from "sonner";

import { useAgents, useUpdateAgent, type AgentInfo } from "@/api/hooks";
import { ApiError } from "@/api/client";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { QueryErrorView } from "@/components/shared/error-views";

// Agent edit page: the name is display only (the agent's SP_AGENT_NAME is
// only read at enrollment).
export const Route = createFileRoute(
  "/orgs/$org/organization/private-locations/agents/$uid",
)({
  component: AgentEditPage,
});

function AgentEditPage() {
  const { t } = useTranslation(["org"]);
  const { org, uid } = Route.useParams();
  const {
    data: agents,
    isLoading,
    error,
    refetch,
  } = useAgents(org, { refetchInterval: 0 });
  const agent = agents?.find((row) => row.uid === uid);

  if (error) {
    return (
      <QueryErrorView
        error={error}
        org={org}
        resource={t("privateLocations.agents.title")}
        backTo="/orgs/$org/organization/private-locations"
        backLabel={t("privateLocations.reseal.back")}
        onRetry={() => refetch()}
      />
    );
  }

  if (isLoading) {
    return <Skeleton className="h-48 max-w-2xl" />;
  }

  if (!agent) {
    return (
      <p
        className="text-sm text-muted-foreground"
        data-testid="agent-edit-not-found"
      >
        {t("privateLocations.agentEdit.notFound")}
      </p>
    );
  }

  return <AgentEditForm org={org} agent={agent} />;
}

function AgentEditForm({ org, agent }: { org: string; agent: AgentInfo }) {
  const { t } = useTranslation(["org", "common"]);
  const navigate = useNavigate();
  const updateAgent = useUpdateAgent(org);
  const [name, setName] = useState(agent.name);

  const goBack = () =>
    navigate({
      to: "/orgs/$org/organization/private-locations",
      params: { org },
    });

  const onSubmit = async (event: React.FormEvent) => {
    event.preventDefault();
    try {
      await updateAgent.mutateAsync({ uid: agent.uid, name: name.trim() });
      toast.success(t("privateLocations.agentEdit.saved"));
      goBack();
    } catch (err) {
      toast.error(
        err instanceof ApiError
          ? err.message
          : t("privateLocations.agentEdit.saveFailed"),
      );
    }
  };

  return (
    <form
      onSubmit={onSubmit}
      className="max-w-2xl space-y-4"
      data-testid="agent-edit-form"
    >
      <Button
        asChild
        variant="ghost"
        size="sm"
        data-testid="agent-edit-back-button"
      >
        <Link
          to="/orgs/$org/organization/private-locations"
          params={{ org }}
          data-testid="agent-edit-back-link"
        >
          <ArrowLeft className="h-4 w-4" />
          {t("privateLocations.reseal.back")}
        </Link>
      </Button>
      <Card>
        <CardHeader>
          <CardTitle>{t("privateLocations.agentEdit.title")}</CardTitle>
          <CardDescription>
            {t("privateLocations.agentEdit.description")}
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="agent-edit-name">
              {t("privateLocations.agents.name")}
            </Label>
            <Input
              id="agent-edit-name"
              data-testid="agent-edit-name-input"
              value={name}
              onChange={(event) => setName(event.target.value)}
              maxLength={100}
              required
            />
          </div>
          <div className="space-y-1 text-sm text-muted-foreground">
            <p>
              {t("privateLocations.agents.region")}:{" "}
              <code className="rounded bg-muted px-1.5 py-0.5 text-xs">
                {agent.region}
              </code>
            </p>
            <p>
              {t("privateLocations.agents.fingerprint")}:{" "}
              <code className="rounded bg-muted px-1.5 py-0.5 text-xs">
                {agent.fingerprint}
              </code>
            </p>
          </div>
        </CardContent>
      </Card>
      <div className="flex justify-end gap-2">
        <Button
          type="button"
          variant="outline"
          onClick={goBack}
          data-testid="agent-edit-cancel"
        >
          {t("common:cancel")}
        </Button>
        <Button
          type="submit"
          disabled={updateAgent.isPending || !name.trim()}
          data-testid="agent-edit-submit"
        >
          {updateAgent.isPending && (
            <Loader2 className="mr-2 h-4 w-4 animate-spin" />
          )}
          {t("common:save")}
        </Button>
      </div>
    </form>
  );
}
