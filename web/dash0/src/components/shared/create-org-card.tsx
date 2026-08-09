import { useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { AlertCircle, Loader2 } from "lucide-react";
import { ApiError, setSession } from "@/api/client";
import { useCreateOrg } from "@/api/hooks";
import { useAuth } from "@/contexts/AuthContext";

// Org slugs are 3-20 chars: a lowercase letter/digit, 0-18 lowercase
// letters/digits/hyphens, a lowercase letter/digit — mirrors the server's
// `^[a-z0-9][a-z0-9-]{1,18}[a-z0-9]$` (ErrInvalidOrgSlug,
// server/internal/handlers/auth/service.go). Kept module-private here
// (distinct from the generic @/lib/utils slugify, which caps at 100 chars for
// other resources) since it encodes an org-specific limit.
function slugify(name: string): string {
  return name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-|-$/g, "")
    .slice(0, 20);
}

interface CreateOrgCardProps {
  /**
   * When provided, renders a Cancel button next to Create that calls this
   * instead of submitting — used by the account-section flow to go back to
   * the organizations list. /no-org has nowhere to go back to (the user has
   * zero orgs) and omits it, keeping its existing single-button layout.
   */
  onCancel?: () => void;
}

/**
 * The org-creation form, shared by /no-org (a zero-org user's only path
 * forward) and /orgs/$org/account/organizations/new (a user who already
 * belongs to at least one org). Session adoption below is the load-bearing
 * part and must exist in exactly one place — do not fork this component.
 */
export function CreateOrgCard({ onCancel }: CreateOrgCardProps) {
  const { t } = useTranslation(["auth", "common"]);
  const navigate = useNavigate();
  const createOrg = useCreateOrg();
  const { refreshUser } = useAuth();

  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [slugTouched, setSlugTouched] = useState(false);
  const [slugAdvancedOpen, setSlugAdvancedOpen] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const handleNameChange = (value: string) => {
    setName(value);
    if (!slugTouched) {
      setSlug(slugify(value));
    }
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    try {
      const result = await createOrg.mutateAsync({ name, slug });
      // POST /api/v1/orgs returns 201 with a session freshly scoped to the
      // NEW org. The caller's current token — whether it's a zero-org token
      // (/no-org) or a perfectly valid token for a DIFFERENT org (the
      // account-section flow) — is not scoped to it, so every org-scoped
      // call would 403 until this is adopted. That makes the account-section
      // failure mode subtle: unlike /no-org, the user already holds a valid
      // token, so skipping this looks fine until the first request against
      // the new org (spec 2026-08-09-05).
      setSession(result.accessToken, result.refreshToken, result.expiresIn);
      // Best-effort: sync AuthContext's user/organizations (sidebar switcher,
      // the account Organizations list) with the new org. Navigation
      // proceeds even if this fails — the new token is already good enough
      // for the dashboard.
      await refreshUser().catch(() => {});
      navigate({ to: "/orgs/$org", params: { org: result.slug } });
    } catch (err) {
      // 422 VALIDATION_ERROR / 409 CONFLICT already carry a usable message
      // from the server (handlers/auth/handler.go CreateOrg) — surface it
      // verbatim instead of a generic failure.
      setError(err instanceof ApiError ? err.message : t("auth:unexpectedError"));
    }
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-lg">{t("auth:createOrg.title")}</CardTitle>
        <p className="text-sm text-muted-foreground">
          {t("auth:createOrg.description")}
        </p>
      </CardHeader>
      <CardContent>
        {error && (
          <Alert variant="destructive" className="mb-4" data-testid="create-org-error">
            <AlertCircle className="h-4 w-4" />
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        )}

        <form onSubmit={handleSubmit} className="space-y-3">
          <div className="space-y-1.5">
            <Label htmlFor="orgName">{t("auth:createOrg.orgName")}</Label>
            <Input
              id="orgName"
              value={name}
              onChange={(e) => handleNameChange(e.target.value)}
              required
              disabled={createOrg.isPending}
              placeholder={t("auth:createOrg.orgNamePlaceholder")}
            />
            {slug && !slugAdvancedOpen && (
              <p className="text-xs text-muted-foreground">
                {t("auth:createOrg.slugPreview", { defaultValue: "Will be reachable as " })}
                <code className="font-mono">{slug}</code>
              </p>
            )}
          </div>
          {!slugAdvancedOpen ? (
            <button
              type="button"
              onClick={() => setSlugAdvancedOpen(true)}
              className="text-xs text-muted-foreground hover:underline"
              data-testid="no-org-advanced-toggle"
            >
              {t("auth:createOrg.advanced", { defaultValue: "Advanced — customize slug" })}
            </button>
          ) : (
            <div className="space-y-1.5">
              <Label htmlFor="orgSlug">{t("auth:createOrg.slug")}</Label>
              <Input
                id="orgSlug"
                value={slug}
                onChange={(e) => {
                  setSlug(e.target.value);
                  setSlugTouched(true);
                }}
                required
                pattern="[a-z0-9][a-z0-9-]{1,18}[a-z0-9]"
                title={t("auth:createOrg.slugTitle")}
                disabled={createOrg.isPending}
                placeholder={t("auth:createOrg.slugPlaceholder")}
              />
            </div>
          )}
          <div className="flex gap-2">
            <Button
              type="submit"
              className={onCancel ? "flex-1" : "w-full"}
              disabled={createOrg.isPending}
              data-testid="create-org-submit"
            >
              {createOrg.isPending ? (
                <>
                  <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                  {t("auth:createOrg.creating")}
                </>
              ) : (
                t("auth:createOrg.submit")
              )}
            </Button>
            {onCancel && (
              <Button
                type="button"
                variant="outline"
                onClick={onCancel}
                disabled={createOrg.isPending}
                data-testid="create-org-cancel"
              >
                {t("common:cancel")}
              </Button>
            )}
          </div>
        </form>
      </CardContent>
    </Card>
  );
}
