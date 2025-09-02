import { useState, useEffect } from "react";
import { createFileRoute } from "@tanstack/react-router";
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
        setError("An unexpected error occurred");
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
        <CardTitle>Mail</CardTitle>
        <CardDescription>
          Configure SMTP email settings for notifications and invitations.
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
              <AlertDescription>Settings saved.</AlertDescription>
            </Alert>
          )}

          <div className="flex items-center gap-3">
            <Switch
              id="emailEnabled"
              checked={enabled}
              onCheckedChange={setEnabled}
              disabled={setParam.isPending}
            />
            <Label htmlFor="emailEnabled">Enable email sending</Label>
          </div>

          <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
            <div className="space-y-2">
              <Label htmlFor="host">SMTP Host</Label>
              <Input
                id="host"
                placeholder="smtp.example.com"
                value={host}
                onChange={(e) => setHost(e.target.value)}
                disabled={setParam.isPending}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="port">SMTP Port</Label>
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
              <Label htmlFor="username">Username</Label>
              <Input
                id="username"
                placeholder="user@example.com"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                disabled={setParam.isPending}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="password">Password</Label>
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
                    Edit
                  </Button>
                </div>
              ) : (
                <div className="flex items-center gap-2">
                  <div className="relative flex-1">
                    <Input
                      id="password"
                      type={showPassword ? "text" : "password"}
                      placeholder="SMTP password"
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
                      Cancel
                    </Button>
                  )}
                </div>
              )}
            </div>
          </div>

          <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
            <div className="space-y-2">
              <Label htmlFor="authType">Auth Type</Label>
              <Select
                value={authType}
                onValueChange={setAuthType}
                disabled={setParam.isPending}
              >
                <SelectTrigger id="authType">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="plain">Plain</SelectItem>
                  <SelectItem value="login">Login</SelectItem>
                  <SelectItem value="cram-md5">CRAM-MD5</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-2">
              <Label htmlFor="protocol">Encryption</Label>
              <Select
                value={protocol}
                onValueChange={setProtocol}
                disabled={setParam.isPending}
              >
                <SelectTrigger id="protocol">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="none">None</SelectItem>
                  <SelectItem value="starttls">STARTTLS</SelectItem>
                  <SelectItem value="ssl">SSL/TLS</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>

          <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
            <div className="space-y-2">
              <Label htmlFor="from">From Address</Label>
              <Input
                id="from"
                type="email"
                placeholder="noreply@example.com"
                value={from}
                onChange={(e) => setFrom(e.target.value)}
                disabled={setParam.isPending}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="fromName">From Name</Label>
              <Input
                id="fromName"
                placeholder="SolidPing"
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
                Saving...
              </>
            ) : (
              "Save"
            )}
          </Button>
        </form>
      </CardContent>
    </Card>

    <Card>
      <CardHeader>
        <CardTitle>Test Email</CardTitle>
        <CardDescription>
          Send a test email to verify your SMTP configuration is working.
        </CardDescription>
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
              <Label htmlFor="testRecipient">Recipient Email</Label>
              <Input
                id="testRecipient"
                type="email"
                placeholder="recipient@example.com"
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
                    setTestError("An unexpected error occurred");
                  }
                }
              }}
            >
              {testEmail.isPending ? (
                <>
                  <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                  Sending...
                </>
              ) : (
                <>
                  <Send className="mr-2 h-4 w-4" />
                  Send Test Email
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
