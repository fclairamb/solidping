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
  useCreateChannel,
  type ConnectionType,
} from "@/api/hooks";
import { ChannelIcon, channelLabel } from "@/components/channels/channel-icon";
import {
  ChannelForm,
  type ChannelFormState,
} from "@/components/channels/channel-form";
import { FreeboxForm } from "@/components/channels/freebox-form";

interface NewChannelSearch {
  type?: ConnectionType;
}

export const Route = createFileRoute("/orgs/$org/channels/new")({
  validateSearch: (search: Record<string, unknown>): NewChannelSearch => ({
    type: search.type as ConnectionType | undefined,
  }),
  component: NewChannelPage,
});

const ALL_TYPES: ConnectionType[] = [
  "slack",
  "discord",
  "webhook",
  "email",
  "googlechat",
  "mattermost",
  "ntfy",
  "opsgenie",
  "pushover",
  "freebox",
];

function NewChannelPage() {
  const { t } = useTranslation("channels");
  const { org } = Route.useParams();
  const search = Route.useSearch();
  const navigate = useNavigate();
  const create = useCreateChannel(org);

  const [type, setType] = useState<ConnectionType | null>(search.type ?? null);
  const [form, setForm] = useState<ChannelFormState | null>(null);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!type || !form) return;

    try {
      const created = await create.mutateAsync({
        type,
        name: form.name,
        enabled: form.enabled,
        isDefault: form.isDefault,
        settings: form.settings,
      });
      toast.success(t("created", "Channel created"));
      navigate({
        to: "/orgs/$org/channels/$channelUid",
        params: { org, channelUid: created.uid },
      });
    } catch {
      toast.error(t("createFailed", "Failed to create channel"));
    }
  };

  if (!type) {
    return (
      <div className="space-y-4">
        <div className="flex items-start justify-between gap-4">
          <div>
            <h1 className="text-2xl font-bold tracking-tight">
              {t("newTitle", "Add a notification channel")}
            </h1>
            <p className="text-sm text-muted-foreground">
              {t("newSubtitle", "Pick the channel type to configure.")}
            </p>
          </div>
          <Button asChild variant="ghost" size="sm">
            <Link to="/orgs/$org/channels" params={{ org }}>
              {t("cancel", "Cancel")}
            </Link>
          </Button>
        </div>
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {ALL_TYPES.map((c) => (
            <button
              key={c}
              type="button"
              onClick={() => setType(c)}
              className="flex items-center gap-3 rounded border p-4 text-left hover:bg-accent"
              data-testid={`pick-${c}`}
            >
              <ChannelIcon type={c} className="h-6 w-6 text-muted-foreground" />
              <div>
                <div className="font-medium">{channelLabel(c)}</div>
                <div className="text-xs text-muted-foreground">
                  {t(`hint.${c}`, "")}
                </div>
              </div>
            </button>
          ))}
        </div>
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
              <ChannelIcon type={type} className="h-5 w-5" />
              {channelLabel(type)}
            </CardTitle>
            <Button asChild variant="ghost" size="sm">
              <Link to="/orgs/$org/channels" params={{ org }}>
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

  // Slack is install-only: a manually-created channel has no bot token and is
  // permanently broken. Instead of a create form, render an "install the Slack
  // app" CTA that does a full-page redirect to the OAuth install flow.
  if (type === "slack") {
    return (
      <Card className="max-w-xl">
        <CardHeader>
          <div className="flex items-center justify-between gap-4">
            <CardTitle className="flex items-center gap-2">
              <ChannelIcon type={type} className="h-5 w-5" />
              {channelLabel(type)}
            </CardTitle>
            <Button asChild variant="ghost" size="sm">
              <Link to="/orgs/$org/channels" params={{ org }}>
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
              window.location.href =
                "/api/v1/integrations/slack/install?source=dashboard";
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
            <ChannelIcon type={type} className="h-5 w-5" />
            {channelLabel(type)}
          </CardTitle>
          <Button asChild variant="ghost" size="sm">
            <Link to="/orgs/$org/channels" params={{ org }}>
              {t("cancel", "Cancel")}
            </Link>
          </Button>
        </div>
        <CardDescription>
          {t("newFormSubtitle", "Configure the channel and save.")}
        </CardDescription>
      </CardHeader>
      <CardContent>
        <form onSubmit={handleSubmit} className="space-y-5">
          <ChannelForm
            type={type}
            initialName={channelLabel(type)}
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
              disabled={create.isPending || !form?.name}
            >
              {create.isPending ? (
                <>
                  <Loader2 className="h-4 w-4 mr-1 animate-spin" />
                  {t("creating", "Creating…")}
                </>
              ) : (
                t("create", "Create channel")
              )}
            </Button>
          </div>
        </form>
      </CardContent>
    </Card>
  );
}
