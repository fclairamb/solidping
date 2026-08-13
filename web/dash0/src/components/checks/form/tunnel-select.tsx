// The shared "Run through SSH tunnel" selector.
//
// Unlike the per-type `Fields` modules, this is protocol-agnostic: it edits the
// well-known `tunnelCheckUid` config key, exactly like the shared per-check
// `timeout` input, and is layered onto the config by check-form rather than by a
// type module. Which types may show it is server-declared capability metadata
// (`CheckTypeInfo.supportsTunnel`), never a hard-coded list here — the backend
// enables http + tcp today and more checkers later, with no frontend change.
import { Link } from "@tanstack/react-router";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import type { Check } from "@/api/hooks";
import { checkLabel } from "@/components/checks/tunnel";

// TUNNEL_NONE_VALUE is the sentinel for "no tunnel". Radix's Select cannot hold
// an empty-string item value, so "None" carries this and is mapped back to ""
// (i.e. the key is omitted from the submitted config, detaching the tunnel).
const TUNNEL_NONE_VALUE = "__none__";

export interface TunnelSelectProps {
  org: string;
  /** The org's SSH checks — candidates to tunnel through. */
  sshChecks: Check[];
  /** The regions the check being edited runs in (its resolved region slugs). */
  selectedRegions: string[];
  value: string;
  onChange: (tunnelCheckUid: string) => void;
}

// hasFingerprint reports whether an SSH check verifies its host key. Only those
// may be used as a tunnel: the tunnel carries the probe's traffic, so an
// unverified bastion is a silent MITM. The server enforces this — the UI just
// explains it up front instead of letting the user hit a 400.
function hasFingerprint(check: Check): boolean {
  const fingerprint = check.config?.["expected_fingerprint"];
  return typeof fingerprint === "string" && fingerprint.length > 0;
}

// privateRegionLabel renders a private region the way the server's own
// validation messages do: `@paris`. Private regions are stored org-relatively
// since spec 2026-08-13-01, so this is usually the identity — the slash case
// only fires on a legacy `@org/paris` row a client is still holding.
function privateRegionLabel(region: string): string {
  const slash = region.lastIndexOf("/");
  return slash >= 0 ? `@${region.slice(slash + 1)}` : region;
}

// regionIncompatibility mirrors the server's decision-1 rule: every PRIVATE
// region the check runs in must be one the SSH check is itself allocated to
// (that is what makes its credentials already sealed to that region's agents).
// Returns the first uncovered private region's label, or null when compatible.
// The sealed-only-for-cloud rule (decision 2) is left to the server — the UI
// cannot see whether an SSH check's secrets are sealed-only — and surfaces as an
// inline error on submit.
function regionIncompatibility(
  check: Check,
  selectedRegions: string[],
): string | null {
  const sshRegions = new Set(check.regions ?? []);
  for (const region of selectedRegions) {
    if (region.startsWith("@") && !sshRegions.has(region)) {
      return privateRegionLabel(region);
    }
  }
  return null;
}

export function TunnelSelect({
  org,
  sshChecks,
  selectedRegions,
  value,
  onChange,
}: TunnelSelectProps) {
  return (
    <div className="space-y-2">
      <Label htmlFor="check-tunnel">Run through SSH tunnel (optional)</Label>
      {sshChecks.length === 0 ? (
        <Alert>
          <AlertDescription>
            No SSH checks yet.{" "}
            <Link
              to="/orgs/$org/checks/new"
              params={{ org }}
              search={{
                checkType: "ssh",
                checkPeriod: undefined,
                checkName: undefined,
                checkSlug: undefined,
                httpUrl: undefined,
                httpMethod: undefined,
                host: undefined,
                port: undefined,
                url: undefined,
                domain: undefined,
                username: undefined,
                database: undefined,
                expectedStatus: undefined,
                timeout: undefined,
                label: undefined,
                region: undefined,
                group: undefined,
                confirmationPeriod: undefined,
                recoveryPeriod: undefined,
                section: undefined,
              }}
              // A full document reload, not a client-side transition: the
              // new-check form only reads its `?checkType=` search param on
              // mount (so a same-tab type change made via its own type
              // picker doesn't wipe other in-progress fields), so a
              // client-side nav to the same route would leave the type
              // picker showing whatever was already selected instead of SSH.
              reloadDocument
              className="underline"
              data-testid="tunnel-empty-create-link"
            >
              Create an SSH check for your bastion
            </Link>{" "}
            — this opens a new form, so any unsaved changes here are lost.
          </AlertDescription>
        </Alert>
      ) : (
        <Select
          value={value === "" ? TUNNEL_NONE_VALUE : value}
          onValueChange={(next) =>
            onChange(next === TUNNEL_NONE_VALUE ? "" : next)
          }
        >
          <SelectTrigger id="check-tunnel" data-testid="check-tunnel-select">
            <SelectValue placeholder="None (direct connection)" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={TUNNEL_NONE_VALUE}>
              None (direct connection)
            </SelectItem>
            {sshChecks.map((check) => {
              const verified = hasFingerprint(check);
              const uncoveredRegion = regionIncompatibility(
                check,
                selectedRegions,
              );
              // A fingerprint gap and a region gap are both blocking; report the
              // fingerprint first (it is the harder prerequisite).
              const disabledReason = !verified
                ? "needs a host key fingerprint"
                : uncoveredRegion
                  ? `not in region ${uncoveredRegion}`
                  : null;
              return (
                <SelectItem
                  key={check.uid}
                  value={check.uid}
                  disabled={disabledReason !== null}
                >
                  {checkLabel(check)}
                  {disabledReason && ` — ${disabledReason}`}
                </SelectItem>
              );
            })}
          </SelectContent>
        </Select>
      )}
      <p className="text-xs text-muted-foreground">
        Dial this check&apos;s target through an SSH check&apos;s connection, to
        reach services behind a bastion. The hostname is resolved by the bastion,
        so private names work. Tunnel setup time is reported separately as
        <code className="mx-1">tunnel_setup_ms</code>and excluded from the
        check&apos;s response time.
      </p>
    </div>
  );
}
