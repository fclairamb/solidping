import { useState, useEffect } from "react";
import { useTranslation } from "react-i18next";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { Switch } from "@/components/ui/switch";
import type { Connection, ConnectionType } from "@/api/hooks";

export interface ChannelFormState {
  name: string;
  enabled: boolean;
  isDefault: boolean;
  settings: Record<string, unknown>;
}

interface ChannelFormProps {
  type: ConnectionType;
  initial?: Connection | null;
  initialName?: string;
  onChange: (state: ChannelFormState) => void;
}

// ChannelForm is the type-dispatched edit surface. Common fields render
// once; a per-type panel slots in below for the channel-specific
// settings. Each panel keeps its own narrow shape — no anything-goes
// JSON editor.
export function ChannelForm({ type, initial, initialName, onChange }: ChannelFormProps) {
  const { t } = useTranslation("channels");
  const [name, setName] = useState(initial?.name || initialName || "");
  const [enabled, setEnabled] = useState(initial?.enabled ?? true);
  const [isDefault, setIsDefault] = useState(initial?.isDefault ?? false);
  const [settings, setSettings] = useState<Record<string, unknown>>(
    initial?.settings || {},
  );

  useEffect(() => {
    onChange({ name, enabled, isDefault, settings });
  }, [name, enabled, isDefault, settings, onChange]);

  return (
    <div className="space-y-4">
      <div className="space-y-2">
        <Label htmlFor="ch-name">{t("form.name", "Name")}</Label>
        <Input
          id="ch-name"
          value={name}
          required
          onChange={(e) => setName(e.target.value)}
          placeholder={t("form.namePlaceholder", "On-call alerts")}
        />
      </div>

      <PerTypePanel type={type} settings={settings} onChange={setSettings} />

      <div className="flex items-center justify-between rounded border p-3">
        <div>
          <Label htmlFor="ch-enabled" className="font-medium">
            {t("form.enabled", "Enabled")}
          </Label>
          <p className="text-xs text-muted-foreground">
            {t("form.enabledHelp", "Disabled channels never send notifications.")}
          </p>
        </div>
        <Switch id="ch-enabled" checked={enabled} onCheckedChange={setEnabled} />
      </div>

      <div className="flex items-center justify-between rounded border p-3">
        <div>
          <Label htmlFor="ch-default" className="font-medium">
            {t("form.default", "Default for new checks")}
          </Label>
          <p className="text-xs text-muted-foreground">
            {t(
              "form.defaultHelp",
              "Pre-checked when creating a new check. Existing checks are unaffected.",
            )}
          </p>
        </div>
        <Switch
          id="ch-default"
          checked={isDefault}
          onCheckedChange={setIsDefault}
        />
      </div>
    </div>
  );
}

interface PerTypePanelProps {
  type: ConnectionType;
  settings: Record<string, unknown>;
  onChange: (next: Record<string, unknown>) => void;
}

function PerTypePanel({ type, settings, onChange }: PerTypePanelProps) {
  const { t } = useTranslation("channels");

  const update = (key: string, value: unknown) =>
    onChange({ ...settings, [key]: value });

  switch (type) {
    case "webhook":
    case "discord":
    case "googlechat":
    case "mattermost":
      return (
        <UrlPanel
          label={t("form.webhookUrl", "Webhook URL")}
          value={(settings.webhook_url as string) || ""}
          onChange={(v) => update("webhook_url", v)}
        />
      );
    case "email":
      return (
        <div className="space-y-2">
          <Label htmlFor="ch-to">
            {t("form.recipients", "Recipients (one per line)")}
          </Label>
          <Textarea
            id="ch-to"
            rows={3}
            value={
              Array.isArray(settings.to)
                ? (settings.to as string[]).join("\n")
                : ""
            }
            onChange={(e) =>
              update(
                "to",
                e.target.value
                  .split("\n")
                  .map((s) => s.trim())
                  .filter(Boolean),
              )
            }
            placeholder="ops@example.com&#10;oncall@example.com"
          />
        </div>
      );
    case "ntfy":
      return (
        <div className="space-y-3">
          <UrlPanel
            label={t("form.ntfyServer", "Server URL")}
            value={(settings.server_url as string) || "https://ntfy.sh"}
            onChange={(v) => update("server_url", v)}
          />
          <div className="space-y-2">
            <Label htmlFor="ch-topic">{t("form.ntfyTopic", "Topic")}</Label>
            <Input
              id="ch-topic"
              value={(settings.topic as string) || ""}
              onChange={(e) => update("topic", e.target.value)}
              placeholder="solidping-alerts"
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="ch-priority">
              {t("form.ntfyPriority", "Priority (1-5, default 3)")}
            </Label>
            <Input
              id="ch-priority"
              type="number"
              min={1}
              max={5}
              value={(settings.priority as number) || 3}
              onChange={(e) =>
                update("priority", Number.parseInt(e.target.value, 10))
              }
            />
          </div>
        </div>
      );
    case "pushover":
      return (
        <div className="space-y-3">
          <SecretPanel
            id="ch-pushover-user"
            label={t("form.pushoverUser", "User key")}
            value={(settings.user as string) || ""}
            onChange={(v) => update("user", v)}
          />
          <SecretPanel
            id="ch-pushover-token"
            label={t("form.pushoverToken", "App token")}
            value={(settings.token as string) || ""}
            onChange={(v) => update("token", v)}
          />
        </div>
      );
    case "opsgenie":
      return (
        <div className="space-y-3">
          <SecretPanel
            id="ch-opsgenie-key"
            label={t("form.opsgenieKey", "API key")}
            value={(settings.api_key as string) || ""}
            onChange={(v) => update("api_key", v)}
          />
          <div className="space-y-2">
            <Label htmlFor="ch-opsgenie-team">
              {t("form.opsgenieTeam", "Team (optional)")}
            </Label>
            <Input
              id="ch-opsgenie-team"
              value={(settings.team as string) || ""}
              onChange={(e) => update("team", e.target.value)}
            />
          </div>
        </div>
      );
    case "slack":
      return (
        <div className="rounded border bg-muted/30 p-3 text-sm space-y-2">
          <p>
            {t(
              "form.slackOauthHint",
              "Slack channels are configured via the Slack OAuth install. The bot will populate workspace and channel names on completion.",
            )}
          </p>
          {typeof settings.team_name === "string" && settings.team_name !== "" && (
            <p>
              <strong>Workspace:</strong> {settings.team_name}
            </p>
          )}
          {typeof settings.channel_name === "string" && settings.channel_name !== "" && (
            <p>
              <strong>Channel:</strong> #{settings.channel_name}
            </p>
          )}
        </div>
      );
    default:
      return null;
  }
}

function UrlPanel({
  label,
  value,
  onChange,
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
}) {
  return (
    <div className="space-y-2">
      <Label htmlFor="ch-url">{label}</Label>
      <Input
        id="ch-url"
        type="url"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder="https://"
      />
    </div>
  );
}

function SecretPanel({
  id,
  label,
  value,
  onChange,
}: {
  id: string;
  label: string;
  value: string;
  onChange: (v: string) => void;
}) {
  return (
    <div className="space-y-2">
      <Label htmlFor={id}>{label}</Label>
      <Input
        id={id}
        type="password"
        autoComplete="new-password"
        value={value}
        onChange={(e) => onChange(e.target.value)}
      />
    </div>
  );
}
