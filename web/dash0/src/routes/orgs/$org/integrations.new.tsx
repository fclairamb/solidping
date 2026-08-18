import { useState } from "react";
import { useTranslation } from "react-i18next";
import { createFileRoute, useNavigate, Link } from "@tanstack/react-router";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  CardDescription,
} from "@/components/ui/card";
import { Loader2 } from "lucide-react";
import { toast } from "sonner";
import {
  useCreateIntegration,
  startSlackInstall,
  canNotify,
  canSource,
  type ConnectionType,
} from "@/api/hooks";
import {
  IntegrationIcon,
  integrationLabel,
} from "@/components/integrations/integration-icon";
import {
  IntegrationForm,
  type IntegrationFormState,
} from "@/components/integrations/integration-form";
import { FreeboxForm } from "@/components/integrations/freebox-form";

interface NewIntegrationSearch {
  type?: ConnectionType;
}

export const Route = createFileRoute("/orgs/$org/integrations/new")({
  validateSearch: (search: Record<string, unknown>): NewIntegrationSearch => ({
    type: search.type as ConnectionType | undefined,
  }),
  component: NewIntegrationPage,
});

const ALL_TYPES: ConnectionType[] = [
  "slack",
  "discord",
  "webhook",
  "email",
  "googlechat",
  "mattermost",
  "msteams",
  "msteams-bot",
  "ntfy",
  "matrix",
  "opsgenie",
  "pushover",
  "twilio",
  "freebox",
  "webpush",
  "kubernetes",
];

function NewIntegrationPage() {
  const { t } = useTranslation("integrations");
  const { org } = Route.useParams();
  const search = Route.useSearch();
  const navigate = useNavigate();
  const create = useCreateIntegration(org);

  const [type, setType] = useState<ConnectionType | null>(search.type ?? null);
  const [form, setForm] = useState<IntegrationFormState | null>(null);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!type || !form || form.isValid === false) return;

    try {
      const created = await create.mutateAsync({
        type,
        name: form.name,
        enabled: form.enabled,
        isDefault: form.isDefault,
        settings: form.settings,
      });
      toast.success(t("created", "Integration created"));
      navigate({
        to: "/orgs/$org/integrations/$integrationUid",
        params: { org, integrationUid: created.uid },
      });
    } catch {
      toast.error(t("createFailed", "Failed to create integration"));
    }
  };

  if (!type) {
    // Group the picker by capability so operators understand the distinction
    // between a notification sink ("channel") and a data source. Mirrors the
    // backend capability registry via the frontend CAPABILITIES map.
    const notifyTypes = ALL_TYPES.filter((c) => canNotify(c));
    const sourceTypes = ALL_TYPES.filter((c) => canSource(c));

    const renderGroup = (groupTypes: ConnectionType[], testid: string) => (
      <div
        className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3"
        data-testid={testid}
      >
        {groupTypes.map((c) => (
          <button
            key={c}
            type="button"
            onClick={() => setType(c)}
            className="flex items-center gap-3 rounded border p-4 text-left hover:bg-accent"
            data-testid={`pick-${c}`}
          >
            <IntegrationIcon type={c} className="h-6 w-6 text-muted-foreground" />
            <div>
              <div className="font-medium">{integrationLabel(c)}</div>
              <div className="text-xs text-muted-foreground">
                {t(`hint.${c}`, "")}
              </div>
            </div>
          </button>
        ))}
      </div>
    );

    return (
      <div className="space-y-6">
        <div className="flex items-start justify-between gap-4">
          <div>
            <h1 className="text-2xl font-bold tracking-tight">
              {t("newTitle", "Add an integration")}
            </h1>
            <p className="text-sm text-muted-foreground">
              {t("newSubtitle", "Pick the integration type to configure.")}
            </p>
          </div>
          <Button asChild variant="ghost" size="sm">
            <Link to="/orgs/$org/integrations" params={{ org }}>
              {t("cancel", "Cancel")}
            </Link>
          </Button>
        </div>
        {notifyTypes.length > 0 && (
          <div className="space-y-3">
            <h2 className="text-sm font-semibold text-muted-foreground">
              {t("groupNotify", "Notification channels")}
            </h2>
            {renderGroup(notifyTypes, "group-notify")}
          </div>
        )}
        {sourceTypes.length > 0 && (
          <div className="space-y-3">
            <h2 className="text-sm font-semibold text-muted-foreground">
              {t("groupSource", "Data sources")}
            </h2>
            {renderGroup(sourceTypes, "group-source")}
          </div>
        )}
      </div>
    );
  }

  // Freebox uses a dedicated form because pairing is a two-step
  // handshake (LCD approval) rather than a regular create — we don't
  // know the app_token until the Freebox grants it.
  if (type === "freebox") {
    return (
      <Card className="max-w-xl">
        <CardHeader>
          <div className="flex items-center justify-between gap-4">
            <CardTitle className="flex items-center gap-2">
              <IntegrationIcon type={type} className="h-5 w-5" />
              {integrationLabel(type)}
            </CardTitle>
            <Button asChild variant="ghost" size="sm">
              <Link to="/orgs/$org/integrations" params={{ org }}>
                {t("cancel", "Cancel")}
              </Link>
            </Button>
          </div>
          <CardDescription>
            {t(
              "freebox.description",
              "Monitor your Freebox line quality and network status",
            )}
          </CardDescription>
        </CardHeader>
        <CardContent>
          <FreeboxForm org={org} onCancel={() => setType(null)} />
        </CardContent>
      </Card>
    );
  }

  // Slack is install-only: a manually-created integration has no bot token and
  // is permanently broken. Instead of a create form, render an "install the
  // Slack app" CTA that does a full-page redirect to the OAuth install flow.
  if (type === "slack") {
    return (
      <Card className="max-w-xl">
        <CardHeader>
          <div className="flex items-center justify-between gap-4">
            <CardTitle className="flex items-center gap-2">
              <IntegrationIcon type={type} className="h-5 w-5" />
              {integrationLabel(type)}
            </CardTitle>
            <Button asChild variant="ghost" size="sm">
              <Link to="/orgs/$org/integrations" params={{ org }}>
                {t("cancel", "Cancel")}
              </Link>
            </Button>
          </div>
          <CardDescription>
            {t(
              "form.slackNotConnectedBody",
              "This channel has no linked Slack workspace. Install the SolidPing Slack app to connect one.",
            )}
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-wrap gap-2">
          <Button
            type="button"
            onClick={() => {
              void startSlackInstall(org).catch(() => {
                toast.error(
                  t("form.slackInstallFailed", "Failed to start Slack install"),
                );
              });
            }}
            data-testid="slack-install"
          >
            {t("form.slackConnectButton", "Install Slack app")}
          </Button>
          <Button type="button" variant="outline" onClick={() => setType(null)}>
            {t("changeType", "Change type")}
          </Button>
        </CardContent>
      </Card>
    );
  }

  return (
    <Card className="max-w-xl">
      <CardHeader>
        <div className="flex items-center justify-between gap-4">
          <CardTitle className="flex items-center gap-2">
            <IntegrationIcon type={type} className="h-5 w-5" />
            {integrationLabel(type)}
          </CardTitle>
          <Button asChild variant="ghost" size="sm">
            <Link to="/orgs/$org/integrations" params={{ org }}>
              {t("cancel", "Cancel")}
            </Link>
          </Button>
        </div>
        <CardDescription>
          {t("newFormSubtitle", "Configure the integration and save.")}
        </CardDescription>
      </CardHeader>
      <CardContent>
        <form onSubmit={handleSubmit} className="space-y-5">
          <IntegrationForm
            type={type}
            initialName={integrationLabel(type)}
            onChange={setForm}
          />
          <div className="flex justify-end gap-2">
            <Button
              type="button"
              variant="outline"
              onClick={() => setType(null)}
              disabled={create.isPending}
            >
              {t("changeType", "Change type")}
            </Button>
            <Button
              type="submit"
              disabled={
                create.isPending || !form?.name || form?.isValid === false
              }
            >
              {create.isPending ? (
                <>
                  <Loader2 className="h-4 w-4 mr-1 animate-spin" />
                  {t("creating", "Creating…")}
                </>
              ) : (
                t("create", "Create integration")
              )}
            </Button>
          </div>
        </form>
      </CardContent>
    </Card>
  );
}
