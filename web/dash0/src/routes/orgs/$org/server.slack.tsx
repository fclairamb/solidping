import { useEffect, useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { AlertCircle, Check, Eye, EyeOff, Loader2 } from "lucide-react";
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
  useSetSystemParameter,
  useSlackSocketStatus,
  useSystemParameters,
  type SystemParameter,
} from "@/api/hooks";

export const Route = createFileRoute("/orgs/$org/server/slack")({
  component: SlackSettingsPage,
});

const KEY_ENABLED = "slack.enabled";
const KEY_SOCKET_ENABLED = "slack.socket_mode_enabled";
const KEY_APP_TOKEN = "slack.app_token";

function SlackSettingsPage() {
  const { t, i18n } = useTranslation(["server", "common"]);
  const { data: params, isLoading } = useSystemParameters();
  const setParam = useSetSystemParameter();
  const { data: status } = useSlackSocketStatus();

  const [socketEnabled, setSocketEnabled] = useState(false);
  const [appToken, setAppToken] = useState("");
  const [editingToken, setEditingToken] = useState(false);
  const [showToken, setShowToken] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  const getParam = (key: string): SystemParameter | undefined =>
    params?.find((p: SystemParameter) => p.key === key);

  const slackEnabled = Boolean(getParam(KEY_ENABLED)?.value);
  const persistedSocketEnabled = Boolean(getParam(KEY_SOCKET_ENABLED)?.value);
  const tokenStored = getParam(KEY_APP_TOKEN)?.secret ?? false;

  useEffect(() => {
    if (!params) return;
    setSocketEnabled(persistedSocketEnabled);
    // Reset edit state when the underlying params change
    setAppToken("");
    setEditingToken(false);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [params]);

  const isDirty =
    socketEnabled !== persistedSocketEnabled ||
    (editingToken && appToken.length > 0);

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setSaved(false);

    try {
      const writes: Promise<unknown>[] = [];
      if (socketEnabled !== persistedSocketEnabled) {
        writes.push(
          setParam.mutateAsync({
            key: KEY_SOCKET_ENABLED,
            value: socketEnabled,
          }),
        );
      }
      if (editingToken && appToken.length > 0) {
        writes.push(
          setParam.mutateAsync({
            key: KEY_APP_TOKEN,
            value: appToken,
            secret: true,
          }),
        );
      }
      await Promise.all(writes);
      setEditingToken(false);
      setAppToken("");
      setSaved(true);
      setTimeout(() => setSaved(false), 3000);
    } catch (err) {
      setError(
        err instanceof ApiError ? err.message : t("server:unexpectedError"),
      );
    }
  };

  const startEditingToken = () => {
    setEditingToken(true);
    setAppToken("");
  };

  const cancelEditingToken = () => {
    setEditingToken(false);
    setAppToken("");
  };

  const formatLastConnected = (iso?: string): string => {
    if (!iso) return t("server:slack.status.never");
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

  if (!slackEnabled) {
    return (
      <Alert data-testid="slack-not-enabled">
        <AlertCircle className="h-4 w-4" />
        <AlertDescription>
          {t("server:slack.socketMode.notEnabledHint")}
        </AlertDescription>
      </Alert>
    );
  }

  const connected = Boolean(status?.connected);
  const teamCount = status?.teamCount ?? 0;

  return (
    <div className="space-y-4">
      <Card>
        <CardHeader>
          <CardTitle>{t("server:slack.socketMode.title")}</CardTitle>
          <CardDescription>
            {t("server:slack.socketMode.description")}
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

            <div className="flex items-center gap-3">
              <Switch
                id="slackSocketModeEnabled"
                data-testid="slack-socket-mode-enabled"
                checked={socketEnabled}
                onCheckedChange={setSocketEnabled}
                disabled={setParam.isPending}
              />
              <Label htmlFor="slackSocketModeEnabled">
                {t("server:slack.socketMode.enableLabel")}
              </Label>
            </div>

            <div className="space-y-2">
              <Label htmlFor="slackAppToken">
                {t("server:slack.socketMode.appTokenLabel")}
              </Label>
              {!editingToken && tokenStored ? (
                <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
                  <Input
                    id="slackAppToken"
                    type="password"
                    value="******"
                    disabled
                    data-testid="slack-app-token-masked"
                  />
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    onClick={startEditingToken}
                    data-testid="slack-app-token-edit"
                  >
                    {t("common:edit")}
                  </Button>
                </div>
              ) : (
                <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
                  <div className="relative flex-1">
                    <Input
                      id="slackAppToken"
                      type={showToken ? "text" : "password"}
                      placeholder={t(
                        "server:slack.socketMode.appTokenPlaceholder",
                      )}
                      value={appToken}
                      onChange={(e) => setAppToken(e.target.value)}
                      disabled={setParam.isPending}
                      autoComplete="new-password"
                      data-testid="slack-app-token-input"
                    />
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      className="absolute right-1 top-1/2 -translate-y-1/2 h-7 w-7 p-0"
                      onClick={() => setShowToken((v) => !v)}
                      aria-label={t("server:slack.socketMode.appTokenLabel")}
                    >
                      {showToken ? (
                        <EyeOff className="h-4 w-4" />
                      ) : (
                        <Eye className="h-4 w-4" />
                      )}
                    </Button>
                  </div>
                  {editingToken && tokenStored && (
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      onClick={cancelEditingToken}
                    >
                      {t("common:cancel")}
                    </Button>
                  )}
                </div>
              )}
            </div>

            <Button
              type="submit"
              data-testid="slack-socket-save"
              disabled={setParam.isPending || !isDirty}
              variant={isDirty ? "default" : "secondary"}
            >
              {setParam.isPending ? (
                <>
                  <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                  {t("common:saving")}
                </>
              ) : (
                t("server:slack.socketMode.saveButton")
              )}
            </Button>
          </form>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{t("server:slack.status.title")}</CardTitle>
          <CardDescription>
            {t("server:slack.status.description")}
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          <div className="flex flex-wrap items-center gap-2 text-sm">
            <span className="text-muted-foreground">
              {t("server:slack.status.connection")}
            </span>
            {connected ? (
              <Badge variant="success" data-testid="slack-status-badge">
                {t("server:slack.status.connected")}
              </Badge>
            ) : (
              <Badge variant="secondary" data-testid="slack-status-badge">
                {t("server:slack.status.disconnected")}
              </Badge>
            )}
          </div>
          <div
            className="flex flex-wrap items-center gap-2 text-sm"
            data-testid="slack-status-teams"
          >
            <span className="text-muted-foreground">
              {t("server:slack.status.teams", { count: teamCount })}
            </span>
          </div>
          <div className="flex flex-wrap items-center gap-2 text-sm">
            <span className="text-muted-foreground">
              {t("server:slack.status.lastConnected")}
            </span>
            <span data-testid="slack-status-last-connected">
              {formatLastConnected(status?.lastConnectedAt)}
            </span>
          </div>
          <div className="flex flex-wrap items-center gap-2 text-sm">
            <span className="text-muted-foreground">
              {t("server:slack.status.lastError")}
            </span>
            <span data-testid="slack-status-last-error">
              {status?.lastError && status.lastError.length > 0
                ? status.lastError
                : t("server:slack.status.noError")}
            </span>
          </div>
        </CardContent>
      </Card>
    </div>
  );
}
