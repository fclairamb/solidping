// EmailDeliveryCard renders the send-mode SMTP enrichment on an email check's
// result (spec 2026-08-19-04): when the receiving JMAP handler found the
// X-SolidPing-Check / X-SolidPing-Sent-At headers, the result output carries
// sourceCheckUid/sentAt/latencyMs. This card shows the latency and links back
// to the sending SMTP check. Renders nothing when those fields are absent —
// the overwhelming majority of email-check results (human/heartbeat-style
// uses, or a header stripped by an intermediate MTA), so it is safe to mount
// unconditionally next to DnsblCard on the result-detail page.
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
export const EMAIL_DELIVERY_OUTPUT_KEYS = ["sourceCheckUid", "sentAt", "latencyMs"] as const;

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

export function EmailDeliveryCard({ org, output }: { org: string; output: Output | undefined }) {
  if (!isEmailDeliveryOutput(output)) return null;

  const sourceCheckUid = output.sourceCheckUid;
  const sentAt = typeof output.sentAt === "string" ? output.sentAt : undefined;
  const latency = formatLatency(output.latencyMs);

  return (
    <Card data-testid="email-delivery-card">
      <CardHeader>
        <CardTitle className="text-base">Send-mode delivery</CardTitle>
        <CardDescription>
          This email was attributed to a send-mode SMTP check&apos;s probe.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-2 text-sm">
        {latency && (
          <div>
            <span className="text-muted-foreground">Delivery latency: </span>
            <code className="font-mono">{latency}</code>
          </div>
        )}
        {sentAt && (
          <div>
            <span className="text-muted-foreground">Sent at: </span>
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
          View sending SMTP check
        </Link>
      </CardContent>
    </Card>
  );
}
