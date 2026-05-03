import { useState, useEffect } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Alert, AlertDescription } from "@/components/ui/alert";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { AlertCircle, Check, Eye, EyeOff, Loader2, Send } from "lucide-react";
import { ApiError } from "@/api/client";
import { useSystemParameters, useSetSystemParameter, useTestEmail } from "@/api/hooks";

export const Route = createFileRoute("/orgs/$org/server/mail")({
  component: MailSettingsPage,
});

function MailSettingsPage() {
  const { t } = useTranslation(["server", "common"]);
  const { data: params, isLoading } = useSystemParameters();
  const setParam = useSetSystemParameter();
  const testEmail = useTestEmail();

  const [enabled, setEnabled] = useState(false);
  const [host, setHost] = useState("");
  const [port, setPort] = useState("587");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [editingPassword, setEditingPassword] = useState(false);
  const [showPassword, setShowPassword] = useState(false);
  const [authType, setAuthType] = useState("login");
  const [protocol, setProtocol] = useState("starttls");
  const [from, setFrom] = useState("");
  const [fromName, setFromName] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  const [testRecipient, setTestRecipient] = useState("");
  const [testResult, setTestResult] = useState<{ sent: boolean; message: string } | null>(null);
  const [testError, setTestError] = useState<string | null>(null);

  useEffect(() => {
    if (params) {
      const get = (key: string) =>
        params.find((p) => p.key === key)?.value;
      setEnabled(get("email.enabled") === true);
      setHost((get("email.host") as string) ?? "");
      const portVal = get("email.port");
      setPort(portVal != null ? String(portVal) : "587");
      setUsername((get("email.username") as string) ?? "");
      setPassword((get("email.password") as string) ?? "");
      setAuthType((get("email.auth_type") as string) || "login");
      setProtocol((get("email.protocol") as string) || "starttls");
      setFrom((get("email.from") as string) ?? "");
      setFromName((get("email.from_name") as string) ?? "");
    }
  }, [params]);

  const isPasswordSecret = params?.find((p) => p.key === "email.password")?.secret;

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setSaved(false);

    try {
      await Promise.all([
        setParam.mutateAsync({ key: "email.enabled", value: enabled }),
        setParam.mutateAsync({ key: "email.host", value: host }),
        setParam.mutateAsync({ key: "email.port", value: parseInt(port, 10) }),
        setParam.mutateAsync({ key: "email.username", value: username }),
        setParam.mutateAsync({ key: "email.auth_type", value: authType }),
        setParam.mutateAsync({ key: "email.protocol", value: protocol }),
        setParam.mutateAsync({ key: "email.from", value: from }),
        setParam.mutateAsync({ key: "email.from_name", value: fromName }),
        ...(editingPassword
          ? [
              setParam.mutateAsync({
                key: "email.password",
                value: password,
                secret: true,
              }),
            ]
          : []),
      ]);
      setEditingPassword(false);
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
    <>
    <Card>
      <CardHeader>
        <CardTitle>{t("server:mail.title")}</CardTitle>
        <CardDescription>{t("server:mail.description")}</CardDescription>
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
              id="emailEnabled"
              checked={enabled}
              onCheckedChange={setEnabled}
              disabled={setParam.isPending}
            />
            <Label htmlFor="emailEnabled">{t("server:mail.enable")}</Label>
          </div>

          <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
            <div className="space-y-2">
              <Label htmlFor="host">{t("server:mail.host")}</Label>
              <Input
                id="host"
                placeholder={t("server:mail.hostPlaceholder")}
                value={host}
                onChange={(e) => setHost(e.target.value)}
                disabled={setParam.isPending}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="port">{t("server:mail.port")}</Label>
              <Input
                id="port"
                type="number"
                placeholder="587"
                value={port}
                onChange={(e) => setPort(e.target.value)}
                disabled={setParam.isPending}
              />
            </div>
          </div>

          <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
            <div className="space-y-2">
              <Label htmlFor="username">{t("server:mail.username")}</Label>
              <Input
                id="username"
                placeholder={t("server:mail.usernamePlaceholder")}
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                disabled={setParam.isPending}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="password">{t("server:mail.password")}</Label>
              {!editingPassword && isPasswordSecret ? (
                <div className="flex items-center gap-2">
                  <Input id="password" type="password" value="******" disabled />
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    onClick={() => {
                      setEditingPassword(true);
                      setPassword("");
                    }}
                  >
                    {t("common:edit")}
                  </Button>
                </div>
              ) : (
                <div className="flex items-center gap-2">
                  <div className="relative flex-1">
                    <Input
                      id="password"
                      type={showPassword ? "text" : "password"}
                      placeholder={t("server:mail.passwordPlaceholder")}
                      value={password}
                      onChange={(e) => setPassword(e.target.value)}
                      disabled={setParam.isPending}
                    />
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      className="absolute right-1 top-1/2 -translate-y-1/2 h-7 w-7 p-0"
                      onClick={() => setShowPassword(!showPassword)}
                    >
                      {showPassword ? (
                        <EyeOff className="h-4 w-4" />
                      ) : (
                        <Eye className="h-4 w-4" />
                      )}
                    </Button>
                  </div>
                  {editingPassword && (
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      onClick={() => {
                        setEditingPassword(false);
                        const original =
                          params?.find((p) => p.key === "email.password")
                            ?.value as string ?? "";
                        setPassword(original);
                      }}
                    >
                      {t("common:cancel")}
                    </Button>
                  )}
                </div>
              )}
            </div>
          </div>

          <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
            <div className="space-y-2">
              <Label htmlFor="authType">{t("server:mail.authType")}</Label>
              <Select
                value={authType}
                onValueChange={setAuthType}
                disabled={setParam.isPending}
              >
                <SelectTrigger id="authType">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="plain">{t("server:mail.authOptions.plain")}</SelectItem>
                  <SelectItem value="login">{t("server:mail.authOptions.login")}</SelectItem>
                  <SelectItem value="cram-md5">{t("server:mail.authOptions.cramMd5")}</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-2">
              <Label htmlFor="protocol">{t("server:mail.encryption")}</Label>
              <Select
                value={protocol}
                onValueChange={setProtocol}
                disabled={setParam.isPending}
              >
                <SelectTrigger id="protocol">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="none">{t("server:mail.encryptionOptions.none")}</SelectItem>
                  <SelectItem value="starttls">{t("server:mail.encryptionOptions.starttls")}</SelectItem>
                  <SelectItem value="ssl">{t("server:mail.encryptionOptions.ssl")}</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>

          <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
            <div className="space-y-2">
              <Label htmlFor="from">{t("server:mail.from")}</Label>
              <Input
                id="from"
                type="email"
                placeholder={t("server:mail.fromPlaceholder")}
                value={from}
                onChange={(e) => setFrom(e.target.value)}
                disabled={setParam.isPending}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="fromName">{t("server:mail.fromName")}</Label>
              <Input
                id="fromName"
                placeholder={t("server:mail.fromNamePlaceholder")}
                value={fromName}
                onChange={(e) => setFromName(e.target.value)}
                disabled={setParam.isPending}
              />
            </div>
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

    <Card>
      <CardHeader>
        <CardTitle>{t("server:mail.test.title")}</CardTitle>
        <CardDescription>{t("server:mail.test.description")}</CardDescription>
      </CardHeader>
      <CardContent>
        <div className="space-y-4">
          {testError && (
            <Alert variant="destructive">
              <AlertCircle className="h-4 w-4" />
              <AlertDescription>{testError}</AlertDescription>
            </Alert>
          )}
          {testResult && (
            <Alert variant={testResult.sent ? "default" : "destructive"}>
              {testResult.sent ? (
                <Check className="h-4 w-4" />
              ) : (
                <AlertCircle className="h-4 w-4" />
              )}
              <AlertDescription>{testResult.message}</AlertDescription>
            </Alert>
          )}
          <div className="flex items-end gap-2">
            <div className="flex-1 space-y-2">
              <Label htmlFor="testRecipient">{t("server:mail.test.recipient")}</Label>
              <Input
                id="testRecipient"
                type="email"
                placeholder={t("server:mail.test.recipientPlaceholder")}
                value={testRecipient}
                onChange={(e) => setTestRecipient(e.target.value)}
                disabled={testEmail.isPending}
              />
            </div>
            <Button
              type="button"
              data-testid="send-test-email-button"
              disabled={testEmail.isPending || !testRecipient}
              onClick={async () => {
                setTestResult(null);
                setTestError(null);
                try {
                  const result = await testEmail.mutateAsync(testRecipient);
                  setTestResult(result);
                } catch (err) {
                  if (err instanceof ApiError) {
                    setTestError(err.message);
                  } else {
                    setTestError(t("server:unexpectedError"));
                  }
                }
              }}
            >
              {testEmail.isPending ? (
                <>
                  <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                  {t("server:mail.test.sending")}
                </>
              ) : (
                <>
                  <Send className="mr-2 h-4 w-4" />
                  {t("server:mail.test.send")}
                </>
              )}
            </Button>
          </div>
        </div>
      </CardContent>
    </Card>
    </>
  );
}
