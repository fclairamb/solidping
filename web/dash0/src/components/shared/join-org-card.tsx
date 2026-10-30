import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { SecondaryDisclosureCard } from "@/components/ui/secondary-disclosure-card";
import { AlertCircle, Loader2 } from "lucide-react";
import { ApiError } from "@/api/client";
import {
  useCreateMembershipRequest,
  useMyMembershipRequests,
} from "@/api/hooks";

function JoinOrgForm({ joinSlug }: { joinSlug?: string }) {
  const { t } = useTranslation("auth");
  const createRequest = useCreateMembershipRequest();

  const [orgSlug, setOrgSlug] = useState(joinSlug ?? "");
  const [message, setMessage] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [success, setSuccess] = useState(false);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    try {
      await createRequest.mutateAsync({
        orgSlug,
        message: message || undefined,
      });
      setSuccess(true);
      setOrgSlug("");
      setMessage("");
    } catch (err) {
      setError(err instanceof ApiError ? err.message : t("unexpectedError"));
    }
  };

  return (
    <SecondaryDisclosureCard
      title={t("noOrg.joinTitle")}
      description={t("noOrg.joinDescription")}
      expandLabel={t("noOrg.joinExpand")}
      collapseLabel={t("noOrg.joinCollapse")}
      data-testid="no-org-join-toggle"
    >
      {success ? (
        <Alert>
          <AlertDescription>{t("noOrg.joinSent")}</AlertDescription>
        </Alert>
      ) : (
        <>
          {error && (
            <Alert variant="destructive" className="mb-4">
              <AlertCircle className="h-4 w-4" />
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}

          <form
            onSubmit={handleSubmit}
            className="space-y-3"
            data-testid="join-org-form"
          >
            <div className="space-y-1.5">
              <Label htmlFor="joinOrgSlug">{t("noOrg.joinSlugLabel")}</Label>
              <Input
                id="joinOrgSlug"
                data-testid="join-org-slug-input"
                value={orgSlug}
                onChange={(e) => setOrgSlug(e.target.value)}
                required
                pattern="[a-z0-9][a-z0-9-]{1,18}[a-z0-9]"
                disabled={createRequest.isPending}
                placeholder={t("noOrg.joinSlugPlaceholder")}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="joinMessage">{t("noOrg.joinMessageLabel")}</Label>
              <Textarea
                id="joinMessage"
                data-testid="join-org-message-input"
                value={message}
                onChange={(e) => setMessage(e.target.value)}
                disabled={createRequest.isPending}
                placeholder={t("noOrg.joinMessagePlaceholder")}
                rows={3}
              />
            </div>
            <Button
              type="submit"
              data-testid="join-org-submit-button"
              className="w-full"
              disabled={createRequest.isPending}
              variant="outline"
            >
              {createRequest.isPending ? (
                <>
                  <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                  {t("noOrg.joining")}
                </>
              ) : (
                t("noOrg.requestJoin")
              )}
            </Button>
          </form>
        </>
      )}
    </SecondaryDisclosureCard>
  );
}

interface JoinOrgCardProps {
  /** `?join=<slug>`: the org the user explicitly wants to join. */
  joinSlug?: string;
  /** `?membershipPending=<slug>`: a sign-in the org did not admit. */
  membershipPending?: string;
}

/**
 * "I'd rather join an existing organization", shown only to a user who came
 * through a join path: a `join` search param, a sign-in the org did not admit,
 * or a pending / rejected membership request. Everyone else only sees org
 * creation (spec 2026-10-30-04).
 */
export function JoinOrgCard({ joinSlug, membershipPending }: JoinOrgCardProps) {
  const { data } = useMyMembershipRequests();
  const hasRequest = (data?.data ?? []).some(
    (r) => r.status === "pending" || r.status === "rejected",
  );

  if (!joinSlug && !membershipPending && !hasRequest) return null;

  return <JoinOrgForm joinSlug={joinSlug} />;
}
