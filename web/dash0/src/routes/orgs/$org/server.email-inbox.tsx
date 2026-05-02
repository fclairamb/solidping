import { useEffect, useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { AlertCircle, Check, Eye, EyeOff, Loader2, RefreshCw, Send } from "lucide-react";
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
import { ApiError } from "@/api/client";
import { useSystemParameters } from "@/api/hooks";
import {
  type EmailInboxConfig,
  useEmailInboxStatus,
  useSaveEmailInboxConfig,
  useSyncEmailInbox,
  useTestEmailInbox,
} from "@/api/email-inbox";

export const Route = createFileRoute("/orgs/$org/server/email-inbox")({
  component: EmailInboxPage,
});

function relativeTime(iso?: string): string {
  if (!iso) return "never";
  const ts = Date.parse(iso);
  if (Number.isNaN(ts)) return iso;
  const diffSec = Math.floor((Date.now() - ts) / 1000);
  if (diffSec < 5) return "just now";
  if (diffSec < 60) return `${diffSec}s ago`;
  if (diffSec < 3600) return `${Math.floor(diffSec / 60)}m ago`;
  if (diffSec < 86400) return `${Math.floor(diffSec / 3600)}h ago`;
  return `${Math.floor(diffSec / 86400)}d ago`;
}

function EmailInboxPage() {
  const { data: params, isLoading } = useSystemParameters();
  const save = useSaveEmailInboxConfig();
  const test = useTestEmailInbox();
  const sync = useSyncEmailInbox();
  const { data: status } = useEmailInboxStatus();

  const [enabled, setEnabled] = useState(false);
  const [sessionUrl, setSessionUrl] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [editingPassword, setEditingPassword] = useState(false);
  const [showPassword, setShowPassword] = useState(false);
  const [addressDomain, setAddressDomain] = useState("");
  const [mailboxName, setMailboxName] = useState("Inbox");
  const [processedMailboxName, setProcessedMailboxName] = useState("Processed");
  const [pollIntervalSeconds, setPollIntervalSeconds] = useState("60");
  const [processedRetentionDays, setProcessedRetentionDays] = useState("30");
  const [failedRetentionDays, setFailedRetentionDays] = useState("7");
  const [rewriteBaseUrl, setRewriteBaseUrl] = useState("");

  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  const [testResult, setTestResult] = useState<{ ok: boolean; message: string } | null>(null);

  useEffect(() => {
    if (!params) return;
    const param = params.find((p) => p.key === "email_inbox");
    if (!param) return;

    const cfg = (param.value ?? {}) as Partial<EmailInboxConfig>;
    setEnabled(cfg.enabled === true);
    setSessionUrl(cfg.sessionUrl ?? "");
    setUsername(cfg.username ?? "");
    setAddressDomain(cfg.addressDomain ?? "");
    setMailboxName(cfg.mailboxName ?? "Inbox");
    setProcessedMailboxName(cfg.processedMailboxName ?? "Processed");
    setPollIntervalSeconds(String(cfg.pollIntervalSeconds ?? 60));
    setProcessedRetentionDays(String(cfg.processedRetentionDays ?? 30));
    setFailedRetentionDays(String(cfg.failedRetentionDays ?? 7));
    setRewriteBaseUrl(cfg.rewriteBaseUrl ?? "");
  }, [params]);

  const isSecretSet = params?.find((p) => p.key === "email_inbox")?.secret;

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setSaved(false);

    const cfg: EmailInboxConfig = {
      enabled,
      sessionUrl,
      username,
      addressDomain,
      mailboxName,
      processedMailboxName,
      pollIntervalSeconds: parseInt(pollIntervalSeconds, 10) || 60,
      processedRetentionDays: parseInt(processedRetentionDays, 10) || 30,
      failedRetentionDays: parseInt(failedRetentionDays, 10) || 7,
      rewriteBaseUrl: rewriteBaseUrl || undefined,
    };

    // Empty password keeps existing; non-empty replaces
    if (editingPassword && password) {
      cfg.password = password;
    }

    try {
      await save.mutateAsync(cfg);
      setEditingPassword(false);
      setPassword("");
      setSaved(true);
      setTimeout(() => setSaved(false), 3000);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Failed to save settings");
    }
  };

  const handleTest = async () => {
    setTestResult(null);
    try {
      const result = await test.mutateAsync(undefined);
      setTestResult({ ok: result.ok, message: `Connected. Mailboxes: ${result.mailboxes.join(", ")}` });
    } catch (err) {
      setTestResult({
        ok: false,
        message: err instanceof ApiError ? err.message : "Test failed",
      });
    }
  };

  const handleSync = async () => {
    setTestResult(null);
    try {
      await sync.mutateAsync();
      setTestResult({ ok: true, message: "Sync triggered" });
    } catch (err) {
      setTestResult({
        ok: false,
        message: err instanceof ApiError ? err.message : "Sync failed",
      });
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
    <div className="space-y-4">
      <Card>
        <CardHeader>
          <CardTitle>Email Inbox</CardTitle>
          <CardDescription>
            Configure the shared JMAP inbox used by passive email checks. Fastmail, Stalwart, and other JMAP-capable providers are supported.
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
                id="emailInboxEnabled"
                data-testid="email-inbox-enabled"
                checked={enabled}
                onCheckedChange={setEnabled}
                disabled={save.isPending}
              />
              <Label htmlFor="emailInboxEnabled">Enable email inbox</Label>
            </div>

            <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
              <div className="space-y-2">
                <Label htmlFor="sessionUrl">JMAP Session URL</Label>
                <Input
                  id="sessionUrl"
                  type="url"
                  placeholder="https://jmap.example.com/.well-known/jmap"
                  value={sessionUrl}
                  onChange={(e) => setSessionUrl(e.target.value)}
                  disabled={save.isPending}
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="addressDomain">Address Domain</Label>
                <Input
                  id="addressDomain"
                  type="text"
                  placeholder="ingest.solidping.example"
                  value={addressDomain}
                  onChange={(e) => setAddressDomain(e.target.value)}
                  disabled={save.isPending}
                />
              </div>
            </div>

            <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
              <div className="space-y-2">
                <Label htmlFor="username">Username</Label>
                <Input
                  id="username"
                  type="text"
                  placeholder="ingest@example.com"
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                  disabled={save.isPending}
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="password">Password</Label>
                {!editingPassword && isSecretSet ? (
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
                  <div className="relative">
                    <Input
                      id="password"
                      type={showPassword ? "text" : "password"}
                      placeholder="JMAP password or app token"
                      value={password}
                      onChange={(e) => setPassword(e.target.value)}
                      disabled={save.isPending}
                    />
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      className="absolute right-1 top-1/2 -translate-y-1/2 h-7 w-7 p-0"
                      onClick={() => setShowPassword(!showPassword)}
                    >
                      {showPassword ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
                    </Button>
                  </div>
                )}
              </div>
            </div>

            <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
              <div className="space-y-2">
                <Label htmlFor="mailboxName">Inbox Mailbox</Label>
                <Input
                  id="mailboxName"
                  type="text"
                  value={mailboxName}
                  onChange={(e) => setMailboxName(e.target.value)}
                  disabled={save.isPending}
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="processedMailboxName">Processed Mailbox</Label>
                <Input
                  id="processedMailboxName"
                  type="text"
                  value={processedMailboxName}
                  onChange={(e) => setProcessedMailboxName(e.target.value)}
                  disabled={save.isPending}
                />
              </div>
            </div>

            <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
              <div className="space-y-2">
                <Label htmlFor="pollIntervalSeconds">Poll Interval (seconds)</Label>
                <Input
                  id="pollIntervalSeconds"
                  type="number"
                  min="5"
                  value={pollIntervalSeconds}
                  onChange={(e) => setPollIntervalSeconds(e.target.value)}
                  disabled={save.isPending}
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="processedRetentionDays">Processed Retention (days)</Label>
                <Input
                  id="processedRetentionDays"
                  type="number"
                  min="1"
                  value={processedRetentionDays}
                  onChange={(e) => setProcessedRetentionDays(e.target.value)}
                  disabled={save.isPending}
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="failedRetentionDays">Failed Retention (days)</Label>
                <Input
                  id="failedRetentionDays"
                  type="number"
                  min="1"
                  value={failedRetentionDays}
                  onChange={(e) => setFailedRetentionDays(e.target.value)}
                  disabled={save.isPending}
                />
              </div>
            </div>

            <div className="space-y-2">
              <Label htmlFor="rewriteBaseUrl">Rewrite Base URL (advanced)</Label>
              <Input
                id="rewriteBaseUrl"
                type="text"
                placeholder="https://proxy.example.com (optional)"
                value={rewriteBaseUrl}
                onChange={(e) => setRewriteBaseUrl(e.target.value)}
                disabled={save.isPending}
              />
              <p className="text-xs text-muted-foreground">
                Used to substitute the JMAP server's published base URL when behind a reverse proxy.
              </p>
            </div>

            <Button type="submit" disabled={save.isPending}>
              {save.isPending ? (
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
          <CardTitle>Status</CardTitle>
          <CardDescription>Live state of the JMAP supervisor.</CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          <div className="flex items-center gap-2 text-sm">
            <span className="text-muted-foreground">Connection:</span>
            {status?.connected ? (
              <span className="inline-flex items-center gap-1 rounded-full bg-green-500/10 px-2 py-0.5 text-green-700 dark:text-green-400">
                <Check className="h-3 w-3" />
                Connected
              </span>
            ) : (
              <span className="inline-flex items-center gap-1 rounded-full bg-muted px-2 py-0.5 text-muted-foreground">
                <AlertCircle className="h-3 w-3" />
                {status?.enabled ? "Disconnected" : "Disabled"}
              </span>
            )}
          </div>
          <div className="flex items-center gap-2 text-sm">
            <span className="text-muted-foreground">Last sync:</span>
            <span>{relativeTime(status?.lastSyncedAt)}</span>
          </div>
          {status?.addressDomain && (
            <div className="flex items-center gap-2 text-sm">
              <span className="text-muted-foreground">Address domain:</span>
              <code className="font-mono">{status.addressDomain}</code>
            </div>
          )}
          {status?.lastError && (
            <Alert variant="destructive">
              <AlertCircle className="h-4 w-4" />
              <AlertDescription>{status.lastError}</AlertDescription>
            </Alert>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Actions</CardTitle>
        </CardHeader>
        <CardContent>
          <div className="space-y-3">
            {testResult && (
              <Alert variant={testResult.ok ? "default" : "destructive"}>
                {testResult.ok ? (
                  <Check className="h-4 w-4" />
                ) : (
                  <AlertCircle className="h-4 w-4" />
                )}
                <AlertDescription>{testResult.message}</AlertDescription>
              </Alert>
            )}
            <div className="flex gap-2">
              <Button
                type="button"
                variant="outline"
                data-testid="email-inbox-test-btn"
                onClick={handleTest}
                disabled={test.isPending}
              >
                {test.isPending ? (
                  <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                ) : (
                  <Send className="mr-2 h-4 w-4" />
                )}
                Test Connection
              </Button>
              <Button
                type="button"
                variant="outline"
                data-testid="email-inbox-sync-btn"
                onClick={handleSync}
                disabled={sync.isPending}
              >
                {sync.isPending ? (
                  <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                ) : (
                  <RefreshCw className="mr-2 h-4 w-4" />
                )}
                Sync Now
              </Button>
            </div>
          </div>
        </CardContent>
      </Card>
    </div>
  );
}
