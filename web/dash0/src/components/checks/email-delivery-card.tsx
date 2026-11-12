// EmailDeliveryCard renders the send-mode SMTP enrichment on an email check's
// result (spec 2026-08-19-04): when the receiving JMAP handler found the
// X-SolidPing-Check / X-SolidPing-Sent-At headers, the result output carries
// sourceCheckUid/sentAt/latencyMs. This card shows the latency and links back
// to the sending SMTP check. Renders nothing when those fields are absent —
// the overwhelming majority of email-check results (human/heartbeat-style
// uses, or a header stripped by an intermediate MTA), so it is safe to mount
// unconditionally next to DnsblCard on the result-detail page.
import { useTranslation } from "react-i18next";
import { Link } from "@tanstack/react-router";
import { Mail } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";

type Output = Record<string, unknown>;

// EMAIL_DELIVERY_OUTPUT_KEYS are the keys this card renders itself. The
// result-detail page strips these from its raw JSON dump so nothing is shown
// twice — mirrors DNSBL_OUTPUT_KEYS.
export const EMAIL_DELIVERY_OUTPUT_KEYS = ["sourceCheckUid", "sentAt", "latencyMs", "auth"] as const;

function isEmailDeliveryOutput(output: Output | undefined): output is Output & {
  sourceCheckUid: string;
} {
  return Boolean(output) && typeof output?.sourceCheckUid === "string" && output.sourceCheckUid !== "";
}

function formatLatency(latencyMs: unknown): string | null {
  if (typeof latencyMs !== "number" || !Number.isFinite(latencyMs)) return null;
  if (latencyMs < 1000) return `${latencyMs.toFixed(0)} ms`;
  return `${(latencyMs / 1000).toFixed(1)} s`;
}

type AuthOutput = Record<string, unknown>;

function authOutput(output: Output | undefined): AuthOutput | null {
  const auth = output?.auth;
  return auth && typeof auth === "object" ? (auth as AuthOutput) : null;
}

type BadgeTone = "ok" | "bad" | "neutral";

function verdictTone(verdict: unknown): BadgeTone {
  if (verdict === "pass") return "ok";
  if (verdict === "fail" || verdict === "softfail" || verdict === "permerror") return "bad";
  return "neutral";
}

function VerdictBadge({ testId, label, value, tone }: {
  testId: string;
  label: string;
  value: string;
  tone: BadgeTone;
}) {
  const toneClass =
    tone === "ok"
      ? "border-green-600/40 text-green-700 dark:text-green-400"
      : tone === "bad"
        ? "border-destructive/50 text-destructive"
        : "border-border text-muted-foreground";
  return (
    <span
      data-testid={testId}
      className={`inline-flex items-center gap-1 rounded-md border px-2 py-0.5 text-xs ${toneClass}`}
    >
      <span className="text-muted-foreground">{label}</span>
      <span className="font-mono">{value}</span>
    </span>
  );
}

// SenderAuthCard shows the sender-authentication verdicts the email check
// recorded (spec 2026-11-12-03). Rendered only when the check enabled any option.
function SenderAuthCard({ auth }: { auth: AuthOutput }) {
  const { t } = useTranslation("checks");
  const str = (v: unknown) => (typeof v === "string" ? v : "");
  const listed = Array.isArray(auth.blocklisted_on) ? (auth.blocklisted_on as string[]) : null;
  const provider =
    auth.provider_auth && typeof auth.provider_auth === "object"
      ? (auth.provider_auth as Record<string, string>)
      : null;
  return (
    <Card data-testid="email-auth-card">
      <CardHeader>
        <CardTitle className="text-base">{t("misc.emailAuthSectionTitle")}</CardTitle>
      </CardHeader>
      <CardContent className="space-y-3 text-sm">
        {str(auth.sending_ip) && (
          <div>
            <span className="text-muted-foreground">{t("misc.emailAuthSendingIp")} </span>
            <code className="font-mono" data-testid="email-auth-sending-ip">{str(auth.sending_ip)}</code>
          </div>
        )}
        <div className="flex flex-wrap gap-2">
          {str(auth.spf) && (
            <VerdictBadge testId="email-auth-spf-badge" label={t("misc.emailAuthSpf")}
              value={str(auth.spf)} tone={verdictTone(auth.spf)} />
          )}
          {str(auth.dkim) && (
            <VerdictBadge testId="email-auth-dkim-badge" label={t("misc.emailAuthDkim")}
              value={str(auth.dkim)} tone={auth.dkim === "pass" && auth.dkim_aligned ? "ok" : verdictTone(auth.dkim) === "bad" || auth.dkim === "pass" ? "bad" : "neutral"} />
          )}
          {str(auth.dmarc) && (
            <VerdictBadge testId="email-auth-dmarc-badge" label={t("misc.emailAuthDmarc")}
              value={str(auth.dmarc)} tone={verdictTone(auth.dmarc)} />
          )}
          {listed && (
            <VerdictBadge testId="email-auth-blocklist-badge" label={t("misc.emailAuthBlocklisted")}
              value={listed.length > 0 ? listed.join(", ") : t("misc.emailAuthNotListed")}
              tone={listed.length > 0 ? "bad" : "ok"} />
          )}
        </div>
        {provider && (
          <div className="text-xs text-muted-foreground" data-testid="email-auth-provider">
            {t("misc.emailAuthProvider")}:{" "}
            {Object.entries(provider).map(([k, v]) => `${k}=${v}`).join(" ")}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

export function EmailDeliveryCard({ org, output }: { org: string; output: Output | undefined }) {
  const auth = authOutput(output);
  return (
    <>
      <SendModeDeliveryCard org={org} output={output} />
      {auth && <SenderAuthCard auth={auth} />}
    </>
  );
}

function SendModeDeliveryCard({ org, output }: { org: string; output: Output | undefined }) {
  const { t } = useTranslation("checks");
  if (!isEmailDeliveryOutput(output)) return null;

  const sourceCheckUid = output.sourceCheckUid;
  const sentAt = typeof output.sentAt === "string" ? output.sentAt : undefined;
  const latency = formatLatency(output.latencyMs);

  return (
    <Card data-testid="email-delivery-card">
      <CardHeader>
        <CardTitle className="text-base">{t("mail.sendModeDelivery")}</CardTitle>
        <CardDescription>
          {t("mail.sendModeDeliveryDescription")}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-2 text-sm">
        {latency && (
          <div>
            <span className="text-muted-foreground">{t("mail.deliveryLatency")} </span>
            <code className="font-mono">{latency}</code>
          </div>
        )}
        {sentAt && (
          <div>
            <span className="text-muted-foreground">{t("mail.sentAt")} </span>
            <code className="font-mono">{new Date(sentAt).toLocaleString()}</code>
          </div>
        )}
        <Link
          to="/orgs/$org/checks/$checkUid"
          params={{ org, checkUid: sourceCheckUid }}
          className="inline-flex items-center gap-1 text-primary hover:underline"
          data-testid="email-delivery-source-link"
        >
          <Mail className="h-3.5 w-3.5" />
          {t("mail.viewSendingSmtpCheck")}
        </Link>
      </CardContent>
    </Card>
  );
}
