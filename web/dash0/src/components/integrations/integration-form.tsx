import { useState, useEffect, useRef } from "react";
import { useTranslation } from "react-i18next";
import {
  Check,
  ChevronsUpDown,
  Copy,
  Loader2,
  MonitorSmartphone,
  RefreshCw,
  AlertTriangle,
  CheckCircle2,
  Download,
  Info,
  Search,
  Send,
  Trash2,
} from "lucide-react";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { RecipientsInput } from "@/components/shared/recipients-input";
import { TokenChipsInput } from "@/components/shared/token-chips-input";
import { isValidEmail } from "@/lib/email";
import { Switch } from "@/components/ui/switch";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";
import { toast } from "sonner";
import type {
  Integration,
  ConnectionType,
  SlackChannel,
  SlackUser,
  MSTeamsDestination,
  IntegrationTestResult,
  IntegrationIdentity,
} from "@/api/hooks";
import {
  useSlackDestinations,
  startSlackInstall,
  useRotateWebhookSecret,
  useTestIntegration,
  useDiscordDestinations,
  useMSTeamsBotDestinations,
  useMSTeamsBotStatus,
  startDiscordInstall,
  type DiscordChannel,
  useStartMSTeamsLink,
  downloadMSTeamsManifest,
  useIntegrationIdentities,
  useSyncIntegrationIdentities,
  useSetIntegrationIdentity,
  useDeleteIntegrationIdentity,
} from "@/api/hooks";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { WebPushEnableButton } from "@/components/notifications/WebPushEnableButton";
import { deriveDeviceLabel } from "@/lib/browser-detection";

export interface IntegrationFormState {
  name: string;
  enabled: boolean;
  isDefault: boolean;
  settings: Record<string, unknown>;
  /**
   * Whether the per-type settings panel considers its current values valid.
   * Defaults to true for types with no client-side validation. The email
   * panel sets this to false while any recipient fails isValidEmail, so the
   * caller can block Save/Create until every address is fixed.
   */
  isValid?: boolean;
}

interface IntegrationFormProps {
  type: ConnectionType;
  initial?: Integration | null;
  initialName?: string;
  onChange: (state: IntegrationFormState) => void;
  /** Org slug — passed through to the Slack destination picker */
  org?: string;
  /** Channel UID — if provided, enables the live Slack destination picker */
  channelUid?: string;
  /**
   * Whether the Send-test button may run. The test always uses the persisted
   * settings, so the edit page passes `!isDirty` here to block testing while
   * there are unsaved edits. Defaults to true (e.g. the create flow).
   */
  canTest?: boolean;
}

// IntegrationForm is the type-dispatched edit surface. Common fields render
// once; a per-type panel slots in below for the channel-specific
// settings. Each panel keeps its own narrow shape — no anything-goes
// JSON editor.
export function IntegrationForm({ type, initial, initialName, onChange, org, channelUid, canTest = true }: IntegrationFormProps) {
  const { t } = useTranslation("integrations");
  const [name, setName] = useState(initial?.name || initialName || "");
  const [enabled, setEnabled] = useState(initial?.enabled ?? true);
  // Create flow (initial === null) starts enabled: most users setting up a
  // notification channel want it wired into new checks by default. Edit flows
  // pass a real `initial`, so the stored value is respected.
  const [isDefault, setIsDefault] = useState(initial?.isDefault ?? true);
  const [settings, setSettings] = useState<Record<string, unknown>>(
    initial?.settings || {},
  );

  // Sync settings when the server-side channel data changes (e.g. after
  // a secret rotation mutation invalidates and refetches the query).
  useEffect(() => {
    setSettings(initial?.settings ?? {});
  }, [initial]);

  // Per-type client-side validity. Only the email panel has any today: every
  // stored recipient must pass isValidEmail. Other types have nothing to
  // validate client-side, so they're always valid.
  const isValid =
    type === "email"
      ? (Array.isArray(settings.to) ? (settings.to as unknown[]) : []).every(
          (v) => typeof v === "string" && isValidEmail(v),
        )
      : true;

  useEffect(() => {
    onChange({ name, enabled, isDefault, settings, isValid });
  }, [name, enabled, isDefault, settings, isValid, onChange]);

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

      <PerTypePanel
        type={type}
        settings={settings}
        onChange={setSettings}
        org={org}
        channelUid={channelUid}
        privateKeys={initial?.settingsPrivateKeys}
        canTest={canTest}
      />

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

      {/* Test delivery — available on every notifiable integration once it
          exists (channelUid present). Data sources (Freebox, Kubernetes) are
          not notification targets: Freebox has nothing to test, and Kubernetes
          has its own "test connection" probe inside its panel. */}
      {channelUid && type !== "freebox" && type !== "kubernetes" && (
        <TestNotificationSection
          org={org}
          channelUid={channelUid}
          canTest={canTest}
        />
      )}
    </div>
  );
}

interface TestNotificationSectionProps {
  org?: string;
  channelUid: string;
  /**
   * Whether testing is allowed. Disabled while the form has unsaved edits,
   * because the test runs against the persisted settings, not the current
   * (unsaved) form state.
   */
  canTest?: boolean;
}

// TestNotificationSection sends a sample notification through the saved
// integration and shows whether it was delivered. It tests the persisted
// settings, so unsaved form edits are not reflected until saved.
function TestNotificationSection({ org, channelUid, canTest = true }: TestNotificationSectionProps) {
  const { t } = useTranslation("integrations");
  const test = useTestIntegration(org ?? "");
  const [testResult, setTestResult] = useState<IntegrationTestResult | null>(
    null,
  );

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
      className="space-y-2 rounded border p-3"
      data-testid="integration-test-section"
    >
      <div className="flex items-center justify-between gap-3">
        <div>
          <Label className="font-medium">
            {t("form.testTitle", "Send a test notification")}
          </Label>
          <p className="text-xs text-muted-foreground">
            {t(
              "form.testHelp",
              "Deliver a sample alert to confirm this integration is wired up. The test uses the saved settings.",
            )}
          </p>
        </div>
        {/* A natively-disabled button emits no hover events, so the tooltip
            trigger wraps the button in a focusable span. The tooltip warns
            only when disabled due to unsaved edits (not while a test is in
            flight). */}
        <Tooltip>
          <TooltipTrigger asChild>
            <span tabIndex={0} className="inline-flex">
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={!canTest || test.isPending}
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
            </span>
          </TooltipTrigger>
          {!canTest && (
            <TooltipContent data-testid="webhook-send-test-tooltip">
              {t(
                "form.testNeedsSave",
                "Save your changes first — the test uses the saved settings.",
              )}
            </TooltipContent>
          )}
        </Tooltip>
      </div>
      {testResult && (
        <Badge
          variant={testResult.success ? "success" : "destructive"}
          data-testid="webhook-test-result"
        >
          {testResult.success
            ? testResult.error
              ? `${testResult.durationMs} ms — ${testResult.error}`
              : testResult.statusCode
                ? `${testResult.statusCode} OK · ${testResult.durationMs} ms`
                : `${t("form.testDelivered", "Delivered")} · ${testResult.durationMs} ms`
            : `${testResult.statusCode || "—"} · ${testResult.durationMs} ms${
                testResult.error ? ` — ${testResult.error}` : ""
              }`}
        </Badge>
      )}
    </div>
  );
}

interface PerTypePanelProps {
  type: ConnectionType;
  settings: Record<string, unknown>;
  onChange: (next: Record<string, unknown>) => void;
  org?: string;
  channelUid?: string;
  /** Names of the settings keys stored encrypted (so secret inputs can render
   *  a "leave blank to keep" hint instead of echoing the value). */
  privateKeys?: string[];
  /** Whether a "test connection" probe may run (false while edits unsaved). */
  canTest?: boolean;
}

function PerTypePanel({ type, settings, onChange, org, channelUid, privateKeys, canTest }: PerTypePanelProps) {
  const { t } = useTranslation("integrations");

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
      return (
        <DiscordDestinationPanel
          settings={settings}
          onChange={onChange}
          org={org}
          channelUid={channelUid}
        />
      );
    case "googlechat":
    case "mattermost":
      return (
        <UrlPanel
          label={t("form.webhookUrl", "Webhook URL")}
          value={(settings.webhook_url as string) || ""}
          onChange={(v) => update("webhook_url", v)}
        />
      );
    case "msteams":
      return (
        <div className="space-y-2">
          <UrlPanel
            label={t("form.webhookUrl", "Webhook URL")}
            value={(settings.webhook_url as string) || ""}
            onChange={(v) => update("webhook_url", v)}
          />
          <p className="text-xs text-muted-foreground">
            {t(
              "form.msteamsWebhookHint",
              'In Teams, open Workflows → "Post to a channel when a webhook request is received", finish the wizard, then paste the workflow URL it gives you here. The legacy "Incoming Webhook" connector is retired and will not work.',
            )}
          </p>
        </div>
      );
    case "email": {
      const recipients = Array.isArray(settings.to)
        ? (settings.to as string[])
        : [];
      const hasInvalid = recipients.some((v) => !isValidEmail(v));
      return (
        <div className="space-y-2">
          <Label htmlFor="ch-to">
            {t("form.recipients", "Recipients")}
          </Label>
          <RecipientsInput
            id="ch-to"
            value={recipients}
            onChange={(next) => update("to", next)}
            placeholder={t(
              "form.recipientsPlaceholder",
              "ops@example.com, oncall@example.com",
            )}
            data-testid="email-recipients"
          />
          <p className="text-xs text-muted-foreground">
            {t(
              "form.recipientsHint",
              "Separate addresses with a space, comma, semicolon, or newline — or paste a list.",
            )}
          </p>
          {hasInvalid && (
            <p
              className="text-xs text-destructive"
              data-testid="email-recipients-error"
            >
              {t(
                "form.recipientsInvalidHint",
                "Fix or remove the highlighted address before saving.",
              )}
            </p>
          )}
        </div>
      );
    }
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
    case "matrix":
      return (
        <div className="space-y-3">
          <div className="space-y-2">
            <Label htmlFor="ch-matrix-homeserver">
              {t("form.matrixHomeserver", "Homeserver URL")}
            </Label>
            <Input
              id="ch-matrix-homeserver"
              type="url"
              value={(settings.homeserverUrl as string) || ""}
              onChange={(e) => update("homeserverUrl", e.target.value)}
              placeholder="https://matrix.org"
            />
          </div>
          <SecretPanel
            id="ch-matrix-token"
            label={t("form.matrixAccessToken", "Access token")}
            value={(settings.accessToken as string) || ""}
            onChange={(v) => update("accessToken", v)}
          />
          <div className="space-y-2">
            <Label htmlFor="ch-matrix-room">{t("form.matrixRoom", "Room")}</Label>
            <Input
              id="ch-matrix-room"
              value={(settings.roomId as string) || ""}
              onChange={(e) => update("roomId", e.target.value)}
              placeholder="!abcdef:matrix.org"
            />
            <p className="text-xs text-muted-foreground">
              {t(
                "form.matrixRoomHint",
                "Room ID (!room:server) or alias (#room:server). Invite the bot to the room first.",
              )}
            </p>
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
    case "pagerduty":
      return (
        <div className="space-y-3">
          <SecretPanel
            id="ch-pagerduty-key"
            label={t("form.pagerdutyKey", "Integration key")}
            value={(settings.routing_key as string) || ""}
            onChange={(v) => update("routing_key", v)}
          />
          <p className="text-xs text-muted-foreground">
            {t(
              "form.pagerdutyKeyHint",
              "PagerDuty → Service → Integrations → Add integration → Events API v2. Paste the generated integration key here.",
            )}
          </p>
        </div>
      );
    case "twilio":
      return (
        <TwilioPanel settings={settings} update={update} />
      );
    case "msteams-bot":
      return (
        <MSTeamsBotPanel
          settings={settings}
          onChange={onChange}
          org={org}
          channelUid={channelUid}
        />
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
    case "kubernetes":
      return (
        <KubernetesPanel
          settings={settings}
          onChange={onChange}
          org={org}
          channelUid={channelUid}
          privateKeys={privateKeys}
          canTest={canTest}
        />
      );
    case "webpush":
      return (
        <WebPushChannelPanel
          settings={settings}
          onChange={onChange}
          org={org}
          isEdit={!!channelUid}
        />
      );
    default:
      return null;
  }
}

// FreeboxStatusPanel is a read-only summary shown on the edit page —
// the actual pairing happens on the dedicated create flow and cannot
// be retried from here (re-pairing creates a new channel row).
function FreeboxStatusPanel({ settings }: { settings: Record<string, unknown> }) {
  const { t } = useTranslation("integrations");
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

// ---- Kubernetes cluster panel ----

type KubeAuthMode = "token" | "kubeconfig" | "inCluster";

interface KubernetesPanelProps {
  settings: Record<string, unknown>;
  onChange: (next: Record<string, unknown>) => void;
  org?: string;
  channelUid?: string;
  privateKeys?: string[];
  canTest?: boolean;
}

// KubernetesPanel collects the public cluster settings (apiServer, caCert,
// insecureSkipTLSVerify, inCluster) and the write-only secret (token or pasted
// kubeconfig). Secrets are never echoed back: when one is already stored
// (present in privateKeys), the input shows a "leave blank to keep" hint and an
// empty value is omitted from the patch so the stored secret is preserved.
function KubernetesPanel({
  settings,
  onChange,
  org,
  channelUid,
  privateKeys,
  canTest = true,
}: KubernetesPanelProps) {
  const { t } = useTranslation("integrations");

  const hasStoredToken = privateKeys?.includes("token") ?? false;
  const hasStoredKubeconfig = privateKeys?.includes("kubeconfig") ?? false;

  // Derive the initial auth mode: in-cluster wins, then a stored kubeconfig,
  // else token (the default for a fresh form).
  const initialMode: KubeAuthMode = settings.inCluster
    ? "inCluster"
    : hasStoredKubeconfig
      ? "kubeconfig"
      : "token";
  const [mode, setMode] = useState<KubeAuthMode>(initialMode);

  const update = (key: string, value: unknown) =>
    onChange({ ...settings, [key]: value });

  // Switching auth mode rewrites the relevant settings so we never send a
  // mismatched combination (e.g. an apiServer alongside inCluster).
  const handleModeChange = (next: KubeAuthMode) => {
    setMode(next);
    const base = { ...settings };
    delete base.token;
    delete base.kubeconfig;

    if (next === "inCluster") {
      onChange({ ...base, inCluster: true });
      return;
    }

    delete base.inCluster;
    onChange(base);
  };

  return (
    <div className="space-y-3" data-testid="kubernetes-panel">
      <div className="space-y-2">
        <Label htmlFor="k8s-auth-mode">
          {t("kubernetes.authMode", "Authentication")}
        </Label>
        <Select value={mode} onValueChange={(v) => handleModeChange(v as KubeAuthMode)}>
          <SelectTrigger id="k8s-auth-mode" data-testid="kubernetes-auth-mode">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="token">
              {t("kubernetes.authToken", "API server + token")}
            </SelectItem>
            <SelectItem value="kubeconfig">
              {t("kubernetes.authKubeconfig", "Kubeconfig")}
            </SelectItem>
            <SelectItem value="inCluster">
              {t("kubernetes.authInCluster", "In-cluster (this cluster)")}
            </SelectItem>
          </SelectContent>
        </Select>
      </div>

      {mode === "inCluster" && (
        <p
          className="rounded border bg-muted/30 p-3 text-xs text-muted-foreground"
          data-testid="kubernetes-incluster-hint"
        >
          {t(
            "kubernetes.inClusterHint",
            "Uses the service-account token mounted in SolidPing's own pod. Only works when SolidPing runs inside the target cluster; no credentials are stored.",
          )}
        </p>
      )}

      {mode === "token" && (
        <>
          <div className="space-y-2">
            <Label htmlFor="k8s-api-server">
              {t("kubernetes.apiServer", "API server URL")}
            </Label>
            <Input
              id="k8s-api-server"
              type="url"
              value={(settings.apiServer as string) || ""}
              onChange={(e) => update("apiServer", e.target.value)}
              placeholder="https://10.0.0.1:6443"
              data-testid="kubernetes-api-server"
            />
          </div>
          <KubeSecretField
            id="k8s-token"
            label={t("kubernetes.token", "Bearer token")}
            value={(settings.token as string) || ""}
            onChange={(v) => update("token", v)}
            stored={hasStoredToken}
            multiline={false}
            testid="kubernetes-token"
          />
          <KubeTlsFields settings={settings} update={update} />
        </>
      )}

      {mode === "kubeconfig" && (
        <KubeSecretField
          id="k8s-kubeconfig"
          label={t("kubernetes.kubeconfig", "Kubeconfig (YAML)")}
          value={(settings.kubeconfig as string) || ""}
          onChange={(v) => update("kubeconfig", v)}
          stored={hasStoredKubeconfig}
          multiline
          testid="kubernetes-kubeconfig"
        />
      )}

      {channelUid && (
        <KubernetesTestSection
          org={org}
          channelUid={channelUid}
          canTest={canTest}
        />
      )}
    </div>
  );
}

// KubeTlsFields renders the CA cert + insecure-skip-tls-verify controls shared
// by the token auth mode.
function KubeTlsFields({
  settings,
  update,
}: {
  settings: Record<string, unknown>;
  update: (key: string, value: unknown) => void;
}) {
  const { t } = useTranslation("integrations");
  const insecure = settings.insecureSkipTLSVerify === true;

  return (
    <>
      <div className="space-y-2">
        <Label htmlFor="k8s-ca-cert">
          {t("kubernetes.caCert", "CA certificate (PEM, optional)")}
        </Label>
        <Textarea
          id="k8s-ca-cert"
          rows={4}
          value={(settings.caCert as string) || ""}
          onChange={(e) => update("caCert", e.target.value)}
          placeholder="-----BEGIN CERTIFICATE-----"
          className="font-mono text-xs"
          disabled={insecure}
          data-testid="kubernetes-ca-cert"
        />
      </div>
      <div className="flex items-center justify-between rounded border p-3">
        <div>
          <Label htmlFor="k8s-insecure" className="font-medium">
            {t("kubernetes.insecure", "Skip TLS verification")}
          </Label>
          <p className="text-xs text-muted-foreground">
            {t(
              "kubernetes.insecureHelp",
              "Disables API-server certificate verification. Use only for clusters with a self-signed cert you cannot pin.",
            )}
          </p>
        </div>
        <Switch
          id="k8s-insecure"
          checked={insecure}
          onCheckedChange={(v) => update("insecureSkipTLSVerify", v)}
          data-testid="kubernetes-insecure"
        />
      </div>
    </>
  );
}

// KubeSecretField is a write-only secret input (single-line password or
// multiline textarea). When a secret is already stored it shows a placeholder
// + hint and starts empty; submitting an empty value preserves the stored
// secret (the backend's PATCH-merge omits absent secret keys).
function KubeSecretField({
  id,
  label,
  value,
  onChange,
  stored,
  multiline,
  testid,
}: {
  id: string;
  label: string;
  value: string;
  onChange: (v: string) => void;
  stored: boolean;
  multiline: boolean;
  testid: string;
}) {
  const { t } = useTranslation("integrations");
  const placeholder = stored
    ? t("kubernetes.secretStored", "•••••••• (stored — leave blank to keep)")
    : "";

  return (
    <div className="space-y-2">
      <Label htmlFor={id}>{label}</Label>
      {multiline ? (
        <Textarea
          id={id}
          rows={8}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          placeholder={placeholder || "apiVersion: v1\nkind: Config\n..."}
          className="font-mono text-xs"
          data-testid={testid}
        />
      ) : (
        <Input
          id={id}
          type="password"
          autoComplete="new-password"
          value={value}
          onChange={(e) => onChange(e.target.value)}
          placeholder={placeholder}
          data-testid={testid}
        />
      )}
    </div>
  );
}

// KubernetesTestSection probes the saved cluster connection (the /test
// endpoint hits the cluster's /version). Like the notification test, it runs
// against persisted settings, so it is disabled while there are unsaved edits.
function KubernetesTestSection({
  org,
  channelUid,
  canTest = true,
}: {
  org?: string;
  channelUid: string;
  canTest?: boolean;
}) {
  const { t } = useTranslation("integrations");
  const test = useTestIntegration(org ?? "");
  const [result, setResult] = useState<IntegrationTestResult | null>(null);

  function handleTest() {
    test.mutate(channelUid, {
      onSuccess: (res) => setResult(res),
      onError: (err) =>
        setResult({
          success: false,
          statusCode: 0,
          durationMs: 0,
          error: err instanceof Error ? err.message : String(err),
        }),
    });
  }

  return (
    <div
      className="space-y-2 rounded border p-3"
      data-testid="kubernetes-test-section"
    >
      <div className="flex items-center justify-between gap-3">
        <div>
          <Label className="font-medium">
            {t("kubernetes.testTitle", "Test cluster connection")}
          </Label>
          <p className="text-xs text-muted-foreground">
            {t(
              "kubernetes.testHelp",
              "Probe the cluster API to confirm the credentials work. Uses the saved settings.",
            )}
          </p>
        </div>
        <Tooltip>
          <TooltipTrigger asChild>
            <span tabIndex={0} className="inline-flex">
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={!canTest || test.isPending}
                onClick={handleTest}
                data-testid="kubernetes-send-test"
              >
                {test.isPending ? (
                  <Loader2 className="mr-1 h-4 w-4 animate-spin" />
                ) : (
                  <Send className="mr-1 h-4 w-4" />
                )}
                {t("kubernetes.testButton", "Test connection")}
              </Button>
            </span>
          </TooltipTrigger>
          {!canTest && (
            <TooltipContent>
              {t(
                "form.testNeedsSave",
                "Save your changes first — the test uses the saved settings.",
              )}
            </TooltipContent>
          )}
        </Tooltip>
      </div>
      {result && (
        <Badge
          variant={result.success ? "success" : "destructive"}
          data-testid="kubernetes-test-result"
        >
          {result.success
            ? `${result.detail || t("kubernetes.connected", "Connected")} · ${result.durationMs} ms`
            : `${t("kubernetes.failed", "Failed")} · ${result.durationMs} ms${
                result.error ? ` — ${result.error}` : ""
              }`}
        </Badge>
      )}
    </div>
  );
}

// ---- Web Push channel panel ----

interface WebPushSub {
  endpoint: string;
  keys: { p256dh: string; auth: string };
  label?: string;
}

interface WebPushChannelPanelProps {
  settings: Record<string, unknown>;
  onChange: (next: Record<string, unknown>) => void;
  org?: string;
  isEdit?: boolean;
}

/** Manages the list of browser subscriptions for a webpush org channel. */
function WebPushChannelPanel({ settings, onChange, org, isEdit: _isEdit }: WebPushChannelPanelProps) {
  const { t } = useTranslation("integrations");

  const subs: WebPushSub[] = Array.isArray(settings.subscriptions)
    ? (settings.subscriptions as WebPushSub[])
    : [];

  const handleRemove = (endpoint: string) => {
    const updated = subs.filter((s) => s.endpoint !== endpoint);
    onChange({ ...settings, subscriptions: updated });
  };

  const handleSubscription = (subscriptionJson: string) => {
    try {
      const parsed = JSON.parse(subscriptionJson) as WebPushSub;
      // Deduplicate by endpoint.
      const already = subs.some((s) => s.endpoint === parsed.endpoint);
      if (!already) {
        onChange({
          ...settings,
          subscriptions: [...subs, { ...parsed, label: deriveDeviceLabel() }],
        });
      }
    } catch {
      // ignore malformed JSON
    }
  };

  return (
    <div className="space-y-3" data-testid="webpush-channel-panel">
      <p className="text-sm text-muted-foreground">
        {t("hint.webpush", "Receive alerts as browser notifications on your subscribed devices")}
      </p>

      {subs.length === 0 ? (
        <p className="text-sm text-muted-foreground italic">
          No devices subscribed yet. Click the button below to add this browser.
        </p>
      ) : (
        <div className="space-y-2" data-testid="webpush-subscriptions-list">
          {subs.map((sub) => (
            <div key={sub.endpoint} className="flex items-center gap-2 rounded border px-3 py-2">
              <MonitorSmartphone className="h-4 w-4 text-muted-foreground flex-none" />
              <span className="flex-1 text-sm truncate">{sub.label || "Browser"}</span>
              <button
                type="button"
                onClick={() => handleRemove(sub.endpoint)}
                className="text-destructive hover:text-destructive/80"
                aria-label="Remove subscription"
                data-testid="remove-webpush-subscription"
              >
                <Trash2 className="h-4 w-4" />
              </button>
            </div>
          ))}
        </div>
      )}

      {org && (
        <WebPushEnableButton
          org={org}
          onSubscription={handleSubscription}
          data-testid="webpush-subscribe-button"
        />
      )}
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
  const { t } = useTranslation("integrations");
  const rotate = useRotateWebhookSecret(org, channelUid);

  const [copied, setCopied] = useState(false);

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
    </div>
  );
}

// ---- Slack destination picker ----

type SlackTab = "channel" | "dm";

// ---- Discord ----

interface DiscordDestinationPanelProps {
  settings: Record<string, unknown>;
  onChange: (next: Record<string, unknown>) => void;
  org?: string;
  channelUid?: string;
}

/**
 * Settings panel for a Discord integration.
 *
 * It covers both Discord modes, because both are live at once and which one an
 * integration is in is a property of its data, not of a separate type:
 *
 *  - Bot mode (a guild is recorded): channel picker, on-call mentions, comment
 *    ingestion — everything the Slack panel offers.
 *  - Legacy webhook mode: the webhook URL field, kept exactly where it was so
 *    an org that never installs the bot sees no change at all.
 *
 * Both are shown together when both are configured: an org migrating from a
 * webhook to the bot needs to see the webhook it is about to stop using.
 */
function DiscordDestinationPanel({
  settings,
  onChange,
  org,
  channelUid,
}: DiscordDestinationPanelProps) {
  const { t } = useTranslation("integrations");

  const isEditMode = Boolean(org && channelUid);
  const guildId = typeof settings.guild_id === "string" ? settings.guild_id : "";
  const guildName =
    typeof settings.guild_name === "string" ? settings.guild_name : "";
  const isConnected = guildId.length > 0;

  const { data, isLoading, isError } = useDiscordDestinations(
    org ?? "",
    channelUid ?? "",
    isEditMode && isConnected,
  );

  const currentId = (settings.channel_id as string) || "";

  const webhookField = (
    <UrlPanel
      label={t("form.webhookUrl", "Webhook URL")}
      value={(settings.webhook_url as string) || ""}
      onChange={(v) => onChange({ ...settings, webhook_url: v })}
    />
  );

  if (!isConnected) {
    return (
      <div className="space-y-3" data-testid="discord-not-connected">
        <div className="rounded border bg-muted/30 p-3 text-sm space-y-3">
          <div className="space-y-1">
            <p className="font-medium">
              {t("form.discordNotConnectedTitle", "Discord server not connected")}
            </p>
            <p className="text-muted-foreground">
              {t(
                "form.discordNotConnectedBody",
                "Install the SolidPing bot to get threads, an Acknowledge button, on-call mentions and inbound comments. Without it, alerts are delivered one-way through the webhook URL below.",
              )}
            </p>
          </div>
          {isEditMode && (
            <Button
              type="button"
              onClick={() => {
                if (!org) return;
                void startDiscordInstall(org, channelUid).catch(() => {
                  toast.error(
                    t("form.discordInstallFailed", "Failed to start Discord install"),
                  );
                });
              }}
              data-testid="discord-install"
            >
              {t("form.discordConnectButton", "Install Discord bot")}
            </Button>
          )}
        </div>
        {webhookField}
      </div>
    );
  }

  return (
    <div className="space-y-3" data-testid="discord-connected">
      <div className="rounded border bg-muted/30 p-3 text-sm space-y-3">
        {guildName && (
          <p className="text-muted-foreground">
            <strong>{t("form.discordServer", "Server")}:</strong> {guildName}
          </p>
        )}

        {isLoading ? (
          <div className="flex items-center gap-2 text-muted-foreground">
            <Loader2 className="h-4 w-4 animate-spin" />
            <span>{t("form.discordLoading", "Loading channels…")}</span>
          </div>
        ) : isError ? (
          <p className="text-destructive text-xs">
            {t(
              "form.discordError",
              "Could not reach this Discord server — re-install the bot.",
            )}
          </p>
        ) : (
          <DiscordChannelCombobox
            channels={data?.channels ?? []}
            currentId={currentId}
            onSelect={(ch) =>
              onChange({
                ...settings,
                channel_id: ch.id,
                channel_name: ch.name,
              })
            }
          />
        )}

        <DiscordMentionSwitch settings={settings} onChange={onChange} />
        <DiscordCommentIngestionSwitch settings={settings} onChange={onChange} />

        {org && channelUid && currentId && (
          <SlackMemberMapping
            org={org}
            integrationUid={channelUid}
            workspaceUsers={[]}
            variant="discord"
          />
        )}
      </div>

      {typeof settings.webhook_url === "string" &&
        settings.webhook_url.length > 0 &&
        webhookField}
    </div>
  );
}

interface DiscordChannelComboboxProps {
  channels: DiscordChannel[];
  currentId: string;
  onSelect: (ch: DiscordChannel) => void;
}

function DiscordChannelCombobox({
  channels,
  currentId,
  onSelect,
}: DiscordChannelComboboxProps) {
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

  if (channels.length === 0) {
    return (
      <p className="text-xs text-muted-foreground">
        No text channels the bot can see. Give it access to a channel in Discord
        first.
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
          data-testid="discord-channel-combobox"
        >
          <span className={cn(!selected && "text-muted-foreground")}>
            {selected ? `#${selected.name}` : "Pick a channel…"}
          </span>
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
            data-testid="discord-channel-search"
          />
        </div>
        <div className="max-h-56 overflow-y-auto p-1">
          {filtered.length === 0 ? (
            <div className="px-3 py-2 text-sm text-muted-foreground">
              No channels found
            </div>
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
                data-testid={`discord-channel-option-${ch.id}`}
              >
                <Check
                  className={cn(
                    "mt-0.5 h-4 w-4 shrink-0",
                    ch.id === currentId ? "opacity-100" : "opacity-0",
                  )}
                />
                <div className="font-medium">#{ch.name}</div>
              </button>
            ))
          )}
        </div>
      </PopoverContent>
    </Popover>
  );
}

interface DiscordSwitchProps {
  settings: Record<string, unknown>;
  onChange: (next: Record<string, unknown>) => void;
}

function DiscordMentionSwitch({ settings, onChange }: DiscordSwitchProps) {
  const { t } = useTranslation("integrations");

  const checked = settings.mention_on_call === true;

  return (
    <div className="flex items-start justify-between gap-3 rounded border bg-background p-3">
      <div>
        <Label htmlFor="discord-mention-on-call" className="font-medium">
          {t("form.discordMentionOnCall", "Mention the on-call person in alerts")}
        </Label>
        <p className="text-xs text-muted-foreground">
          {t(
            "form.discordMentionOnCallHelp",
            "New and escalated incident messages start by @-mentioning whoever the escalation policy pages first. Members without a mapped Discord account are named in plain text.",
          )}
        </p>
      </div>
      <Switch
        id="discord-mention-on-call"
        checked={checked}
        onCheckedChange={(value) =>
          onChange({ ...settings, mention_on_call: value })
        }
        data-testid="discord-mention-on-call"
      />
    </div>
  );
}

function DiscordCommentIngestionSwitch({
  settings,
  onChange,
}: DiscordSwitchProps) {
  const { t } = useTranslation("integrations");

  const checked = settings.comment_ingestion === "all";

  return (
    <div className="flex items-start justify-between gap-3 rounded border bg-background p-3">
      <div>
        <Label htmlFor="discord-comment-ingestion" className="font-medium">
          {t(
            "form.discordCommentIngestion",
            "Capture every thread reply as a comment",
          )}
        </Label>
        <p className="text-xs text-muted-foreground">
          {t(
            "form.discordCommentIngestionHelp",
            "Off (recommended): only an explicit comment command becomes an incident comment, so triage chatter stays chatter. On: every human reply in a tracked incident thread is saved to the incident timeline. Requires the Discord Gateway and the MESSAGE_CONTENT intent.",
          )}
        </p>
      </div>
      <Switch
        id="discord-comment-ingestion"
        checked={checked}
        onCheckedChange={(value) =>
          onChange({
            ...settings,
            comment_ingestion: value ? "all" : "explicit",
          })
        }
        data-testid="discord-comment-ingestion"
      />
    </div>
  );
}

interface SlackDestinationPanelProps {
  settings: Record<string, unknown>;
  onChange: (next: Record<string, unknown>) => void;
  org?: string;
  channelUid?: string;
}

function SlackDestinationPanel({ settings, onChange, org, channelUid }: SlackDestinationPanelProps) {
  const { t } = useTranslation("integrations");

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
      <div className="rounded border bg-muted/30 p-3 text-sm space-y-3">
        <p>
          {t(
            "form.slackOauthHint",
            "Slack channels are configured via the Slack OAuth install. The bot will populate workspace and channel names on completion.",
          )}
        </p>
        <MentionOnCallSwitch settings={settings} onChange={onChange} isCreate />
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
            if (!org) return;
            void startSlackInstall(org, channelUid).catch(() => {
              toast.error(
                t("form.slackInstallFailed", "Failed to start Slack install"),
              );
            });
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

      <SlackDmCaptureNotice
        settings={settings}
        org={org}
        channelUid={channelUid}
      />

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

      <MentionOnCallSwitch settings={settings} onChange={onChange} />

      <CommentIngestionSwitch settings={settings} onChange={onChange} />

      {org && channelUid && (
        <SlackMemberMapping
          org={org}
          integrationUid={channelUid}
          workspaceUsers={data?.users ?? []}
        />
      )}
    </div>
  );
}

// ---- Mention the on-call person ----

interface MentionOnCallSwitchProps {
  settings: Record<string, unknown>;
  onChange: (next: Record<string, unknown>) => void;
  /**
   * True on the create page. A brand-new Slack integration defaults to
   * mentioning the on-call person (spec 2026-08-12-03); an existing one whose
   * settings never carried the key keeps the historical "no mentions" behavior,
   * so the switch must not silently appear enabled there.
   */
  isCreate?: boolean;
}

function MentionOnCallSwitch({
  settings,
  onChange,
  isCreate = false,
}: MentionOnCallSwitchProps) {
  const { t } = useTranslation("integrations");

  const stored = settings.mention_on_call;
  const checked = typeof stored === "boolean" ? stored : isCreate;

  return (
    <div className="flex items-start justify-between gap-3 rounded border bg-background p-3">
      <div>
        <Label htmlFor="slack-mention-on-call" className="font-medium">
          {t("form.slackMentionOnCall", "Mention the on-call person in alerts")}
        </Label>
        <p className="text-xs text-muted-foreground">
          {t(
            "form.slackMentionOnCallHelp",
            "New and escalated incident messages start by @-mentioning whoever the escalation policy pages first. Members without a mapped Slack account are named in plain text.",
          )}
        </p>
      </div>
      <Switch
        id="slack-mention-on-call"
        checked={checked}
        onCheckedChange={(value) =>
          onChange({ ...settings, mention_on_call: value })
        }
        data-testid="slack-mention-on-call"
      />
    </div>
  );
}

// ---- Inbound comment ingestion ----

interface CommentIngestionSwitchProps {
  settings: Record<string, unknown>;
  onChange: (next: Record<string, unknown>) => void;
}

/**
 * Chooses how inbound Slack thread replies are treated. Off (the default, and
 * the meaning of an absent value) is "explicit": only `/comment` creates an
 * incident comment. On restores the historical capture-every-reply behavior.
 */
function CommentIngestionSwitch({
  settings,
  onChange,
}: CommentIngestionSwitchProps) {
  const { t } = useTranslation("integrations");

  const checked = settings.comment_ingestion === "all";

  return (
    <div className="flex items-start justify-between gap-3 rounded border bg-background p-3">
      <div>
        <Label htmlFor="slack-comment-ingestion" className="font-medium">
          {t(
            "form.slackCommentIngestion",
            "Capture every thread reply as a comment",
          )}
        </Label>
        <p className="text-xs text-muted-foreground">
          {t(
            "form.slackCommentIngestionHelp",
            "Off (recommended): only an explicit /comment becomes an incident comment, so triage chatter stays chatter. On: every human reply in a tracked incident thread is saved to the incident timeline.",
          )}
        </p>
      </div>
      <Switch
        id="slack-comment-ingestion"
        checked={checked}
        onCheckedChange={(value) =>
          onChange({
            ...settings,
            comment_ingestion: value ? "all" : "explicit",
          })
        }
        data-testid="slack-comment-ingestion"
      />
    </div>
  );
}

// ---- Member mapping ----

interface SlackMemberMappingProps {
  org: string;
  integrationUid: string;
  workspaceUsers: SlackUser[];
  /**
   * "slack" offers a workspace-user picker per row. "discord" does not: a bot
   * cannot look a Discord user up by email, so mappings come from members who
   * signed in with Discord (re-sync picks those up). Rendering a picker with
   * no options would be a dead control that looks broken.
   */
  variant?: "slack" | "discord";
}

/**
 * Shows which org members SolidPing can @-mention on this workspace. Purely an
 * identity surface — it never displays a phone number or any other contact
 * value, because identities are "who I am there", not "how to page me".
 */
function SlackMemberMapping({
  org,
  integrationUid,
  workspaceUsers,
  variant = "slack",
}: SlackMemberMappingProps) {
  const { t } = useTranslation("integrations");
  const { data, isLoading, isError } = useIntegrationIdentities(
    org,
    integrationUid,
  );
  const sync = useSyncIntegrationIdentities(org, integrationUid);
  const setIdentity = useSetIntegrationIdentity(org, integrationUid);
  const clearIdentity = useDeleteIntegrationIdentity(org, integrationUid);

  const identities = data?.data ?? [];
  const matched = identities.filter((i) => i.status === "matched");
  const unmatched = identities.filter((i) => i.status !== "matched");

  const runSync = () => {
    sync.mutate(undefined, {
      onSuccess: (result) => {
        toast.success(
          t("form.slackMappingSynced", {
            defaultValue:
              "{{matched}} matched, {{notFound}} not found, {{ambiguous}} ambiguous",
            matched: result.matchedCount,
            notFound: result.notFoundCount,
            ambiguous: result.ambiguousCount,
          }),
        );
      },
      onError: () =>
        toast.error(t("form.slackMappingSyncFailed", "Member sync failed")),
    });
  };

  return (
    <div className="rounded border bg-background p-3 space-y-3">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <p className="font-medium">
            {t("form.slackMemberMapping", "Member mapping")}
          </p>
          <p className="text-xs text-muted-foreground">
            {variant === "discord"
              ? t(
                  "form.discordMemberMappingHelp",
                  "Which SolidPing members we can @-mention in this server. Members who signed in with Discord are matched automatically; re-sync to pick up new ones.",
                )
              : t(
                  "form.slackMemberMappingHelp",
                  "Which SolidPing members we can @-mention on this workspace. Matched automatically by email; override any row manually.",
                )}
          </p>
        </div>
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={runSync}
          disabled={sync.isPending}
          data-testid="slack-mapping-sync"
        >
          {sync.isPending ? (
            <Loader2 className="mr-2 h-4 w-4 animate-spin" />
          ) : (
            <RefreshCw className="mr-2 h-4 w-4" />
          )}
          {t("form.slackMappingResync", "Re-sync")}
        </Button>
      </div>

      {isLoading ? (
        <div className="flex items-center gap-2 text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin" />
          <span>{t("form.slackLoading", "Loading…")}</span>
        </div>
      ) : isError ? (
        <p className="text-destructive text-xs">
          {t("form.slackMappingError", "Could not load the member mapping.")}
        </p>
      ) : identities.length === 0 ? (
        <p className="text-xs text-muted-foreground">
          {t("form.slackMappingEmpty", "No organization members yet.")}
        </p>
      ) : (
        <div className="space-y-3" data-testid="slack-member-mapping">
          <p className="text-xs text-muted-foreground">
            {t("form.slackMappingCounts", {
              defaultValue: "{{matched}} matched · {{unmatched}} not mapped",
              matched: matched.length,
              unmatched: unmatched.length,
            })}
          </p>
          <ul className="divide-y rounded border">
            {identities.map((identity) => (
              <li
                key={identity.userUid}
                className="flex flex-col gap-2 p-2 sm:flex-row sm:items-center sm:justify-between"
                data-testid={`slack-mapping-row-${identity.email}`}
              >
                <div className="min-w-0">
                  <p className="truncate text-sm font-medium">
                    {identity.name || identity.email}
                  </p>
                  <p className="truncate text-xs text-muted-foreground">
                    {identity.email}
                  </p>
                </div>
                <div className="flex flex-wrap items-center gap-2">
                  <IdentityStatusBadge identity={identity} />
                  <div
                    className={cn(
                      "w-full sm:w-[220px]",
                      variant === "discord" && "hidden",
                    )}
                  >
                    <SlackUserCombobox
                      users={workspaceUsers}
                      currentId={identity.externalId ?? ""}
                      onSelect={(u) =>
                        setIdentity.mutate(
                          {
                            userUid: identity.userUid,
                            externalId: u.id,
                            displayName: u.realName || u.name,
                          },
                          {
                            onError: (err) =>
                              toast.error(
                                err instanceof Error
                                  ? err.message
                                  : t(
                                      "form.slackMappingSetFailed",
                                      "Could not save that mapping",
                                    ),
                              ),
                          },
                        )
                      }
                    />
                  </div>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    disabled={!identity.externalId || clearIdentity.isPending}
                    onClick={() => clearIdentity.mutate(identity.userUid)}
                    aria-label={t("form.slackMappingClear", "Clear mapping")}
                    data-testid={`slack-mapping-clear-${identity.email}`}
                  >
                    <Trash2 className="h-4 w-4 text-destructive" />
                  </Button>
                </div>
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}

function IdentityStatusBadge({
  identity,
}: {
  identity: IntegrationIdentity;
}) {
  const { t } = useTranslation("integrations");

  if (identity.status === "matched") {
    return (
      <Badge variant="secondary" data-testid="slack-mapping-status-matched">
        {identity.source === "manual"
          ? t("form.slackMappingManual", "Manual")
          : t("form.slackMappingMatched", "Matched")}
      </Badge>
    );
  }

  if (identity.status === "ambiguous") {
    return (
      <Badge variant="destructive" data-testid="slack-mapping-status-ambiguous">
        {t("form.slackMappingAmbiguous", "Ambiguous")}
      </Badge>
    );
  }

  return (
    <Badge variant="outline" data-testid="slack-mapping-status-notfound">
      {t("form.slackMappingNotFound", "Not found")}
    </Badge>
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

/** E.164 phone-number check (leading +, non-zero first digit, 7–15 digits). */
function isE164(value: string): boolean {
  return /^\+[1-9]\d{6,14}$/.test(value.trim());
}

function TwilioPanel({
  settings,
  update,
}: {
  settings: Record<string, unknown>;
  update: (key: string, value: unknown) => void;
}) {
  const { t } = useTranslation("integrations");
  const toNumbers = (settings.to_numbers as string[] | undefined) ?? [];

  return (
    <div className="space-y-3" data-testid="twilio-panel">
      <div className="space-y-2">
        <Label htmlFor="ch-twilio-sid">
          {t("form.twilioAccountSid", "Account SID")}
        </Label>
        <Input
          id="ch-twilio-sid"
          data-testid="twilio-account-sid"
          placeholder="AC…"
          value={(settings.account_sid as string) || ""}
          onChange={(e) => update("account_sid", e.target.value)}
        />
      </div>

      <SecretPanel
        id="ch-twilio-token"
        label={t("form.twilioAuthToken", "Auth token")}
        value={(settings.auth_token as string) || ""}
        onChange={(v) => update("auth_token", v)}
      />

      <div className="space-y-2">
        <Label htmlFor="ch-twilio-region">
          {t("form.twilioRegion", "Region (optional)")}
        </Label>
        <Input
          id="ch-twilio-region"
          data-testid="twilio-region"
          list="ch-twilio-region-options"
          placeholder="us1"
          value={(settings.region as string) || ""}
          onChange={(e) => update("region", e.target.value.trim().toLowerCase())}
        />
        {/* Suggestions only — any well-formed region token (e.g. "br2") is
            accepted, not just these three. The backend validates by format,
            not an allowlist. */}
        <datalist id="ch-twilio-region-options">
          <option value="us1">US1 (default)</option>
          <option value="ie1">Ireland (ie1)</option>
          <option value="au1">Australia (au1)</option>
        </datalist>
        <p className="text-xs text-muted-foreground">
          {t(
            "form.twilioRegionHint",
            "Leave empty for US1 (default). Credentials are region-scoped — an ie1 account's SID and auth token only work against ie1, and won't verify here if pasted from a different region's account.",
          )}
        </p>
      </div>

      <p className="text-xs text-muted-foreground">
        {t(
          "form.twilioSenderHint",
          "Provide exactly one sender: a from-number or a Messaging Service SID.",
        )}
      </p>

      <div className="space-y-2">
        <Label htmlFor="ch-twilio-from">
          {t("form.twilioFromNumber", "From number (E.164)")}
        </Label>
        <Input
          id="ch-twilio-from"
          data-testid="twilio-from-number"
          placeholder="+15551234567"
          value={(settings.from_number as string) || ""}
          onChange={(e) => update("from_number", e.target.value)}
        />
      </div>

      <div className="space-y-2">
        <Label htmlFor="ch-twilio-mss">
          {t("form.twilioMessagingServiceSid", "Messaging Service SID")}
        </Label>
        <Input
          id="ch-twilio-mss"
          placeholder="MG…"
          value={(settings.messaging_service_sid as string) || ""}
          onChange={(e) => update("messaging_service_sid", e.target.value)}
        />
      </div>

      <div className="space-y-2">
        <Label htmlFor="ch-twilio-voice-from">
          {t("form.twilioVoiceFrom", "Voice from-number (optional)")}
        </Label>
        <Input
          id="ch-twilio-voice-from"
          placeholder="+15551234567"
          value={(settings.voice_from_number as string) || ""}
          onChange={(e) => update("voice_from_number", e.target.value)}
        />
        <p className="text-xs text-muted-foreground">
          {t("form.twilioVoiceHint", "Leave empty to disable voice calls.")}
        </p>
      </div>

      <div className="space-y-2">
        <Label htmlFor="ch-twilio-to">
          {t("form.twilioToNumbers", "Shared recipient numbers (optional)")}
        </Label>
        <TokenChipsInput
          id="ch-twilio-to"
          data-testid="twilio-to-numbers"
          value={toNumbers}
          onChange={(next) => update("to_numbers", next)}
          validate={isE164}
          normalize={(v) => v.trim()}
          placeholder="+15551234567"
          invalidTitle={t("form.twilioInvalidNumber", "Not a valid E.164 number")}
          getRemoveLabel={(n) => t("form.twilioRemoveNumber", "Remove {{n}}", { n })}
        />
        <p className="text-xs text-muted-foreground">
          {t(
            "form.twilioToHint",
            "Direct-channel sends (per-check broadcast, test) text these numbers.",
          )}
        </p>
      </div>
    </div>
  );
}

// ---- Microsoft Teams bot setup panel ----

interface MSTeamsBotPanelProps {
  settings: Record<string, unknown>;
  onChange: (next: Record<string, unknown>) => void;
  org?: string;
  channelUid?: string;
}

/**
 * Setup surface for the two-way Microsoft Teams bot (`msteams-bot`).
 *
 * Three things the Slack panel does not have to do, because Bot Framework has
 * no OAuth install and no Socket Mode:
 *  1. it states the public-HTTPS-endpoint requirement up front (a firewalled
 *     self-hosted instance simply cannot use the bot);
 *  2. it offers the generated app package instead of an "Install" redirect;
 *  3. it issues a one-time LINK CODE rather than asking for a tenant id.
 *
 * Point 3 is a security property, not a convenience: a tenant id is a
 * semi-public identifier, so a free-text field would let any org assert
 * ownership of any Microsoft 365 tenant. The tenant is written server-side
 * only, from a signature-verified Bot Framework activity that quotes this
 * code back.
 */
function MSTeamsBotPanel({ settings, onChange, org, channelUid }: MSTeamsBotPanelProps) {
  const { t } = useTranslation("integrations");

  const isEditMode = Boolean(org && channelUid);
  const tenantId = (settings.tenant_id as string) || "";
  const uninstalledAt = (settings.uninstalled_at as string) || "";
  const isLinked = tenantId.length > 0;

  const { data: status } = useMSTeamsBotStatus(org ?? "", isEditMode);
  const { data, isLoading, isError } = useMSTeamsBotDestinations(
    org ?? "",
    channelUid ?? "",
    isEditMode && isLinked,
  );

  const startLink = useStartMSTeamsLink(org ?? "");
  const [linkCode, setLinkCode] = useState<string | null>(null);

  const destinations = data?.destinations ?? [];
  const currentId = (settings.channel_id as string) || "";

  function selectDestination(dest: MSTeamsDestination) {
    onChange({
      ...settings,
      channel_id: dest.id,
      channel_name: dest.name,
      // Keep team_id in step with the selected channel — Teams channels are
      // per-team, so a stale team_id would misattribute the destination.
      team_id: dest.team_id ?? "",
      display_name: dest.name,
    });
  }

  async function handleConnect() {
    if (!org) return;
    try {
      const pending = await startLink.mutateAsync(channelUid);
      setLinkCode(pending.code);
    } catch {
      toast.error(
        t("form.msteamsBotLinkFailed", "Could not create a link code"),
      );
    }
  }

  async function handleDownload() {
    if (!org) return;
    try {
      await downloadMSTeamsManifest(org);
    } catch {
      toast.error(
        t("form.msteamsBotDownloadFailed", "Could not download the Teams app package"),
      );
    }
  }

  if (!isEditMode) {
    return (
      <div className="rounded border bg-muted/30 p-3 text-sm">
        <p>
          {t(
            "form.msteamsBotCreateHint",
            "Create the integration first, then install the Teams app package and run the link command shown here to connect your Microsoft 365 tenant.",
          )}
        </p>
      </div>
    );
  }

  return (
    <div className="space-y-4" data-testid="msteams-bot-panel">
      {status && !status.enabled && (
        <Alert variant="destructive" data-testid="msteams-bot-disabled">
          <AlertTriangle />
          <AlertTitle>
            {t("form.msteamsBotDisabledTitle", "The Teams bot is disabled on this server")}
          </AlertTitle>
          <AlertDescription>
            {t(
              "form.msteamsBotDisabledBody",
              "Enable it in the server settings. Microsoft must be able to reach this instance over public HTTPS — a firewalled deployment cannot use the Teams bot.",
            )}
          </AlertDescription>
        </Alert>
      )}

      {/* Connection status — which tenant this integration is bound to. */}
      {uninstalledAt || data?.uninstalled ? (
        <Alert variant="warning" data-testid="msteams-bot-uninstalled">
          <AlertTriangle />
          <AlertTitle>
            {t("form.msteamsBotUninstalledTitle", "The Teams app was removed")}
          </AlertTitle>
          <AlertDescription>
            {t(
              "form.msteamsBotUninstalled",
              "The Teams app was removed from this tenant. Reinstall it to resume notifications.",
            )}
          </AlertDescription>
        </Alert>
      ) : isLinked ? (
        <Alert variant="success" data-testid="msteams-bot-connected">
          <CheckCircle2 />
          <AlertTitle>{t("form.msteamsBotConnected", "Connected")}</AlertTitle>
          <AlertDescription>
            <span className="block">
              {t("form.msteamsBotTenantLabel", "Microsoft 365 tenant")}:{" "}
              <code className="break-all">{tenantId}</code>
            </span>
          </AlertDescription>
        </Alert>
      ) : (
        <Alert data-testid="msteams-bot-not-connected">
          <Info />
          <AlertTitle>
            {t("form.msteamsBotNotConnectedTitle", "Not connected to Microsoft Teams")}
          </AlertTitle>
          <AlertDescription>
            {t(
              "form.msteamsBotNotConnectedBody",
              "Install the Teams app package, then run the link command below in any channel the bot was added to.",
            )}
          </AlertDescription>
        </Alert>
      )}

      {status?.messagingEndpoint && (
        <div className="space-y-1 text-xs text-muted-foreground">
          <p>
            {t(
              "form.msteamsBotEndpointHint",
              "Microsoft must be able to reach this messaging endpoint over public HTTPS:",
            )}
          </p>
          <code
            className="block break-all rounded bg-muted px-2 py-1"
            data-testid="msteams-bot-endpoint"
          >
            {status.messagingEndpoint}
          </code>
        </div>
      )}

      {/* Step 1 — app package */}
      <div className="space-y-2">
        <Button
          type="button"
          variant="outline"
          onClick={() => void handleDownload()}
          data-testid="msteams-bot-manifest"
        >
          <Download className="mr-2 h-4 w-4" aria-hidden="true" />
          {t("form.msteamsBotDownloadApp", "Download Teams app package")}
        </Button>
        <p className="text-xs text-muted-foreground">
          {t(
            "form.msteamsBotSideloadHint",
            "In Teams, open Apps → Manage your apps → Upload a custom app, pick this zip, then add SolidPing to a team. The channel you add it to becomes a notification destination.",
          )}
        </p>
      </div>

      {/* Step 2 — link code */}
      {!isLinked && (
        <div className="space-y-2">
          <Button
            type="button"
            onClick={() => void handleConnect()}
            disabled={startLink.isPending}
            data-testid="msteams-bot-connect"
          >
            {startLink.isPending && (
              <Loader2 className="mr-2 h-4 w-4 animate-spin" aria-hidden="true" />
            )}
            {t("form.msteamsBotConnectButton", "Connect Microsoft Teams")}
          </Button>

          {linkCode && (
            <div className="space-y-2 rounded border bg-muted/30 p-3">
              <p className="text-xs text-muted-foreground">
                {t(
                  "form.msteamsBotLinkCodeHint",
                  "In a Teams channel where SolidPing was added, send this message. The code can be used once and expires in 30 minutes.",
                )}
              </p>
              <code
                className="block break-all rounded bg-background px-2 py-1 text-sm"
                data-testid="msteams-bot-link-code"
              >
                @SolidPing link {linkCode}
              </code>
            </div>
          )}
        </div>
      )}

      {/* Step 3 — destination picker */}
      <div className="space-y-2">
        <Label>{t("form.msteamsBotDestination", "Notification channel")}</Label>

        {!isLinked ? (
          <p className="text-xs text-muted-foreground">
            {t(
              "form.msteamsBotLinkFirst",
              "Connect a Microsoft 365 tenant first — channels appear here once the bot is added to them.",
            )}
          </p>
        ) : isLoading ? (
          <div className="flex items-center gap-2 text-muted-foreground">
            <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
            <span>{t("form.msteamsBotLoading", "Loading…")}</span>
          </div>
        ) : isError ? (
          <p className="text-xs text-destructive">
            {t("form.msteamsBotError", "Could not load Teams channels.")}
          </p>
        ) : destinations.length === 0 ? (
          <p className="text-xs text-muted-foreground" data-testid="msteams-bot-empty">
            {t(
              "form.msteamsBotNoDestinations",
              "No Teams channels yet. Add SolidPing to a channel in Teams and it will appear here.",
            )}
          </p>
        ) : (
          <ul className="space-y-1" data-testid="msteams-bot-destinations">
            {destinations.map((dest) => (
              <li key={dest.id}>
                <button
                  type="button"
                  onClick={() => selectDestination(dest)}
                  className={cn(
                    "flex w-full items-center justify-between gap-2 rounded border p-2 text-left text-sm hover:bg-accent",
                    dest.id === currentId && "border-primary bg-accent",
                  )}
                  data-testid={`msteams-bot-destination-${dest.id}`}
                >
                  <span className="min-w-0">
                    <span className="block truncate font-medium">{dest.name || dest.id}</span>
                    {dest.team_name && (
                      <span className="block truncate text-xs text-muted-foreground">
                        {dest.team_name}
                      </span>
                    )}
                  </span>
                  {dest.id === currentId && (
                    <Check className="h-4 w-4 shrink-0" aria-hidden="true" />
                  )}
                </button>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}

/**
 * "Direct messages need a reinstall" (spec 2026-08-22-02).
 *
 * SolidPing now asks for the `im:history` scope so a DM to the bot lands in the
 * support inbox. Slack DOES NOT GRANT NEW SCOPES TO EXISTING INSTALLS: a
 * workspace connected before that scope was requested keeps its old grant, and
 * Slack simply never delivers `message.im` to us. From the inbox that is
 * indistinguishable from nobody writing in — which is why the state is surfaced
 * here, in the product, instead of leaving an operator to discover it from a
 * silence.
 *
 * Reinstalling is the whole fix, and it reuses the existing install flow with
 * this channel's uid so the callback updates the row rather than creating a
 * second one.
 */
function SlackDmCaptureNotice({
  settings,
  org,
  channelUid,
}: {
  settings: Record<string, unknown>;
  org?: string;
  channelUid?: string;
}) {
  const { t } = useTranslation("integrations");

  const scopes = Array.isArray(settings.scopes) ? (settings.scopes as string[]) : [];
  if (scopes.includes("im:history")) {
    return null;
  }

  return (
    <Alert data-testid="slack-dm-reinstall">
      <AlertTriangle className="h-4 w-4" />
      <AlertTitle>
        {t("form.slackDmUnavailableTitle", "Direct messages are not being captured")}
      </AlertTitle>
      <AlertDescription className="space-y-2">
        <p>
          {t(
            "form.slackDmUnavailableBody",
            "This workspace was connected before SolidPing asked for the im:history scope. " +
              "Slack does not grant new scopes to an existing install, so direct messages to the " +
              "bot never reach the support inbox. Reinstall the app to enable DM capture — " +
              "nothing else about this integration changes.",
          )}
        </p>
        {org ? (
          <Button
            type="button"
            size="sm"
            variant="outline"
            onClick={() => {
              void startSlackInstall(org, channelUid).catch(() =>
                toast.error(
                  t("form.slackInstallFailed", "Failed to start Slack install"),
                ),
              );
            }}
            data-testid="slack-dm-reinstall-button"
          >
            {t("form.slackReinstallButton", "Reinstall Slack app")}
          </Button>
        ) : null}
      </AlertDescription>
    </Alert>
  );
}
