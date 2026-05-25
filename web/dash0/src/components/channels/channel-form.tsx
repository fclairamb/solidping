import { useState, useEffect, useRef } from "react";
import { useTranslation } from "react-i18next";
import {
  Check,
  ChevronsUpDown,
  Copy,
  Loader2,
  RefreshCw,
  Search,
  Send,
} from "lucide-react";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { Switch } from "@/components/ui/switch";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { cn } from "@/lib/utils";
import type {
  Channel,
  ConnectionType,
  SlackChannel,
  SlackUser,
  WebhookTestResult,
} from "@/api/hooks";
import {
  useSlackDestinations,
  useRotateWebhookSecret,
  useTestWebhookChannel,
} from "@/api/hooks";

export interface ChannelFormState {
  name: string;
  enabled: boolean;
  isDefault: boolean;
  settings: Record<string, unknown>;
}

interface ChannelFormProps {
  type: ConnectionType;
  initial?: Channel | null;
  initialName?: string;
  onChange: (state: ChannelFormState) => void;
  /** Org slug — passed through to the Slack destination picker */
  org?: string;
  /** Channel UID — if provided, enables the live Slack destination picker */
  channelUid?: string;
}

// ChannelForm is the type-dispatched edit surface. Common fields render
// once; a per-type panel slots in below for the channel-specific
// settings. Each panel keeps its own narrow shape — no anything-goes
// JSON editor.
export function ChannelForm({ type, initial, initialName, onChange, org, channelUid }: ChannelFormProps) {
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

      <PerTypePanel type={type} settings={settings} onChange={setSettings} org={org} channelUid={channelUid} />

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
  org?: string;
  channelUid?: string;
}

function PerTypePanel({ type, settings, onChange, org, channelUid }: PerTypePanelProps) {
  const { t } = useTranslation("channels");

  const update = (key: string, value: unknown) =>
    onChange({ ...settings, [key]: value });

  switch (type) {
    case "webhook":
      return (
        <div className="space-y-3">
          <UrlPanel
            label={t("form.webhookUrl", "Webhook URL")}
            value={(settings.url as string) || ""}
            onChange={(v) => update("url", v)}
          />
          {org && channelUid && (
            <WebhookSigningPanel
              settings={settings}
              org={org}
              channelUid={channelUid}
            />
          )}
        </div>
      );
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
        <SlackDestinationPanel
          settings={settings}
          onChange={onChange}
          org={org}
          channelUid={channelUid}
        />
      );
    case "freebox":
      return <FreeboxStatusPanel settings={settings} />;
    default:
      return null;
  }
}

// FreeboxStatusPanel is a read-only summary shown on the edit page —
// the actual pairing happens on the dedicated create flow and cannot
// be retried from here (re-pairing creates a new channel row).
function FreeboxStatusPanel({ settings }: { settings: Record<string, unknown> }) {
  const { t } = useTranslation("channels");
  const status = typeof settings.status === "string" ? settings.status : "";
  const baseUrl = typeof settings.baseUrl === "string" ? settings.baseUrl : "";

  return (
    <div className="rounded border bg-muted/30 p-3 text-sm space-y-2">
      {baseUrl && (
        <p>
          <strong>{t("freebox.baseUrl", "Freebox base URL")}:</strong> {baseUrl}
        </p>
      )}
      {status && (
        <p>
          <strong>{t("col.status", "Status")}:</strong>{" "}
          <span data-testid="freebox-status">{status}</span>
        </p>
      )}
      <p className="text-xs text-muted-foreground">
        {t(
          "freebox.baseUrlHint",
          "Leave as-is if SolidPing runs on the same LAN as the Freebox. For remote access, use the public hostname you configured in the Freebox admin under Settings → Freebox OS API.",
        )}
      </p>
    </div>
  );
}

// ---- Webhook signing-secret panel ----

interface WebhookSigningPanelProps {
  settings: Record<string, unknown>;
  org: string;
  channelUid: string;
}

// WebhookSigningPanel shows the per-channel Standard Webhooks signing secret
// (retrievable, not a one-time reveal), with copy + rotate actions, a
// rotation-in-progress banner, and a "send test" button reporting the result.
function WebhookSigningPanel({ settings, org, channelUid }: WebhookSigningPanelProps) {
  const { t } = useTranslation("channels");
  const rotate = useRotateWebhookSecret(org, channelUid);
  const test = useTestWebhookChannel(org);

  const [copied, setCopied] = useState(false);
  const [testResult, setTestResult] = useState<WebhookTestResult | null>(null);

  const secret =
    typeof settings.signingSecret === "string" ? settings.signingSecret : "";
  const previousExpiry =
    typeof settings.signingSecretPreviousExpiry === "string"
      ? settings.signingSecretPreviousExpiry
      : "";

  async function handleCopy() {
    if (!secret) return;
    try {
      await navigator.clipboard.writeText(secret);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      // Clipboard may be unavailable (insecure context) — silently ignore.
    }
  }

  function handleTest() {
    test.mutate(channelUid, {
      onSuccess: (res) => setTestResult(res),
      onError: (err) =>
        setTestResult({
          success: false,
          statusCode: 0,
          durationMs: 0,
          error: err instanceof Error ? err.message : String(err),
        }),
    });
  }

  return (
    <div
      className="space-y-3 rounded border bg-muted/30 p-3"
      data-testid="webhook-signing-panel"
    >
      <div className="space-y-2">
        <Label htmlFor="ch-signing-secret">
          {t("form.signingSecret", "Signing secret")}
        </Label>
        {secret ? (
          <div className="flex items-center gap-2">
            <Input
              id="ch-signing-secret"
              readOnly
              value={secret}
              className="font-mono text-xs"
              data-testid="webhook-signing-secret"
              onFocus={(e) => e.currentTarget.select()}
            />
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={handleCopy}
              data-testid="webhook-copy-secret"
            >
              <Copy className="mr-1 h-4 w-4" />
              {copied ? t("form.copied", "Copied") : t("form.copy", "Copy")}
            </Button>
            <Button
              type="button"
              variant="outline"
              size="sm"
              disabled={rotate.isPending}
              onClick={() => rotate.mutate()}
              data-testid="webhook-rotate-secret"
            >
              {rotate.isPending ? (
                <Loader2 className="mr-1 h-4 w-4 animate-spin" />
              ) : (
                <RefreshCw className="mr-1 h-4 w-4" />
              )}
              {t("form.rotate", "Rotate")}
            </Button>
          </div>
        ) : (
          <div className="space-y-2" data-testid="webhook-no-secret">
            <p className="text-xs text-muted-foreground">
              {t(
                "form.signingSecretMissing",
                "No signing secret — will be auto-generated on next delivery. Rotate to generate one now.",
              )}
            </p>
            <Button
              type="button"
              variant="outline"
              size="sm"
              disabled={rotate.isPending}
              onClick={() => rotate.mutate()}
              data-testid="webhook-rotate-secret"
            >
              {rotate.isPending ? (
                <Loader2 className="mr-1 h-4 w-4 animate-spin" />
              ) : (
                <RefreshCw className="mr-1 h-4 w-4" />
              )}
              {t("form.generate", "Generate")}
            </Button>
          </div>
        )}
        <p className="text-xs text-muted-foreground">
          {t(
            "form.signingSecretHelp",
            "Outbound webhooks are signed with this secret using the Standard Webhooks scheme (HMAC-SHA256). Verify it on the receiving end.",
          )}
        </p>
      </div>

      {previousExpiry && (
        <div
          className="rounded border border-yellow-500/40 bg-yellow-500/10 p-2 text-xs"
          data-testid="webhook-rotation-banner"
        >
          {t("form.rotationBanner", "Previous secret active until")}{" "}
          {new Date(previousExpiry).toLocaleString()}.{" "}
          {t("form.rotationBannerHint", "Remove it early by rotating again.")}
        </div>
      )}

      <div className="flex items-center gap-2">
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={test.isPending}
          onClick={handleTest}
          data-testid="webhook-send-test"
        >
          {test.isPending ? (
            <Loader2 className="mr-1 h-4 w-4 animate-spin" />
          ) : (
            <Send className="mr-1 h-4 w-4" />
          )}
          {t("form.sendTest", "Send test")}
        </Button>
        {testResult && (
          <Badge
            variant={testResult.success ? "success" : "destructive"}
            data-testid="webhook-test-result"
          >
            {testResult.success
              ? `${testResult.statusCode} OK · ${testResult.durationMs} ms`
              : `${testResult.statusCode || "—"} · ${testResult.durationMs} ms${
                  testResult.error ? ` — ${testResult.error}` : ""
                }`}
          </Badge>
        )}
      </div>
    </div>
  );
}

// ---- Slack destination picker ----

type SlackTab = "channel" | "dm";

interface SlackDestinationPanelProps {
  settings: Record<string, unknown>;
  onChange: (next: Record<string, unknown>) => void;
  org?: string;
  channelUid?: string;
}

function SlackDestinationPanel({ settings, onChange, org, channelUid }: SlackDestinationPanelProps) {
  const { t } = useTranslation("channels");

  // If no org/channelUid, we're on the new-channel page (Slack OAuth not yet complete).
  const isEditMode = Boolean(org && channelUid);

  // A channel is connected once the OAuth install has written its workspace
  // identity. team_id is a non-secret field present in the public settings for
  // OAuth-connected channels and absent for tokenless stubs.
  const isConnected =
    typeof settings.team_id === "string" && settings.team_id.length > 0;

  // Derive current tab from existing settings; default to "channel".
  const [activeTab, setActiveTab] = useState<SlackTab>(() => {
    const dt = settings.destination_type;
    return dt === "dm" ? "dm" : "channel";
  });

  // Gate the destinations fetch so a tokenless channel never triggers the
  // backend 409 / Slack API call.
  const { data, isLoading, isError } = useSlackDestinations(
    org ?? "",
    channelUid ?? "",
    isEditMode && isConnected,
  );

  // Current selection from settings
  const currentId = (settings.channel_id as string) || "";

  function handleSelect(tab: SlackTab, id: string, name: string) {
    const displayName = tab === "channel" ? `#${name}` : `@${name}`;
    onChange({
      ...settings,
      channel_id: id,
      channel_name: name,
      destination_type: tab,
      display_name: displayName,
    });
  }

  function handleTabSwitch(tab: SlackTab) {
    setActiveTab(tab);
    // Clear selection when switching tabs so the UI is consistent
    onChange({
      ...settings,
      channel_id: "",
      channel_name: "",
      destination_type: tab,
      display_name: "",
    });
  }

  // Show the workspace name if present
  const teamName = typeof settings.team_name === "string" ? settings.team_name : "";

  if (!isEditMode) {
    return (
      <div className="rounded border bg-muted/30 p-3 text-sm space-y-2">
        <p>
          {t(
            "form.slackOauthHint",
            "Slack channels are configured via the Slack OAuth install. The bot will populate workspace and channel names on completion.",
          )}
        </p>
      </div>
    );
  }

  // Editing a Slack channel that was never connected (e.g. a tokenless stub):
  // show an install CTA instead of the broken destination picker.
  if (!isConnected) {
    return (
      <div
        className="rounded border bg-muted/30 p-3 text-sm space-y-3"
        data-testid="slack-not-connected"
      >
        <div className="space-y-1">
          <p className="font-medium">
            {t("form.slackNotConnectedTitle", "Slack workspace not connected")}
          </p>
          <p className="text-muted-foreground">
            {t(
              "form.slackNotConnectedBody",
              "This channel has no linked Slack workspace. Install the SolidPing Slack app to connect one.",
            )}
          </p>
        </div>
        <Button
          type="button"
          onClick={() => {
            const params = new URLSearchParams({ source: "dashboard" });
            if (channelUid) params.set("channelUid", channelUid);
            if (org) params.set("org", org);
            window.location.href = `/api/v1/integrations/slack/install?${params.toString()}`;
          }}
          data-testid="slack-install"
        >
          {t("form.slackConnectButton", "Install Slack app")}
        </Button>
      </div>
    );
  }

  return (
    <div className="rounded border bg-muted/30 p-3 text-sm space-y-3">
      {teamName && (
        <p className="text-muted-foreground">
          <strong>Workspace:</strong> {teamName}
        </p>
      )}

      {/* Tab strip */}
      <div className="flex gap-1 rounded-md border bg-background p-0.5 w-fit">
        {(["channel", "dm"] as SlackTab[]).map((tab) => (
          <button
            key={tab}
            type="button"
            onClick={() => handleTabSwitch(tab)}
            className={cn(
              "rounded px-3 py-1 text-xs font-medium transition-colors",
              activeTab === tab
                ? "bg-primary text-primary-foreground"
                : "text-muted-foreground hover:text-foreground",
            )}
            data-testid={`slack-tab-${tab}`}
          >
            {tab === "channel" ? "Channel" : "Direct message"}
          </button>
        ))}
      </div>

      {/* Combobox or states */}
      {isLoading ? (
        <div className="flex items-center gap-2 text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin" />
          <span>{t("form.slackLoading", "Loading…")}</span>
        </div>
      ) : isError ? (
        <p className="text-destructive text-xs">
          {t(
            "form.slackError",
            "Could not connect to Slack workspace — re-install the bot.",
          )}
        </p>
      ) : activeTab === "channel" ? (
        <SlackChannelCombobox
          channels={data?.channels ?? []}
          currentId={currentId}
          onSelect={(ch) => handleSelect("channel", ch.id, ch.name)}
        />
      ) : (
        <SlackUserCombobox
          users={data?.users ?? []}
          currentId={currentId}
          onSelect={(u) => handleSelect("dm", u.id, u.realName || u.name)}
        />
      )}
    </div>
  );
}

// ---- Channel combobox ----

interface SlackChannelComboboxProps {
  channels: SlackChannel[];
  currentId: string;
  onSelect: (ch: SlackChannel) => void;
}

function SlackChannelCombobox({ channels, currentId, onSelect }: SlackChannelComboboxProps) {
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState("");
  const searchRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (open) {
      setTimeout(() => searchRef.current?.focus(), 0);
    }
  }, [open]);

  const filtered = channels.filter((ch) =>
    ch.name.toLowerCase().includes(search.toLowerCase()),
  );

  const selected = channels.find((ch) => ch.id === currentId);
  const label = selected ? `#${selected.name}` : "Pick a channel…";

  if (channels.length === 0) {
    return (
      <p className="text-xs text-muted-foreground">
        Invite the bot to a channel first with{" "}
        <code className="font-mono">/invite @solidping</code>.
      </p>
    );
  }

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          variant="outline"
          role="combobox"
          aria-expanded={open}
          className="w-full justify-between font-normal text-sm"
          data-testid="slack-channel-combobox"
        >
          <span className={cn(!selected && "text-muted-foreground")}>{label}</span>
          <ChevronsUpDown className="ml-2 h-4 w-4 shrink-0 opacity-50" />
        </Button>
      </PopoverTrigger>
      <PopoverContent className="p-0 w-[280px]" align="start">
        <div className="flex items-center border-b px-3 py-2">
          <Search className="mr-2 h-4 w-4 shrink-0 opacity-50" />
          <input
            ref={searchRef}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Search channels…"
            className="flex h-8 w-full bg-transparent text-sm outline-none placeholder:text-muted-foreground"
            data-testid="slack-channel-search"
          />
        </div>
        <div className="max-h-56 overflow-y-auto p-1">
          {filtered.length === 0 ? (
            <div className="px-3 py-2 text-sm text-muted-foreground">No channels found</div>
          ) : (
            filtered.map((ch) => (
              <button
                key={ch.id}
                type="button"
                role="option"
                aria-selected={ch.id === currentId}
                className={cn(
                  "flex w-full items-start gap-2 rounded-sm px-2 py-1.5 text-left text-sm hover:bg-accent cursor-pointer",
                  ch.id === currentId && "bg-accent",
                )}
                onClick={() => {
                  onSelect(ch);
                  setOpen(false);
                  setSearch("");
                }}
                data-testid={`slack-channel-option-${ch.id}`}
              >
                <Check
                  className={cn(
                    "mt-0.5 h-4 w-4 shrink-0",
                    ch.id === currentId ? "opacity-100" : "opacity-0",
                  )}
                />
                <div>
                  <div className="font-medium">#{ch.name}</div>
                  {ch.isPrivate && (
                    <div className="text-xs text-muted-foreground">Private</div>
                  )}
                  {!ch.isMember && (
                    <div className="text-xs text-amber-600">
                      Bot not in channel — run /invite @solidping first
                    </div>
                  )}
                </div>
              </button>
            ))
          )}
        </div>
      </PopoverContent>
    </Popover>
  );
}

// ---- User (DM) combobox ----

interface SlackUserComboboxProps {
  users: SlackUser[];
  currentId: string;
  onSelect: (u: SlackUser) => void;
}

function SlackUserCombobox({ users, currentId, onSelect }: SlackUserComboboxProps) {
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState("");
  const searchRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (open) {
      setTimeout(() => searchRef.current?.focus(), 0);
    }
  }, [open]);

  const filtered = users.filter(
    (u) =>
      (u.realName || u.name).toLowerCase().includes(search.toLowerCase()) ||
      u.name.toLowerCase().includes(search.toLowerCase()),
  );

  const selected = users.find((u) => u.id === currentId);
  const label = selected ? `@${selected.realName || selected.name}` : "Pick a person…";

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          variant="outline"
          role="combobox"
          aria-expanded={open}
          className="w-full justify-between font-normal text-sm"
          data-testid="slack-user-combobox"
        >
          <span className={cn(!selected && "text-muted-foreground")}>{label}</span>
          <ChevronsUpDown className="ml-2 h-4 w-4 shrink-0 opacity-50" />
        </Button>
      </PopoverTrigger>
      <PopoverContent className="p-0 w-[280px]" align="start">
        <div className="flex items-center border-b px-3 py-2">
          <Search className="mr-2 h-4 w-4 shrink-0 opacity-50" />
          <input
            ref={searchRef}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Search people…"
            className="flex h-8 w-full bg-transparent text-sm outline-none placeholder:text-muted-foreground"
            data-testid="slack-user-search"
          />
        </div>
        <div className="max-h-56 overflow-y-auto p-1">
          {filtered.length === 0 ? (
            <div className="px-3 py-2 text-sm text-muted-foreground">No people found</div>
          ) : (
            filtered.map((u) => (
              <button
                key={u.id}
                type="button"
                role="option"
                aria-selected={u.id === currentId}
                className={cn(
                  "flex w-full items-start gap-2 rounded-sm px-2 py-1.5 text-left text-sm hover:bg-accent cursor-pointer",
                  u.id === currentId && "bg-accent",
                )}
                onClick={() => {
                  onSelect(u);
                  setOpen(false);
                  setSearch("");
                }}
                data-testid={`slack-user-option-${u.id}`}
              >
                <Check
                  className={cn(
                    "mt-0.5 h-4 w-4 shrink-0",
                    u.id === currentId ? "opacity-100" : "opacity-0",
                  )}
                />
                <div className="font-medium">@{u.realName || u.name}</div>
              </button>
            ))
          )}
        </div>
      </PopoverContent>
    </Popover>
  );
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
