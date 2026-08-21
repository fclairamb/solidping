import { useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { AlertCircle, Check, Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { ApiError } from "@/api/client";
import {
  useDiscordGatewayStatus,
  useSetSystemParameter,
  useSystemParameters,
  type SystemParameter,
} from "@/api/hooks";

export const Route = createFileRoute("/orgs/$org/server/discord")({
  component: DiscordSettingsPage,
});

// These MUST match the keys the backend reads in
// server/internal/systemconfig/systemconfig.go (KeyDiscordEnabled,
// KeyDiscordPublicKey, KeyDiscordGatewayEnabled). Writing a `discord.*`
// namespace the backend never loads persists rows that silently do nothing.
const KEY_ENABLED = "auth.discord.enabled";
const KEY_PUBLIC_KEY = "auth.discord.public_key";
const KEY_GATEWAY_ENABLED = "auth.discord.gateway_enabled";

function DiscordSettingsPage() {
  const { t, i18n } = useTranslation(["server", "common"]);
  const { data: params, isLoading } = useSystemParameters();
  const setParam = useSetSystemParameter();
  const { data: status } = useDiscordGatewayStatus();

  const [gatewayEnabled, setGatewayEnabled] = useState(false);
  const [publicKey, setPublicKey] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  const getParam = (key: string): SystemParameter | undefined =>
    params?.find((p: SystemParameter) => p.key === key);

  const discordEnabled = Boolean(getParam(KEY_ENABLED)?.value);
  const persistedGatewayEnabled = Boolean(getParam(KEY_GATEWAY_ENABLED)?.value);
  const persistedPublicKey = String(getParam(KEY_PUBLIC_KEY)?.value ?? "");

  // Seed the form from the persisted parameters the moment they arrive, and
  // re-seed whenever they change underneath us. Done during render (React's
  // documented "adjusting state when props change" pattern) rather than in an
  // effect: an effect would render once with stale values and then again with
  // the real ones, which is both a wasted render and a visible flicker on the
  // switch.
  const [seededFrom, setSeededFrom] = useState(params);

  if (params !== seededFrom) {
    setSeededFrom(params);
    setGatewayEnabled(persistedGatewayEnabled);
    setPublicKey(persistedPublicKey);
  }

  const isDirty =
    gatewayEnabled !== persistedGatewayEnabled ||
    publicKey !== persistedPublicKey;

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setSaved(false);

    try {
      const writes: Promise<unknown>[] = [];
      if (gatewayEnabled !== persistedGatewayEnabled) {
        writes.push(
          setParam.mutateAsync({
            key: KEY_GATEWAY_ENABLED,
            value: gatewayEnabled,
          }),
        );
      }
      if (publicKey !== persistedPublicKey) {
        // Not a secret: it is a PUBLIC verification key Discord shows on the
        // app's own settings page, and an operator needs to see it to confirm
        // it matches.
        writes.push(
          setParam.mutateAsync({ key: KEY_PUBLIC_KEY, value: publicKey }),
        );
      }
      await Promise.all(writes);
      setSaved(true);
      setTimeout(() => setSaved(false), 3000);
    } catch (err) {
      setError(
        err instanceof ApiError ? err.message : t("server:unexpectedError"),
      );
    }
  };

  const formatLastConnected = (iso?: string): string => {
    if (!iso) return t("server:discord.status.never", "Never");
    const ts = Date.parse(iso);
    if (Number.isNaN(ts)) return iso;
    try {
      return new Date(ts).toLocaleString(i18n.language);
    } catch {
      return new Date(ts).toLocaleString();
    }
  };

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-12">
        <Loader2 className="h-8 w-8 animate-spin text-muted-foreground" />
      </div>
    );
  }

  if (!discordEnabled) {
    return (
      <Alert data-testid="discord-not-enabled">
        <AlertCircle className="h-4 w-4" />
        <AlertDescription>
          {t(
            "server:discord.notEnabledHint",
            "Discord is disabled on this instance. Enable it under Authentication first.",
          )}
        </AlertDescription>
      </Alert>
    );
  }

  const connected = Boolean(status?.connected);
  const guildCount = status?.guildCount ?? 0;

  return (
    <div className="space-y-4">
      <Card>
        <CardHeader>
          <CardTitle>{t("server:discord.bot.title", "Discord bot")}</CardTitle>
          <CardDescription>
            {t(
              "server:discord.bot.description",
              "The public key verifies Discord's signed interaction requests. The Gateway is a separate outgoing WebSocket that carries everything a human types at the bot.",
            )}
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={handleSave} className="space-y-4">
            {error && (
              <Alert variant="destructive">
                <AlertCircle className="h-4 w-4" />
                <AlertDescription>{error}</AlertDescription>
              </Alert>
            )}
            {saved && (
              <Alert>
                <Check className="h-4 w-4" />
                <AlertDescription>{t("server:saved")}</AlertDescription>
              </Alert>
            )}

            <div className="space-y-2">
              <Label htmlFor="discordPublicKey">
                {t("server:discord.bot.publicKeyLabel", "Application public key")}
              </Label>
              <Input
                id="discordPublicKey"
                value={publicKey}
                onChange={(e) => setPublicKey(e.target.value.trim())}
                placeholder="0123456789abcdef…"
                autoComplete="off"
                spellCheck={false}
                disabled={setParam.isPending}
                data-testid="discord-public-key-input"
              />
              <p className="text-xs text-muted-foreground">
                {t(
                  "server:discord.bot.publicKeyHelp",
                  "Copy it from the Discord Developer Portal → your application → General Information. Without it, every interaction is rejected and buttons and slash commands do nothing.",
                )}
              </p>
            </div>

            <div className="flex items-center gap-3">
              <Switch
                id="discordGatewayEnabled"
                data-testid="discord-gateway-enabled"
                checked={gatewayEnabled}
                onCheckedChange={setGatewayEnabled}
                disabled={setParam.isPending}
              />
              <Label htmlFor="discordGatewayEnabled">
                {t(
                  "server:discord.bot.gatewayEnableLabel",
                  "Enable the Discord Gateway (inbound messages)",
                )}
              </Label>
            </div>

            <Alert data-testid="discord-intent-hint">
              <AlertCircle className="h-4 w-4" />
              <AlertDescription>
                {t(
                  "server:discord.bot.intentHint",
                  "The Gateway needs the privileged MESSAGE_CONTENT intent, enabled under Bot → Privileged Gateway Intents. Discord grants it freely below 100 servers and requires a review above that. Without it the bot connects but every message arrives empty, so thread comments and mention commands silently do nothing.",
                )}
              </AlertDescription>
            </Alert>

            <Alert data-testid="discord-restart-hint">
              <AlertCircle className="h-4 w-4" />
              <AlertDescription>
                {t(
                  "server:discord.bot.restartHint",
                  "Changing these takes effect on the next server restart.",
                )}
              </AlertDescription>
            </Alert>

            <Button
              type="submit"
              data-testid="discord-save"
              disabled={setParam.isPending || !isDirty}
              variant={isDirty ? "default" : "secondary"}
            >
              {setParam.isPending ? (
                <>
                  <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                  {t("common:saving")}
                </>
              ) : (
                t("common:save")
              )}
            </Button>
          </form>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>
            {t("server:discord.status.title", "Gateway status")}
          </CardTitle>
          <CardDescription>
            {t(
              "server:discord.status.description",
              "Live state of the outgoing Discord Gateway connection.",
            )}
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          <div className="flex flex-wrap items-center gap-2 text-sm">
            <span className="text-muted-foreground">
              {t("server:discord.status.connection", "Connection")}
            </span>
            {connected ? (
              <Badge variant="success" data-testid="discord-status-badge">
                {t("server:discord.status.connected", "Connected")}
              </Badge>
            ) : (
              <Badge variant="secondary" data-testid="discord-status-badge">
                {t("server:discord.status.disconnected", "Disconnected")}
              </Badge>
            )}
          </div>
          <div
            className="flex flex-wrap items-center gap-2 text-sm"
            data-testid="discord-status-guilds"
          >
            <span className="text-muted-foreground">
              {t("server:discord.status.guilds", {
                defaultValue: "{{count}} servers",
                count: guildCount,
              })}
            </span>
          </div>
          <div className="flex flex-wrap items-center gap-2 text-sm">
            <span className="text-muted-foreground">
              {t("server:discord.status.lastConnected", "Last connected")}
            </span>
            <span data-testid="discord-status-last-connected">
              {formatLastConnected(status?.lastConnectedAt)}
            </span>
          </div>
          <div className="flex flex-wrap items-center gap-2 text-sm">
            <span className="text-muted-foreground">
              {t("server:discord.status.lastError", "Last error")}
            </span>
            <span data-testid="discord-status-last-error">
              {status?.lastError && status.lastError.length > 0
                ? status.lastError
                : t("server:discord.status.noError", "None")}
            </span>
          </div>
        </CardContent>
      </Card>
    </div>
  );
}
