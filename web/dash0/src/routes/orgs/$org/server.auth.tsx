import { useState, useEffect } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Alert, AlertDescription } from "@/components/ui/alert";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { AlertCircle, Check, Eye, EyeOff, Loader2 } from "lucide-react";
import { ApiError } from "@/api/client";
import {
  useSystemParameters,
  useSetSystemParameter,
  type SystemParameter,
} from "@/api/hooks";

export const Route = createFileRoute("/orgs/$org/server/auth")({
  component: AuthSettingsPage,
});

type FieldKind = "clientId" | "clientSecret" | "appId" | "signingSecret";

interface ProviderConfig {
  name: string;
  fields: {
    key: string;
    labelKey: FieldKind;
    secret: boolean;
  }[];
}

const providers: ProviderConfig[] = [
  {
    name: "Google",
    fields: [
      { key: "auth.google.client_id", labelKey: "clientId", secret: false },
      { key: "auth.google.client_secret", labelKey: "clientSecret", secret: true },
    ],
  },
  {
    name: "GitHub",
    fields: [
      { key: "auth.github.client_id", labelKey: "clientId", secret: false },
      { key: "auth.github.client_secret", labelKey: "clientSecret", secret: true },
    ],
  },
  {
    name: "GitLab",
    fields: [
      { key: "auth.gitlab.client_id", labelKey: "clientId", secret: false },
      { key: "auth.gitlab.client_secret", labelKey: "clientSecret", secret: true },
    ],
  },
  {
    name: "Microsoft",
    fields: [
      { key: "auth.microsoft.client_id", labelKey: "clientId", secret: false },
      { key: "auth.microsoft.client_secret", labelKey: "clientSecret", secret: true },
    ],
  },
  {
    name: "Slack",
    fields: [
      { key: "auth.slack.app_id", labelKey: "appId", secret: false },
      { key: "auth.slack.client_id", labelKey: "clientId", secret: false },
      { key: "auth.slack.client_secret", labelKey: "clientSecret", secret: true },
      { key: "auth.slack.signing_secret", labelKey: "signingSecret", secret: true },
    ],
  },
];

function AuthSettingsPage() {
  const { t } = useTranslation(["server", "common"]);
  const { data: params, isLoading } = useSystemParameters();
  const setParam = useSetSystemParameter();

  const [values, setValues] = useState<Record<string, string>>({});
  const [editingSecrets, setEditingSecrets] = useState<Set<string>>(new Set());
  const [visibleSecrets, setVisibleSecrets] = useState<Set<string>>(new Set());
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  useEffect(() => {
    if (params) {
      const newValues: Record<string, string> = {};
      for (const provider of providers) {
        for (const field of provider.fields) {
          const param = params.find((p: SystemParameter) => p.key === field.key);
          newValues[field.key] = (param?.value as string) ?? "";
        }
      }
      setValues(newValues);
    }
  }, [params]);

  const isSecretStored = (key: string) =>
    params?.find((p: SystemParameter) => p.key === key)?.secret ?? false;

  const handleSave = async (providerName: string) => {
    setError(null);
    setSaved(false);

    const provider = providers.find((p) => p.name === providerName);
    if (!provider) return;

    try {
      await Promise.all(
        provider.fields
          .filter(
            (field) => !field.secret || editingSecrets.has(field.key) || !isSecretStored(field.key)
          )
          .map((field) =>
            setParam.mutateAsync({
              key: field.key,
              value: values[field.key] || "",
              secret: field.secret || undefined,
            })
          )
      );
      setEditingSecrets((prev) => {
        const next = new Set(prev);
        for (const field of provider.fields) {
          next.delete(field.key);
        }
        return next;
      });
      setSaved(true);
      setTimeout(() => setSaved(false), 3000);
    } catch (err) {
      if (err instanceof ApiError) {
        setError(err.message);
      } else {
        setError(t("server:unexpectedError"));
      }
    }
  };

  const toggleSecretVisibility = (key: string) => {
    setVisibleSecrets((prev) => {
      const next = new Set(prev);
      if (next.has(key)) {
        next.delete(key);
      } else {
        next.add(key);
      }
      return next;
    });
  };

  const startEditing = (key: string) => {
    setEditingSecrets((prev) => new Set(prev).add(key));
    setValues((prev) => ({ ...prev, [key]: "" }));
  };

  const cancelEditing = (key: string) => {
    setEditingSecrets((prev) => {
      const next = new Set(prev);
      next.delete(key);
      return next;
    });
    const original = params?.find((p: SystemParameter) => p.key === key)?.value as string ?? "";
    setValues((prev) => ({ ...prev, [key]: original }));
  };

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-12">
        <Loader2 className="h-8 w-8 animate-spin text-muted-foreground" />
      </div>
    );
  }

  return (
    <div className="space-y-4">
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

      {providers.map((provider) => (
        <Card key={provider.name}>
          <CardHeader>
            <CardTitle className="text-lg">{provider.name}</CardTitle>
            <CardDescription>
              {t("server:auth.providerDescription", { provider: provider.name })}
            </CardDescription>
          </CardHeader>
          <CardContent>
            <div className="space-y-4">
              {provider.fields.map((field) => {
                const label = t(`server:auth.fields.${field.labelKey}`);
                return (
                <div key={field.key} className="space-y-2">
                  <Label htmlFor={field.key}>{label}</Label>
                  {field.secret &&
                  isSecretStored(field.key) &&
                  !editingSecrets.has(field.key) ? (
                    <div className="flex items-center gap-2">
                      <Input
                        id={field.key}
                        type="password"
                        value="******"
                        disabled
                      />
                      <Button
                        type="button"
                        variant="outline"
                        size="sm"
                        onClick={() => startEditing(field.key)}
                      >
                        {t("common:edit")}
                      </Button>
                    </div>
                  ) : (
                    <div className="flex items-center gap-2">
                      <div className="relative flex-1">
                        <Input
                          id={field.key}
                          type={
                            field.secret && !visibleSecrets.has(field.key)
                              ? "password"
                              : "text"
                          }
                          placeholder={label}
                          value={values[field.key] ?? ""}
                          onChange={(e) =>
                            setValues((prev) => ({
                              ...prev,
                              [field.key]: e.target.value,
                            }))
                          }
                          disabled={setParam.isPending}
                        />
                        {field.secret && (
                          <Button
                            type="button"
                            variant="ghost"
                            size="sm"
                            className="absolute right-1 top-1/2 -translate-y-1/2 h-7 w-7 p-0"
                            onClick={() => toggleSecretVisibility(field.key)}
                          >
                            {visibleSecrets.has(field.key) ? (
                              <EyeOff className="h-4 w-4" />
                            ) : (
                              <Eye className="h-4 w-4" />
                            )}
                          </Button>
                        )}
                      </div>
                      {field.secret && editingSecrets.has(field.key) && (
                        <Button
                          type="button"
                          variant="outline"
                          size="sm"
                          onClick={() => cancelEditing(field.key)}
                        >
                          {t("common:cancel")}
                        </Button>
                      )}
                    </div>
                  )}
                </div>
                );
              })}
              <Button
                onClick={() => handleSave(provider.name)}
                disabled={setParam.isPending}
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
            </div>
          </CardContent>
        </Card>
      ))}
    </div>
  );
}
