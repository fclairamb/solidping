import { useState } from "react";
import { Trans, useTranslation } from "react-i18next";
import {
  AlertTriangle,
  Copy,
  ExternalLink,
  Loader2,
  RefreshCw,
} from "lucide-react";
import { toast } from "sonner";
import { useRotateHeartbeatToken, useUpdateCheck } from "@/api/hooks";
import { useHeartbeatPush } from "@/api/public-config";
import { useEmailAddressDomain, emailCheckAddress } from "@/api/email-inbox";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { Label } from "@/components/ui/label";
import { CollapsibleCode } from "@/components/shared/copyable-code";
import { CollapsibleSection } from "@/components/ui/collapsible-section";
import { DocsLink } from "@/components/shared/docs-link";
import { SecretText } from "@/components/ui/secret-text";
import { NO_CAPTURE_CLASS } from "@/lib/no-capture";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";

// The endpoint cards of the heartbeat and email checks, shown on the check
// detail page. Both render the check's token on every visit (the heartbeat
// URL and its curl / push commands, the email address): each is a bearer
// credential, so everything built from it renders under `ph-no-capture`
// (spec 2026-11-15-04), out of the unmasked session replay.

export function HeartbeatEndpoint({
  org,
  check,
}: {
  org: string;
  check: { slug?: string; uid: string; config?: Record<string, unknown> };
}) {
  const { t } = useTranslation("checks");
  const token = check.config?.token as string;
  const identifier = check.slug || check.uid;
  const heartbeatUrl = `${window.location.origin}/api/v1/heartbeat/${org}/${identifier}?token=${token}`;
  const curlCommand = `curl "${heartbeatUrl}"`;
  const rotateToken = useRotateHeartbeatToken(org, check.uid);

  const copyToClipboard = (text: string) => {
    navigator.clipboard.writeText(text);
    toast.success(t("detail.toast.copied"));
  };

  const handleRegenerate = async () => {
    try {
      await rotateToken.mutateAsync();
      toast.success(t("endpoints.heartbeat.regenerated"));
    } catch {
      toast.error(t("endpoints.heartbeat.regenerateFailed"));
    }
  };

  return (
    <div>
      <div className="flex items-center justify-between gap-2 mb-2">
        <div className="text-sm font-medium text-muted-foreground" data-testid="heartbeat-endpoint-title">
          {t("endpoints.heartbeat.title")}
        </div>
        <AlertDialog>
          <AlertDialogTrigger asChild>
            <Button
              type="button"
              variant="outline"
              size="sm"
              data-testid="heartbeat-regenerate-token"
            >
              <RefreshCw className="h-3.5 w-3.5 mr-1.5" />
              {t("endpoints.heartbeat.regenerate")}
            </Button>
          </AlertDialogTrigger>
          <AlertDialogContent data-testid="heartbeat-regenerate-dialog">
            <AlertDialogHeader>
              <AlertDialogTitle>
                {t("endpoints.heartbeat.regenerateTitle")}
              </AlertDialogTitle>
              <AlertDialogDescription>
                {t("endpoints.heartbeat.regenerateDescription")}
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>{t("common:cancel")}</AlertDialogCancel>
              <AlertDialogAction
                onClick={handleRegenerate}
                disabled={rotateToken.isPending}
                data-testid="heartbeat-regenerate-confirm"
              >
                {rotateToken.isPending ? (
                  <>
                    <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                    {t("endpoints.heartbeat.regenerating")}
                  </>
                ) : (
                  t("endpoints.heartbeat.regenerateConfirm")
                )}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </div>
      <div className="space-y-3">
        <div className="bg-muted rounded-md p-3 text-sm font-mono break-all flex items-start gap-2">
          <SecretText as="span" className="flex-1" data-testid="heartbeat-url">
            {heartbeatUrl}
          </SecretText>
          <button
            type="button"
            data-testid="heartbeat-url-copy"
            onClick={() => copyToClipboard(heartbeatUrl)}
            className="text-muted-foreground hover:text-foreground p-0.5 rounded shrink-0"
          >
            <Copy className="h-4 w-4" />
          </button>
        </div>
        <div>
          <div className="text-xs text-muted-foreground mb-1" data-testid="heartbeat-curl-label">
            {t("endpoints.heartbeat.curl")}
          </div>
          <div className="bg-muted rounded-md p-3 text-sm font-mono break-all flex items-start gap-2">
            <SecretText as="span" className="flex-1" data-testid="heartbeat-curl">
              {curlCommand}
            </SecretText>
            <button
              type="button"
              data-testid="heartbeat-curl-copy"
              onClick={() => copyToClipboard(curlCommand)}
              className="text-muted-foreground hover:text-foreground p-0.5 rounded shrink-0"
            >
              <Copy className="h-4 w-4" />
            </button>
          </div>
        </div>
        <p className="text-xs text-muted-foreground">
          {t("endpoints.heartbeat.callerNote")}
        </p>
        <HeartbeatPushEndpoint org={org} check={check} />
      </div>
    </div>
  );
}

/**
 * Embedded push transports for a heartbeat check (spec 2026-09-01-06).
 *
 * Rendered only when the server reports a listener enabled: the TCP/UDP
 * listeners are off by default and exposing their ports is a deployment
 * decision, so advertising a `nc` one-liner nobody can reach would be worse
 * than showing nothing.
 *
 * Collapsed by default (spec 2026-09-02-01): this is an opt-in,
 * deployment-level feature that most heartbeat checks never touch, so it
 * should be discoverable rather than dominating the endpoint card. The
 * `summary` line keeps the collapsed row honest about what is enabled. The
 * rotate-token nudge is rendered *outside* the collapsible body — it is a
 * security warning and must stay visible even while the section is
 * collapsed.
 */
function HeartbeatPushEndpoint({
  org,
  check,
}: {
  org: string;
  check: { slug?: string; uid: string; config?: Record<string, unknown> };
}) {
  const { t } = useTranslation("checks");
  const updateCheck = useUpdateCheck(org, check.uid);
  const push = useHeartbeatPush();
  const requireHmac = check.config?.require_hmac === true;

  if (!push || (!push.tcpEnabled && !push.udpEnabled)) return null;

  const token = (check.config?.token as string) ?? "";
  const identifier = check.slug || check.uid;
  const host = push.host || window.location.hostname;
  const target = `${org}/${identifier}`;
  // The trailing "\\n" is TWO characters on purpose — the backslash escape
  // printf turns into the newline that frames a TCP beat. Writing a real LF
  // here would render as a trailing space (HTML collapses it), so anyone
  // retyping what they see would send an unterminated line that never frames.
  const tcpCommand = `printf 'SP1 ${target} ${token}\\n' | nc ${host} ${push.tcpPort}`;
  // UDP needs no terminator: the datagram boundary is the frame.
  const udpCommand = `printf 'SP1 ${target} ${token}' | nc -u -w1 ${host} ${push.udpPort}`;

  const summaryParts: string[] = [];
  if (push.tcpEnabled) {
    summaryParts.push(
      t("endpoints.heartbeat.push.summaryTcp", { port: push.tcpPort }),
    );
  }
  if (push.udpEnabled) {
    summaryParts.push(
      t("endpoints.heartbeat.push.summaryUdp", { port: push.udpPort }),
    );
  }
  if (requireHmac) {
    summaryParts.push(t("endpoints.heartbeat.push.summarySignedOnly"));
  }
  const summary = summaryParts.join(" · ");

  const handleToggle = async (next: boolean) => {
    try {
      // The public side of a config PATCH is REPLACE, not merge, so the whole
      // config goes back — sending only the flag would drop the ping token
      // this very card renders a URL from.
      await updateCheck.mutateAsync({
        config: { ...(check.config ?? {}), require_hmac: next },
      });
      toast.success(
        next
          ? t("endpoints.heartbeat.push.hmacEnabled")
          : t("endpoints.heartbeat.push.hmacDisabled"),
      );
    } catch {
      toast.error(t("endpoints.heartbeat.push.hmacFailed"));
    }
  };

  return (
    <div className="space-y-3 border-t pt-3" data-testid="heartbeat-push">
      <CollapsibleSection
        title={t("endpoints.heartbeat.push.title")}
        summary={summary}
        defaultOpen={false}
        data-testid="heartbeat-push-toggle"
      >
        <p className="text-xs text-muted-foreground">
          {t("endpoints.heartbeat.push.description")}
        </p>

        {push.tcpEnabled && (
          <CollapsibleCode
            label={t("endpoints.heartbeat.push.tcpLabel", {
              port: push.tcpPort,
            })}
            value={tcpCommand}
            secret
            data-testid="heartbeat-push-tcp"
          />
        )}

        {push.udpEnabled && (
          <CollapsibleCode
            label={t("endpoints.heartbeat.push.udpLabel", {
              port: push.udpPort,
            })}
            value={udpCommand}
            secret
            data-testid="heartbeat-push-udp"
          />
        )}

        <p className="text-xs text-muted-foreground">
          {t("endpoints.heartbeat.push.annotationHint")}
        </p>

        <div className="flex items-start gap-2">
          <p className="flex-1 text-xs text-muted-foreground">
            {t("endpoints.heartbeat.push.sketchDocsHint")}
          </p>
          <DocsLink href="/docs/features/embedded-push#a-minimal-arduino--esp-sketch" />
        </div>

        <div className="flex items-start gap-2">
          <Switch
            id="heartbeat-require-hmac"
            checked={requireHmac}
            disabled={updateCheck.isPending}
            onCheckedChange={handleToggle}
            data-testid="heartbeat-require-hmac"
          />
          <div className="space-y-1">
            <Label htmlFor="heartbeat-require-hmac">
              {t("endpoints.heartbeat.push.requireHmac")}
            </Label>
            <p className="text-xs text-muted-foreground">
              {t("endpoints.heartbeat.push.requireHmacHint")}
            </p>
          </div>
        </div>
      </CollapsibleSection>

      {requireHmac && (
        <Alert data-testid="heartbeat-rotate-nudge">
          <AlertTriangle className="h-4 w-4" />
          <AlertDescription>
            {t("endpoints.heartbeat.push.rotateNudge")}
          </AlertDescription>
        </Alert>
      )}
    </div>
  );
}

export function EmailEndpoint({
  check,
}: {
  check: { config?: Record<string, unknown> };
}) {
  const { t } = useTranslation("checks");
  const token = check.config?.token as string;
  const { data: domain } = useEmailAddressDomain();
  const [showHelp, setShowHelp] = useState(false);

  const copyToClipboard = (text: string) => {
    navigator.clipboard.writeText(text);
    toast.success(t("detail.toast.copied"));
  };

  if (!domain) {
    return (
      <div>
        <div className="text-sm font-medium text-muted-foreground mb-2">
          {t("endpoints.email.title")}
        </div>
        <div className="bg-muted rounded-md p-3 text-sm text-muted-foreground">
          {t("endpoints.email.notConfigured")}
        </div>
      </div>
    );
  }

  const address = emailCheckAddress(token, domain);
  const mailto = `mailto:${address}?subject=Test`;

  return (
    <div>
      <div className="text-sm font-medium text-muted-foreground mb-2">
        {t("endpoints.email.title")}
      </div>
      <div className="space-y-3">
        <div className="bg-muted rounded-md p-3 text-sm font-mono break-all flex items-start gap-2">
          <SecretText as="span" className="flex-1" data-testid="email-check-address">
            {address}
          </SecretText>
          <button
            type="button"
            data-testid="email-check-copy-btn"
            onClick={() => copyToClipboard(address)}
            className="text-muted-foreground hover:text-foreground p-0.5 rounded shrink-0"
          >
            <Copy className="h-4 w-4" />
          </button>
        </div>
        <div>
          <a
            href={mailto}
            data-testid="email-check-send-test"
            className={`${NO_CAPTURE_CLASS} text-sm text-primary hover:underline inline-flex items-center gap-1`}
          >
            {t("endpoints.email.sendTest")}
            <ExternalLink className="h-3 w-3" />
          </a>
        </div>
        <button
          type="button"
          data-testid="email-check-options-toggle"
          onClick={() => setShowHelp((v) => !v)}
          className="text-sm text-muted-foreground hover:text-foreground"
        >
          {showHelp
            ? t("endpoints.email.hideOptions")
            : t("endpoints.email.showOptions")}
        </button>
        {showHelp && (
          <div className="bg-muted rounded-md p-3 text-sm space-y-2">
            <p>{t("endpoints.email.help.intro")}</p>
            <ul className="list-disc pl-5 space-y-1 text-muted-foreground">
              <li className={NO_CAPTURE_CLASS} data-testid="email-check-help-plus">
                <Trans
                  i18nKey="checks:endpoints.email.help.plus"
                  values={{ token, domain }}
                  components={{ code: <code className="font-mono" /> }}
                />
              </li>
              <li>
                <Trans
                  i18nKey="checks:endpoints.email.help.header"
                  components={{ code: <code className="font-mono" /> }}
                />
              </li>
              <li>
                <Trans
                  i18nKey="checks:endpoints.email.help.subject"
                  components={{ code: <code className="font-mono" /> }}
                />
              </li>
            </ul>
            <p className="text-xs text-muted-foreground">
              <Trans
                i18nKey="checks:endpoints.email.help.priority"
                components={{ code: <code className="font-mono" /> }}
              />
            </p>
          </div>
        )}
      </div>
    </div>
  );
}

/**
 * The target column of an email check in the checks list: the address,
 * truncated to the token's first 8 characters. No `title` carries the full
 * address: the token alone routes mail to the check, and session replay
 * records DOM attributes as well as text (spec 2026-11-15-04).
 */
export function EmailCheckTarget({ token, domain }: { token: string; domain: string }) {
  return (
    <span className="font-mono text-xs" data-testid="check-row-email-target">
      {`${token.slice(0, 8)}…@${domain}`}
    </span>
  );
}
