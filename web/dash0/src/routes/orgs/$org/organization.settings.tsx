import { useState, useEffect } from "react";
import { useTranslation } from "react-i18next";
import { createFileRoute } from "@tanstack/react-router";
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
import { AlertCircle, Check, Loader2 } from "lucide-react";
import { ApiError } from "@/api/client";
import { useOrgSettings, useUpdateOrgSettings } from "@/api/hooks";

export const Route = createFileRoute("/orgs/$org/organization/settings")({
  component: SettingsPage,
});

function SettingsPage() {
  const { t } = useTranslation("org");
  const { t: tc } = useTranslation("common");
  const { org } = Route.useParams();
  const { data: settings, isLoading } = useOrgSettings(org);
  const updateSettings = useUpdateOrgSettings(org);

  const [emailPattern, setEmailPattern] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [fieldError, setFieldError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  const [testEmail, setTestEmail] = useState("");

  useEffect(() => {
    if (settings) {
      setEmailPattern(settings.registrationEmailPattern || "");
    }
  }, [settings]);

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setFieldError(null);
    setSaved(false);

    try {
      await updateSettings.mutateAsync({
        registrationEmailPattern: emailPattern,
      });
      setSaved(true);
      setTimeout(() => setSaved(false), 3000);
    } catch (err) {
      if (err instanceof ApiError) {
        if (err.code === "INVALID_AUTO_JOIN_REGEX") {
          setFieldError(err.message);
        } else {
          setError(err.message);
        }
      } else {
        setError(t("settings.unexpectedError"));
      }
    }
  };

  let testResult: "match" | "no-match" | null = null;
  if (emailPattern && testEmail) {
    try {
      testResult = new RegExp(emailPattern).test(testEmail) ? "match" : "no-match";
    } catch {
      testResult = "no-match";
    }
  }

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
        <CardTitle>{t("settings.autoJoin")}</CardTitle>
        <CardDescription>
          {t("settings.autoJoinFullDescription")}
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
              <AlertDescription>{t("settings.saved")}</AlertDescription>
            </Alert>
          )}

          <div className="space-y-2">
            <Label htmlFor="emailPattern">{t("settings.emailPattern")}</Label>
            <Input
              id="emailPattern"
              type="text"
              placeholder={t("settings.emailPlaceholder")}
              value={emailPattern}
              onChange={(e) => {
                setEmailPattern(e.target.value);
                setFieldError(null);
              }}
              disabled={updateSettings.isPending}
              aria-invalid={fieldError != null}
            />
            {fieldError && (
              <p className="text-xs text-destructive">{fieldError}</p>
            )}
            <p className="text-xs text-muted-foreground"
              dangerouslySetInnerHTML={{ __html: t("settings.autoJoinHelp") }}
            />
          </div>

          <div className="space-y-2">
            <Label htmlFor="testEmail">
              {t("settings.testAgainstEmail", "Test against email")}
            </Label>
            <Input
              id="testEmail"
              type="email"
              value={testEmail}
              onChange={(e) => setTestEmail(e.target.value)}
              placeholder="user@example.com"
            />
            {testResult && (
              <p
                className={`text-xs ${
                  testResult === "match"
                    ? "text-green-600"
                    : "text-muted-foreground"
                }`}
              >
                {testResult === "match"
                  ? t("settings.testMatch", "✓ matches the pattern")
                  : t("settings.testNoMatch", "✗ does not match the pattern")}
              </p>
            )}
          </div>

          <Button
            type="submit"
            disabled={updateSettings.isPending}
          >
            {updateSettings.isPending ? (
              <>
                <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                {tc("saving")}
              </>
            ) : (
              tc("save")
            )}
          </Button>
        </form>
      </CardContent>
    </Card>
  );
}
