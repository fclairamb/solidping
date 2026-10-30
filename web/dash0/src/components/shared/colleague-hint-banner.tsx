import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Users, X } from "lucide-react";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { useAuth } from "@/contexts/AuthContext";
import { useColleagueHint, useCreateMembershipRequest } from "@/api/hooks";

interface ColleagueHintBannerProps {
  /** Slug of the org being viewed: the banner only shows on the personal org. */
  org: string;
}

/** localStorage key remembering a dismissed hint, per user and suggested org. */
export function colleagueHintDismissKey(userUid: string, orgSlug: string) {
  return `solidping:colleague-hint-dismissed:${userUid}:${orgSlug}`;
}

function isDismissed(key: string): boolean {
  try {
    return window.localStorage.getItem(key) === "1";
  } catch {
    return false;
  }
}

/**
 * ColleagueHintBanner (spec 2026-10-30-04): a user who got an auto-created
 * personal org, and whose company domain is already used by exactly one org
 * accepting join requests, is offered to ask to join it. Dismissable, and gone
 * once the request is sent. The server decides everything that could reveal
 * an organization; this only renders its answer.
 */
export function ColleagueHintBanner({ org }: ColleagueHintBannerProps) {
  const { t } = useTranslation(["org"]);
  const { user } = useAuth();
  const { data } = useColleagueHint(!!user && !user.isDemo);
  const createRequest = useCreateMembershipRequest();
  const [dismissed, setDismissed] = useState(false);
  const [sent, setSent] = useState(false);
  const [error, setError] = useState(false);

  const hint = data?.org;
  if (!user || !hint || data?.personalOrgSlug !== org) return null;

  const dismissKey = colleagueHintDismissKey(user.uid, hint.slug);
  if (dismissed || isDismissed(dismissKey)) return null;

  const dismiss = () => {
    try {
      window.localStorage.setItem(dismissKey, "1");
    } catch {
      // Private mode: the banner is hidden for this page view only.
    }
    setDismissed(true);
  };

  const request = async () => {
    setError(false);
    try {
      await createRequest.mutateAsync({ orgSlug: hint.slug });
      setSent(true);
    } catch {
      setError(true);
    }
  };

  const orgName = hint.name || hint.slug;

  return (
    <Alert className="mb-3" data-testid="colleague-hint-banner">
      <Users />
      <AlertTitle className="pr-8">{t("org:colleagueHint.title")}</AlertTitle>
      <AlertDescription className="space-y-2">
        {sent ? (
          <p data-testid="colleague-hint-sent">
            {t("org:colleagueHint.sent", { org: orgName })}
          </p>
        ) : (
          <>
            <p>
              {t("org:colleagueHint.description", {
                domain: data?.domain ?? "",
                org: orgName,
              })}
            </p>
            {error && (
              <p className="text-destructive" data-testid="colleague-hint-error">
                {t("org:colleagueHint.error")}
              </p>
            )}
            <div className="pt-1">
              <Button
                size="sm"
                onClick={request}
                disabled={createRequest.isPending}
                data-testid="colleague-hint-request"
              >
                {createRequest.isPending
                  ? t("org:colleagueHint.requesting")
                  : t("org:colleagueHint.request")}
              </Button>
            </div>
          </>
        )}
      </AlertDescription>
      <Button
        variant="ghost"
        size="icon"
        className="absolute right-2 top-2 h-7 w-7"
        onClick={dismiss}
        aria-label={t("org:colleagueHint.dismiss")}
        data-testid="colleague-hint-dismiss"
      >
        <X className="h-4 w-4" />
      </Button>
    </Alert>
  );
}
