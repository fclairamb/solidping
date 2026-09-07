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
import { orgSlugify } from "@/lib/org-slug";

interface CreateOrgCardProps {
  /**
   * When provided, renders a Cancel button next to Create that calls this
   * instead of submitting — used by the account-section flow to go back to
   * the organizations list. /no-org has nowhere to go back to (the user has
   * zero orgs) and omits it, keeping its existing single-button layout.
   */
  onCancel?: () => void;
  /**
   * A ready-to-submit organization name to pre-fill the form with, so a
   * brand-new account can create its org without inventing anything (spec
   * 2026-09-05-01). Only /no-org passes one — the account-section flow, where
   * the user already has an org and is deliberately adding another, keeps an
   * empty form. It is a DEFAULT, not a lock: the field stays editable.
   */
  suggestedName?: string;
  /**
   * The org-slug BASE that goes with `suggestedName` — derived from the
   * user's first name, not the (localized, boilerplate-prefixed) possessive
   * sentence `suggestedName` renders (spec 2026-09-07-01). Shown in the "will
   * be reachable as …" preview and sent to the server as `slugBase` when the
   * user submits without touching the slug field themselves. Once the user
   * types into either the name or the slug field, this proposal is gone for
   * good — see the `slug` derivation below.
   */
  suggestedSlug?: string;
}

/**
 * The org-creation form, shared by /no-org (a zero-org user's only path
 * forward) and /orgs/$org/account/organizations/new (a user who already
 * belongs to at least one org). Session adoption below is the load-bearing
 * part and must exist in exactly one place — do not fork this component.
 */
export function CreateOrgCard({
  onCancel,
  suggestedName,
  suggestedSlug,
}: CreateOrgCardProps) {
  const { t } = useTranslation(["auth", "common"]);
  const navigate = useNavigate();
  const createOrg = useCreateOrg();
  const { refreshUser } = useAuth();

  // `typedName` / `typedSlug` hold ONLY what the user typed; the rendered
  // values are derived below. That matters because the proposal is not known at
  // mount — /no-org builds it from useAuth()'s user and GET /auth/me resolves
  // after the first render — so seeding it as initial state would leave a named
  // user looking at the random fallback forever, and copying it in with an
  // effect would be a cascading-render sync. Deriving needs neither: the field
  // shows the proposal until the moment somebody types, and never again after.
  const [typedName, setTypedName] = useState("");
  const [typedSlug, setTypedSlug] = useState("");
  const [nameTouched, setNameTouched] = useState(false);
  const [slugTouched, setSlugTouched] = useState(false);
  const [slugAdvancedOpen, setSlugAdvancedOpen] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const name = nameTouched ? typedName : (suggestedName ?? "");
  // Precedence: what the user typed into the slug field; else, once they
  // typed into the NAME field, orgSlugify of that (the proposal — and its
  // dedicated slug base — is gone the moment the name changes); else the
  // proposed slug base, which is NOT orgSlugify(suggestedName) — it comes
  // from the first name alone (spec 2026-09-07-01). `suggestedSlug` is
  // undefined for the account-section caller, which never proposes anything.
  const slug = slugTouched
    ? typedSlug
    : nameTouched
      ? orgSlugify(name)
      : (suggestedSlug ?? orgSlugify(name));

  const handleNameChange = (value: string) => {
    setNameTouched(true);
    setTypedName(value);
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    try {
      // `slug` (strict: 422 invalid, 409 taken) travels ONLY when the user
      // opened Advanced and typed one themselves. Otherwise the rendered
      // preview travels as `slugBase` — a HINT the server normalizes and
      // suffixes on collision via orgslug.GenerateUnique, never answering 422
      // or 409 for it. That is what lets a newcomer who accepts the proposal
      // as-is never meet a 409 they cannot act on, whether the proposal came
      // from `suggestedSlug` or (once they typed a name) from orgSlugify(name)
      // — the same value the server would have derived from the name anyway,
      // so the preview stays honest either way.
      const result = await createOrg.mutateAsync({
        name,
        slug: slugTouched ? slug : undefined,
        slugBase: slugTouched ? undefined : slug || undefined,
      });
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
      setError(
        err instanceof ApiError ? err.message : t("auth:unexpectedError"),
      );
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
          <Alert
            variant="destructive"
            className="mb-4"
            data-testid="create-org-error"
          >
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
                {t("auth:createOrg.slugPreview", {
                  defaultValue: "Will be reachable as ",
                })}
                <code className="font-mono" data-testid="create-org-slug-preview">
                  {slug}
                </code>
              </p>
            )}
          </div>
          {!slugAdvancedOpen ? (
            <button
              type="button"
              onClick={() => {
                // Seed the editable field with exactly what the preview showed,
                // so opening Advanced never blanks the slug the user was
                // promised.
                setTypedSlug(slug);
                setSlugAdvancedOpen(true);
              }}
              className="text-xs text-muted-foreground hover:underline"
              data-testid="no-org-advanced-toggle"
            >
              {t("auth:createOrg.advanced", {
                defaultValue: "Advanced — customize slug",
              })}
            </button>
          ) : (
            <div className="space-y-1.5">
              <Label htmlFor="orgSlug">{t("auth:createOrg.slug")}</Label>
              <Input
                id="orgSlug"
                value={slug}
                onChange={(e) => {
                  setSlugTouched(true);
                  setTypedSlug(e.target.value);
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
