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
  useSystemParameterEnvOverrides,
  useSystemParameters,
  type SystemParameter,
} from "@/api/hooks";

export const Route = createFileRoute("/orgs/$org/server/analytics")({
  component: AnalyticsSettingsPage,
});

// These MUST match the keys the backend reads in
// server/internal/systemconfig/systemconfig.go (KeyPostHog*). Writing any other
// namespace persists rows the backend never loads, so the settings would
// silently do nothing. A backend test guards the contract.
const KEY_ENABLED = "posthog.enabled";
const KEY_PROJECT_API_KEY = "posthog.project_api_key";
const KEY_HOST = "posthog.host";
const KEY_PERSONAL_API_KEY = "posthog.personal_api_key";

const DEFAULT_HOST = "https://eu.i.posthog.com";

function AnalyticsSettingsPage() {
  const { t } = useTranslation(["server", "common"]);
  const { data: params, isLoading } = useSystemParameters();
  const { data: envOverrides } = useSystemParameterEnvOverrides();
  const setParam = useSetSystemParameter();

  const [enabled, setEnabled] = useState(true);
  const [projectApiKey, setProjectApiKey] = useState("");
  const [host, setHost] = useState("");
  const [personalApiKey, setPersonalApiKey] = useState("");
  const [editingPersonal, setEditingPersonal] = useState(false);
  const [showPersonal, setShowPersonal] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  const getParam = (key: string): SystemParameter | undefined =>
    params?.find((p: SystemParameter) => p.key === key);

  const isEnvOverridden = (key: string) => (envOverrides ?? []).includes(key);

  // posthog.enabled defaults to true server-side, so an absent row means "on".
  const persistedEnabled = getParam(KEY_ENABLED)
    ? Boolean(getParam(KEY_ENABLED)?.value)
    : true;
  const persistedProjectApiKey = (getParam(KEY_PROJECT_API_KEY)?.value as string) ?? "";
  const persistedHost = (getParam(KEY_HOST)?.value as string) ?? "";
  const personalStored = getParam(KEY_PERSONAL_API_KEY)?.secret ?? false;
  const personalInputVisible = editingPersonal || !personalStored;

  useEffect(() => {
    if (!params) return;
    setEnabled(persistedEnabled);
    setProjectApiKey(persistedProjectApiKey);
    setHost(persistedHost);
    setPersonalApiKey("");
    setEditingPersonal(false);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [params]);

  // THE enablement rule, identical to the backend's
  // config.PostHogConfig.Active(): the toggle alone never turns anything on.
  const effectivelyActive = enabled && projectApiKey.trim() !== "";

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setSaved(false);

    try {
      const writes: Promise<unknown>[] = [];
      if (enabled !== persistedEnabled) {
        writes.push(setParam.mutateAsync({ key: KEY_ENABLED, value: enabled }));
      }
      if (projectApiKey !== persistedProjectApiKey) {
        writes.push(
          setParam.mutateAsync({ key: KEY_PROJECT_API_KEY, value: projectApiKey.trim() }),
        );
      }
      if (host !== persistedHost) {
        writes.push(setParam.mutateAsync({ key: KEY_HOST, value: host.trim() }));
      }
      if (personalInputVisible && personalApiKey.length > 0) {
        writes.push(
          setParam.mutateAsync({
            key: KEY_PERSONAL_API_KEY,
            value: personalApiKey,
            secret: true,
          }),
        );
      }
      await Promise.all(writes);
      setEditingPersonal(false);
      setPersonalApiKey("");
      setSaved(true);
      setTimeout(() => setSaved(false), 3000);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : t("server:unexpectedError"));
    }
  };

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-12">
        <Loader2 className="h-8 w-8 animate-spin text-muted-foreground" />
      </div>
    );
  }

  const envBadge = (key: string) =>
    isEnvOverridden(key) ? (
      <Badge variant="outline" className="ml-2 align-middle">
        {t("server:analytics.envOverride")}
      </Badge>
    ) : null;

  const envHint = (key: string) =>
    isEnvOverridden(key) ? (
      <p className="text-xs text-amber-600 dark:text-amber-500">
        {t("server:analytics.envOverrideHelp")}
      </p>
    ) : null;

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("server:analytics.title")}</CardTitle>
        <CardDescription>{t("server:analytics.description")}</CardDescription>
      </CardHeader>
      <CardContent>
        <form onSubmit={handleSave} className="space-y-6">
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

          <Alert>
            <AlertCircle className="h-4 w-4" />
            <AlertDescription>
              <span className="font-medium">
                {effectivelyActive
                  ? t("server:analytics.statusOn")
                  : t("server:analytics.statusOff")}
              </span>{" "}
              {!effectivelyActive && t("server:analytics.statusOffHelp")}
            </AlertDescription>
          </Alert>

          <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
            <div className="space-y-1">
              <Label htmlFor="posthogEnabled">
                {t("server:analytics.enabled")}
                {envBadge(KEY_ENABLED)}
              </Label>
              <p className="text-xs text-muted-foreground">
                {t("server:analytics.enabledHelp")}
              </p>
              {envHint(KEY_ENABLED)}
            </div>
            <Switch
              id="posthogEnabled"
              checked={enabled}
              onCheckedChange={setEnabled}
              disabled={setParam.isPending}
            />
          </div>

          <div className="space-y-2">
            <Label htmlFor="posthogProjectApiKey">
              {t("server:analytics.projectApiKey")}
              {envBadge(KEY_PROJECT_API_KEY)}
            </Label>
            <Input
              id="posthogProjectApiKey"
              type="text"
              autoComplete="off"
              placeholder={t("server:analytics.projectApiKeyPlaceholder")}
              value={projectApiKey}
              onChange={(e) => setProjectApiKey(e.target.value)}
              disabled={setParam.isPending}
            />
            <p className="text-xs text-muted-foreground">
              {t("server:analytics.projectApiKeyHelp")}
            </p>
            {envHint(KEY_PROJECT_API_KEY)}
          </div>

          <div className="space-y-2">
            <Label htmlFor="posthogHost">
              {t("server:analytics.host")}
              {envBadge(KEY_HOST)}
            </Label>
            <Input
              id="posthogHost"
              type="url"
              placeholder={DEFAULT_HOST}
              value={host}
              onChange={(e) => setHost(e.target.value)}
              disabled={setParam.isPending}
            />
            <p className="text-xs text-muted-foreground">
              {t("server:analytics.hostHelp")}
            </p>
            {envHint(KEY_HOST)}
          </div>

          <div className="space-y-2">
            <Label htmlFor="posthogPersonalApiKey">
              {t("server:analytics.personalApiKey")}
              {envBadge(KEY_PERSONAL_API_KEY)}
            </Label>
            {!personalInputVisible ? (
              <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
                <Input id="posthogPersonalApiKey" type="password" value="******" disabled />
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => {
                    setEditingPersonal(true);
                    setPersonalApiKey("");
                  }}
                >
                  {t("common:edit")}
                </Button>
              </div>
            ) : (
              <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
                <div className="relative flex-1">
                  <Input
                    id="posthogPersonalApiKey"
                    type={showPersonal ? "text" : "password"}
                    autoComplete="off"
                    placeholder={t("server:analytics.personalApiKeyPlaceholder")}
                    value={personalApiKey}
                    onChange={(e) => setPersonalApiKey(e.target.value)}
                    disabled={setParam.isPending}
                  />
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    className="absolute right-1 top-1/2 h-7 w-7 -translate-y-1/2 p-0"
                    onClick={() => setShowPersonal(!showPersonal)}
                  >
                    {showPersonal ? (
                      <EyeOff className="h-4 w-4" />
                    ) : (
                      <Eye className="h-4 w-4" />
                    )}
                  </Button>
                </div>
                {editingPersonal && (
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    onClick={() => {
                      setEditingPersonal(false);
                      setPersonalApiKey("");
                    }}
                  >
                    {t("common:cancel")}
                  </Button>
                )}
              </div>
            )}
            <p className="text-xs text-muted-foreground">
              {t("server:analytics.personalApiKeyHelp")}
            </p>
            {envHint(KEY_PERSONAL_API_KEY)}
          </div>

          <p className="text-xs text-muted-foreground">
            {t("server:analytics.whatIsSent")}
          </p>

          <Button type="submit" disabled={setParam.isPending}>
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
  );
}
