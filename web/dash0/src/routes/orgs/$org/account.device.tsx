import { useState } from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import {
  AlertCircle,
  CheckCircle2,
  MonitorSmartphone,
  Terminal,
} from "lucide-react";
import { toast } from "sonner";

import {
  useDeviceConsent,
  useRespondToDeviceConsent,
} from "@/api/hooks";
import { useAuth } from "@/contexts/AuthContext";
import { PageHeader } from "@/components/shared/page-header";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";

/**
 * Consent page for the OAuth 2.0 Device Authorization Grant (RFC 8628,
 * spec 2026-08-08-02) — the browser half of `sp auth login`.
 *
 * The CLI prints a short code and the org-less `/dash0/device` URL. That route
 * sends a logged-out visitor to /login with ITSELF as `returnTo` (org-less, so
 * the code survives the round trip for any org slug — see
 * `isDeviceVerificationReturnTo`), and forwards an authenticated one here, into
 * the `/orgs/$org` layout. The code arrives in `?user_code=` when the CLI's
 * `verification_uri_complete` was followed, and is typed by hand otherwise.
 *
 * Approving mints a Personal Access Token scoped to the organization SELECTED
 * here — not implicitly to the current session's org — so a member of several
 * orgs picks which one the CLI ends up talking to.
 *
 * The whole page is single-column and touch-sized: approving from a phone,
 * while the CLI runs on a headless box, is the entire point of the flow.
 */

type DeviceConsentSearch = {
  /** RFC 8628 names this parameter `user_code`; kept verbatim (see above). */
  user_code?: string;
};

export const Route = createFileRoute("/orgs/$org/account/device")({
  validateSearch: (search: Record<string, unknown>): DeviceConsentSearch => ({
    user_code:
      typeof search.user_code === "string" ? search.user_code : undefined,
  }),
  component: DeviceConsentPage,
});

/** A complete user code is 8 alphanumeric characters (RFC 8628 short code). */
function isCompleteUserCode(raw: string): boolean {
  return raw.replace(/[^A-Za-z0-9]/g, "").length === 8;
}

/** Uppercases and re-inserts the display dash: "wdjp4kxr" -> "WDJP-4KXR". */
function formatUserCode(raw: string): string {
  const canonical = raw.toUpperCase().replace(/[^A-Z0-9]/g, "");
  if (canonical.length <= 4) return canonical;
  return `${canonical.slice(0, 4)}-${canonical.slice(4, 8)}`;
}

function DeviceConsentPage() {
  const { org } = Route.useParams();
  const search = Route.useSearch();
  const navigate = useNavigate();
  const { organizations } = useAuth();

  // The URL is the source of truth for the code, so both a plain refresh and
  // the /device -> /login -> /device bounce restore it. `code` is only the
  // input draft; it re-syncs whenever the URL changes, using React's
  // adjust-state-during-render pattern rather than an effect.
  const urlCode = formatUserCode(search.user_code ?? "");
  const [code, setCode] = useState(urlCode);
  const [lastUrlCode, setLastUrlCode] = useState(urlCode);

  if (urlCode !== lastUrlCode) {
    setLastUrlCode(urlCode);
    setCode(urlCode);
  }

  // Only a complete code is looked up — no half-typed lookups, which would
  // burn the consent endpoint's (deliberately tight) rate limit.
  const submitted = isCompleteUserCode(urlCode) ? urlCode : "";

  const [selectedOrg, setSelectedOrg] = useState(org);
  const [decision, setDecision] = useState<"approved" | "denied" | null>(null);

  const consent = useDeviceConsent(submitted);
  const respond = useRespondToDeviceConsent();

  const orgOptions = organizations.length > 0
    ? organizations
    : [{ slug: org, name: org, role: "" }];
  const isSingleOrg = orgOptions.length === 1;

  function lookUp() {
    void navigate({
      to: ".",
      search: { user_code: formatUserCode(code) },
      replace: true,
    });
  }

  function decide(approve: boolean) {
    respond.mutate(
      { userCode: submitted, org: selectedOrg, approve },
      {
        onSuccess: () => {
          setDecision(approve ? "approved" : "denied");
          toast.success(
            approve
              ? "Device approved — return to your terminal"
              : "Device login denied",
          );
        },
        onError: () => {
          toast.error("Could not record your decision");
        },
      },
    );
  }

  return (
    <div className="mx-auto flex w-full max-w-lg flex-col gap-4">
      <PageHeader
        icon={MonitorSmartphone}
        title="Connect a device"
        description="Approve a sign-in request from the SolidPing command line."
      />

      {decision === null && (
        <Card>
          <CardHeader>
            <CardTitle className="text-base">Enter your one-time code</CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
            <div className="flex flex-col gap-2">
              <Label htmlFor="device-user-code">Code</Label>
              <Input
                id="device-user-code"
                data-testid="device-user-code"
                autoFocus
                autoComplete="off"
                autoCapitalize="characters"
                spellCheck={false}
                placeholder="XXXX-XXXX"
                className="font-mono text-lg tracking-widest"
                value={code}
                onChange={(event) => setCode(formatUserCode(event.target.value))}
                onKeyDown={(event) => {
                  if (event.key === "Enter") lookUp();
                }}
              />
            </div>
            <Button
              type="button"
              data-testid="device-lookup"
              className="w-full"
              disabled={!isCompleteUserCode(code)}
              onClick={lookUp}
            >
              Continue
            </Button>
          </CardContent>
        </Card>
      )}

      {decision === null && submitted !== "" && consent.isLoading && (
        <Card>
          <CardContent className="flex flex-col gap-3 pt-6">
            <Skeleton className="h-5 w-2/3" />
            <Skeleton className="h-5 w-1/2" />
          </CardContent>
        </Card>
      )}

      {decision === null && submitted !== "" && consent.isError && (
        <Alert variant="destructive" data-testid="device-not-found">
          <AlertCircle className="h-4 w-4" />
          <AlertTitle>We couldn't find that code</AlertTitle>
          <AlertDescription>
            It may have expired, already been used, or been mistyped. Run{" "}
            <code className="font-mono">sp auth login</code> again to get a fresh
            code.
          </AlertDescription>
        </Alert>
      )}

      {decision === null && consent.data && consent.data.status !== "pending" && (
        <Alert data-testid="device-already-decided">
          <AlertCircle className="h-4 w-4" />
          <AlertTitle>This request was already handled</AlertTitle>
          <AlertDescription>
            Nothing left to do here — start a new login from your terminal if you
            still need access.
          </AlertDescription>
        </Alert>
      )}

      {decision === null && consent.data?.status === "pending" && (
        <Card data-testid="device-consent-card">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <Terminal className="h-4 w-4" />
              Authorize this device
            </CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
            <p className="text-sm text-muted-foreground">
              <span
                className="font-medium text-foreground"
                data-testid="device-client-name"
              >
                {consent.data.clientName}
              </span>{" "}
              is asking to sign in as you. Approving creates a personal access
              token for it, valid for 90 days, which you can revoke any time from
              Account &rarr; Tokens.
            </p>

            <div className="flex flex-col gap-2">
              <Label htmlFor="device-org">Organization</Label>
              {isSingleOrg ? (
                <p
                  className="text-sm font-medium"
                  data-testid="device-org-readonly"
                >
                  {orgOptions[0].name || orgOptions[0].slug}
                </p>
              ) : (
                <Select value={selectedOrg} onValueChange={setSelectedOrg}>
                  <SelectTrigger id="device-org" data-testid="device-org-select">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {orgOptions.map((option) => (
                      <SelectItem key={option.slug} value={option.slug}>
                        {option.name || option.slug}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
              <p className="text-xs text-muted-foreground">
                The token will only be able to reach this organization.
              </p>
            </div>

            <p className="text-xs text-muted-foreground">
              Expires {new Date(consent.data.expiresAt).toLocaleTimeString()}
            </p>

            <div className="flex flex-col gap-2 sm:flex-row sm:justify-end">
              <Button
                type="button"
                variant="outline"
                className="w-full sm:w-auto"
                data-testid="device-deny"
                disabled={respond.isPending}
                onClick={() => decide(false)}
              >
                Deny
              </Button>
              <Button
                type="button"
                className="w-full sm:w-auto"
                data-testid="device-approve"
                disabled={respond.isPending}
                onClick={() => decide(true)}
              >
                Approve
              </Button>
            </div>
          </CardContent>
        </Card>
      )}

      {decision === "approved" && (
        <Alert data-testid="device-approved">
          <CheckCircle2 className="h-4 w-4" />
          <AlertTitle>Device approved</AlertTitle>
          <AlertDescription>
            You can close this page and go back to your terminal — the command
            line will pick the login up within a few seconds.
          </AlertDescription>
        </Alert>
      )}

      {decision === "denied" && (
        <Alert variant="destructive" data-testid="device-denied">
          <AlertCircle className="h-4 w-4" />
          <AlertTitle>Device login denied</AlertTitle>
          <AlertDescription>
            No token was created. The command line will report that the request
            was denied.
          </AlertDescription>
        </Alert>
      )}
    </div>
  );
}
