import { useTranslation } from "react-i18next";
import { ShieldAlert, ShieldCheck, HelpCircle, Info } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";

type Output = Record<string, unknown>;

type TFunc = ReturnType<typeof useTranslation>["t"];

function asStringArray(value: unknown): string[] {
  if (!Array.isArray(value)) return [];
  return value.filter((v): v is string => typeof v === "string");
}

// A zone -> codes map (backend error_codes / return_codes). Values are the raw
// DNS reply octets (e.g. "127.0.0.2", "127.255.255.254").
function asCodeMap(value: unknown): Record<string, string[]> {
  if (!value || typeof value !== "object" || Array.isArray(value)) return {};
  const out: Record<string, string[]> = {};
  for (const [zone, codes] of Object.entries(value as Record<string, unknown>)) {
    const list = asStringArray(codes);
    if (list.length > 0) out[zone] = list;
  }
  return out;
}

// A DNSBL result always carries listed_on/clean/inconclusive as arrays. Requiring
// all three keeps the card from mounting on any non-DNSBL check output, so it is
// safe to render unconditionally (e.g. on the generic result-detail page).
function isDnsblOutput(output: Output | undefined): boolean {
  if (!output) return false;
  return (
    Array.isArray(output.listed_on) &&
    Array.isArray(output.clean) &&
    Array.isArray(output.inconclusive)
  );
}

// codeLabel turns a reserved 127.255.255.x DNSBL status octet into plain
// language. Everything else (including unknown reserved codes) falls back to the
// generic label so an operator never sees a bare, meaningless IP.
function codeLabel(t: TFunc, code: string): string {
  switch (code) {
    case "127.255.255.252":
      return t("checks:dnsbl.errTypo", "Typo / malformed DNSBL zone name");
    case "127.255.255.254":
      return t(
        "checks:dnsbl.errPublicResolver",
        "Query via public/open resolver — refused",
      );
    case "127.255.255.255":
      return t("checks:dnsbl.errRateLimit", "Query volume / rate limit exceeded");
    default:
      return t("checks:dnsbl.errGeneric", "DNSBL error response ({{code}})", {
        code,
      });
  }
}

// The output keys DnsblCard renders itself. Callers that also dump the raw output
// (e.g. the result-detail page) strip these so nothing is shown twice.
export const DNSBL_OUTPUT_KEYS = [
  "listed_on",
  "clean",
  "inconclusive",
  "error_codes",
  "return_codes",
] as const;

// DnsblCard renders a DNSBL check result: which blocklist zones the target is
// listed on (with the raw 127.0.0.x sub-list code operators want verbatim), which
// are clean, which are inconclusive, and — the primary ask — the reserved
// 127.255.255.x status codes as human-readable labels instead of bare octets.
// Returns null when the output is not a DNSBL result, so it is safe to mount
// unconditionally.
export function DnsblCard({ output }: { output: Output | undefined }) {
  const { t } = useTranslation(["checks", "common"]);

  if (!isDnsblOutput(output)) return null;

  const listedOn = asStringArray(output?.listed_on);
  const clean = asStringArray(output?.clean);
  const inconclusive = asStringArray(output?.inconclusive);
  const errorCodes = asCodeMap(output?.error_codes);
  const returnCodes = asCodeMap(output?.return_codes);

  const errorEntries = Object.entries(errorCodes);

  return (
    <Card data-testid="dnsbl-card">
      <CardHeader>
        <div className="flex flex-wrap items-center gap-2">
          <CardTitle>{t("checks:dnsbl.title", "Blocklist status")}</CardTitle>
          {listedOn.length > 0 ? (
            <Badge variant="destructive" className="gap-1">
              <ShieldAlert className="h-3 w-3" />
              {t("checks:dnsbl.listedOn", "Listed on")}
            </Badge>
          ) : (
            <Badge variant="success" className="gap-1">
              <ShieldCheck className="h-3 w-3" />
              {t("checks:dnsbl.clean", "Clean")}
            </Badge>
          )}
        </div>
        <CardDescription>
          {t(
            "checks:dnsbl.description",
            "How the target resolved against each DNS blocklist zone.",
          )}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        {listedOn.length > 0 && (
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-sm text-muted-foreground">
              {t("checks:dnsbl.listedOn", "Listed on")}
            </span>
            {listedOn.map((zone) => {
              const codes = returnCodes[zone];
              return (
                <Badge
                  key={zone}
                  variant="destructive"
                  data-testid={`dnsbl-listed-${zone}`}
                >
                  {zone}
                  {codes && codes.length > 0 ? ` (${codes.join(", ")})` : ""}
                </Badge>
              );
            })}
          </div>
        )}

        {clean.length > 0 && (
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-sm text-muted-foreground">
              {t("checks:dnsbl.clean", "Clean")}
            </span>
            {clean.map((zone) => (
              <Badge key={zone} variant="success" data-testid={`dnsbl-clean-${zone}`}>
                {zone}
              </Badge>
            ))}
          </div>
        )}

        {inconclusive.length > 0 && (
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-sm text-muted-foreground">
              {t("checks:dnsbl.inconclusive", "Inconclusive")}
            </span>
            {inconclusive.map((zone) => (
              <Badge
                key={zone}
                variant="secondary"
                data-testid={`dnsbl-inconclusive-${zone}`}
              >
                {zone}
              </Badge>
            ))}
          </div>
        )}

        {errorEntries.length > 0 && (
          <div className="space-y-2" data-testid="dnsbl-error-codes">
            <div className="flex items-center gap-1.5 text-sm font-medium text-muted-foreground">
              <Info className="h-3.5 w-3.5" />
              {t("checks:dnsbl.errorCodes", "Diagnostic responses")}
            </div>
            <ul className="space-y-1 text-sm">
              {errorEntries.map(([zone, codes]) => (
                <li key={zone} className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
                  <span className="font-mono text-xs text-muted-foreground">{zone}</span>
                  <span className="flex flex-wrap items-center gap-1.5">
                    {codes.map((code) => (
                      <span key={code} className="inline-flex items-center gap-1">
                        <HelpCircle className="h-3 w-3 text-muted-foreground" />
                        {codeLabel(t, code)}
                      </span>
                    ))}
                  </span>
                </li>
              ))}
            </ul>
          </div>
        )}
      </CardContent>
    </Card>
  );
}
