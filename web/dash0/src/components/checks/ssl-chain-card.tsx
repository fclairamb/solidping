import { useTranslation } from "react-i18next";
import { ShieldAlert, ShieldCheck } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";

// One entry of the reported certificate chain (mirrors the backend output shape
// from checkssl: output.chain[]).
interface ChainCert {
  subject?: string;
  issuer?: string;
  notAfter?: string;
  daysRemaining?: number;
}

type Output = Record<string, unknown>;

function asChain(output: Output | undefined): ChainCert[] | null {
  const raw = output?.chain;
  if (!Array.isArray(raw)) return null;
  return raw as ChainCert[];
}

function formatDate(iso?: string): string {
  if (!iso) return "-";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleDateString();
}

// SslChainCard renders the SSL certificate chain reported by an SSL check's last
// result: a per-cert table plus an amber "expiring in N days" badge when the
// backend flagged certExpiryWarning. Only renders when the output carries a
// chain, so it is safe to mount unconditionally for SSL checks.
export function SslChainCard({ output }: { output: Output | undefined }) {
  const { t } = useTranslation(["checks", "common"]);
  const chain = asChain(output);

  if (!chain || chain.length === 0) return null;

  const warning = output?.certExpiryWarning === true;
  const critical = output?.severity === "critical";
  const chainVerified = output?.chainVerified === true;

  // The soonest-expiring days-remaining drives the badge label.
  const soonest = output?.soonestExpiring as
    | { daysRemaining?: number; subject?: string }
    | undefined;
  const minDays =
    typeof soonest?.daysRemaining === "number"
      ? soonest.daysRemaining
      : Math.min(
          ...chain.map((c) =>
            typeof c.daysRemaining === "number" ? c.daysRemaining : Infinity,
          ),
        );

  return (
    <Card data-testid="ssl-chain-card">
      <CardHeader>
        <div className="flex flex-wrap items-center gap-2">
          <CardTitle>{t("checks:ssl.chainTitle", "Certificate chain")}</CardTitle>
          {chainVerified ? (
            <Badge variant="success" className="gap-1">
              <ShieldCheck className="h-3 w-3" />
              {t("checks:ssl.chainVerified", "Verified")}
            </Badge>
          ) : (
            <Badge variant="secondary" className="gap-1">
              <ShieldAlert className="h-3 w-3" />
              {t("checks:ssl.chainUnverified", "Unverified")}
            </Badge>
          )}
          {warning && (
            <Badge variant="warning" className="gap-1" data-testid="ssl-expiry-warning-badge">
              <ShieldAlert className="h-3 w-3" />
              {t("checks:ssl.expiringInDays", "Expiring in {{count}} days", {
                count: minDays,
              })}
            </Badge>
          )}
          {critical && (
            <Badge variant="destructive" className="gap-1" data-testid="ssl-expiry-critical-badge">
              <ShieldAlert className="h-3 w-3" />
              {t("checks:ssl.expiredInDays", "Expires in {{count}} days", {
                count: minDays,
              })}
            </Badge>
          )}
        </div>
        <CardDescription>
          {t(
            "checks:ssl.chainDescription",
            "Every presented certificate. The status is driven by the soonest to expire.",
          )}
        </CardDescription>
      </CardHeader>
      <CardContent>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t("checks:ssl.subject", "Subject")}</TableHead>
              <TableHead>{t("checks:ssl.issuer", "Issuer")}</TableHead>
              <TableHead>{t("checks:ssl.notAfter", "Expires")}</TableHead>
              <TableHead className="text-right">
                {t("checks:ssl.daysRemaining", "Days left")}
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {chain.map((cert, i) => {
              const isSoonest = cert.daysRemaining === minDays;
              return (
                <TableRow
                  key={`${cert.subject ?? "cert"}-${i}`}
                  className={isSoonest && (warning || critical) ? "bg-yellow-500/10" : ""}
                >
                  <TableCell className="font-medium">{cert.subject || "-"}</TableCell>
                  <TableCell className="text-muted-foreground">{cert.issuer || "-"}</TableCell>
                  <TableCell className="font-mono text-sm">{formatDate(cert.notAfter)}</TableCell>
                  <TableCell className="text-right font-mono">
                    {typeof cert.daysRemaining === "number" ? cert.daysRemaining : "-"}
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      </CardContent>
    </Card>
  );
}
