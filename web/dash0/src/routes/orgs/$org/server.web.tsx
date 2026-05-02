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
import { useSystemParameters, useSetSystemParameter } from "@/api/hooks";

export const Route = createFileRoute("/orgs/$org/server/web")({
  component: WebSettingsPage,
});

function WebSettingsPage() {
  const { t } = useTranslation(["server", "common"]);
  const { data: params, isLoading } = useSystemParameters();
  const setParam = useSetSystemParameter();

  const [baseUrl, setBaseUrl] = useState("");
  const [jwtSecret, setJwtSecret] = useState("");
  const [editingJwt, setEditingJwt] = useState(false);
  const [showJwt, setShowJwt] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  useEffect(() => {
    if (params) {
      const get = (key: string) =>
        params.find((p) => p.key === key)?.value as string ?? "";
      setBaseUrl(get("base_url"));
      setJwtSecret(get("jwt_secret"));
    }
  }, [params]);

  const isJwtSecret = params?.find((p) => p.key === "jwt_secret")?.secret;

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setSaved(false);

    try {
      await setParam.mutateAsync({ key: "base_url", value: baseUrl });
      if (editingJwt) {
        await setParam.mutateAsync({
          key: "jwt_secret",
          value: jwtSecret,
          secret: true,
        });
        setEditingJwt(false);
      }
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

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-12">
        <Loader2 className="h-8 w-8 animate-spin text-muted-foreground" />
      </div>
    );
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("server:web.title")}</CardTitle>
        <CardDescription>{t("server:web.description")}</CardDescription>
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
            <Label htmlFor="baseUrl">{t("server:web.baseUrl")}</Label>
            <Input
              id="baseUrl"
              type="url"
              placeholder={t("server:web.baseUrlPlaceholder")}
              value={baseUrl}
              onChange={(e) => setBaseUrl(e.target.value)}
              disabled={setParam.isPending}
            />
            <p className="text-xs text-muted-foreground">
              {t("server:web.baseUrlHelp")}
            </p>
          </div>

          <div className="space-y-2">
            <Label htmlFor="jwtSecret">{t("server:web.jwtSecret")}</Label>
            {!editingJwt && isJwtSecret ? (
              <div className="flex items-center gap-2">
                <Input
                  id="jwtSecret"
                  type="password"
                  value="******"
                  disabled
                />
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => {
                    setEditingJwt(true);
                    setJwtSecret("");
                  }}
                >
                  {t("common:edit")}
                </Button>
              </div>
            ) : (
              <div className="flex items-center gap-2">
                <div className="relative flex-1">
                  <Input
                    id="jwtSecret"
                    type={showJwt ? "text" : "password"}
                    placeholder={t("server:web.jwtSecretPlaceholder")}
                    value={jwtSecret}
                    onChange={(e) => setJwtSecret(e.target.value)}
                    disabled={setParam.isPending}
                  />
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    className="absolute right-1 top-1/2 -translate-y-1/2 h-7 w-7 p-0"
                    onClick={() => setShowJwt(!showJwt)}
                  >
                    {showJwt ? (
                      <EyeOff className="h-4 w-4" />
                    ) : (
                      <Eye className="h-4 w-4" />
                    )}
                  </Button>
                </div>
                {editingJwt && (
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    onClick={() => {
                      setEditingJwt(false);
                      const original =
                        params?.find((p) => p.key === "jwt_secret")?.value as string ?? "";
                      setJwtSecret(original);
                    }}
                  >
                    {t("common:cancel")}
                  </Button>
                )}
              </div>
            )}
            <p className="text-xs text-muted-foreground">
              {t("server:web.jwtSecretHelp")}
            </p>
          </div>

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
