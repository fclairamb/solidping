import { useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "@tanstack/react-router";
import { toast } from "sonner";
import { AlertCircle, Building2, Loader2, Trash2, Upload } from "lucide-react";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ApiError } from "@/api/client";
import {
  useClearOrgLogo,
  useUpdateOrgProfile,
  useUploadOrgLogo,
  type OrgProfileResponse,
} from "@/api/hooks";
import { useAuth } from "@/contexts/AuthContext";

// Mirrors the server allowlist (handlers/orglogo) so the file picker never
// offers something the upload would reject.
const LOGO_ACCEPT = "image/png,image/jpeg,image/webp,image/gif,image/svg+xml";

// The server returns an uploaded logo as a relative /pub/assets/<uid> path
// (never absolute) and refuses anything else that isn't an absolute http(s)
// URL (normalizeLogoURL, org_profile.go) — so "not http" reliably means
// "currently an uploaded file", never a URL the URL field could hold.
const isUploadedLogoPath = (url: string) => url !== "" && !url.startsWith("http");

interface OrgProfileCardProps {
  org: string;
}

/**
 * Owner-only card for the organization's own identity: name, URL slug and
 * logo. The server enforces the same gate (RequireOrgOwner) — hiding the card
 * from an admin is UX, not security.
 *
 * The slug is never derived from the name here: editing the name leaves the
 * slug untouched, so moving the organization's URLs is always something the
 * user typed on purpose.
 *
 * Renaming the slug moves every URL of the organization. It is not
 * destructive any more: the previous slug keeps redirecting to the new one —
 * but only until another organization claims it, which is what the warning
 * says. On success the re-minted session is adopted before navigating, because
 * the current access token is scoped to the old slug and would 403.
 */
export function OrgProfileCard({ org }: OrgProfileCardProps) {
  const { t } = useTranslation("org");
  const { t: tc } = useTranslation("common");
  const navigate = useNavigate();
  const { organizations, adoptRenamedOrgSession, refreshUser } = useAuth();

  const current = organizations.find((entry) => entry.slug === org);

  const [name, setName] = useState("");
  const [slug, setSlug] = useState(org);
  // True once the user has typed into name/slug since the last sync/save —
  // guards the render-time reseed below from clobbering an in-progress edit
  // when a save's background /auth/me refresh (AuthContext.refreshUser)
  // lands late and changes `current` out from under the user (spec
  // 2026-08-28-13 audit: reproduced by editing the slug immediately after
  // saving the name).
  const [nameTouched, setNameTouched] = useState(false);
  const [slugTouched, setSlugTouched] = useState(false);
  // `logoUrl` is the authoritative current value (as returned by the server —
  // either an absolute http(s) URL, a relative /pub/assets/<uid> upload path,
  // or ""). It drives the preview thumbnail and the clear button, and is
  // never fed into the type="url" input directly (see isUploadedLogoPath).
  const [logoUrl, setLogoUrl] = useState("");
  // The URL input's own text, tracked separately so an uploaded path never
  // lands in a type="url" field and trips native constraint validation.
  const [logoUrlDraft, setLogoUrlDraft] = useState("");
  // True once the user has typed into the URL field since the last sync/save
  // — gates whether logoUrl is sent on submit (see handleSubmit), and guards
  // the same render-time reseed race as nameTouched/slugTouched above.
  const [logoUrlTouched, setLogoUrlTouched] = useState(false);
  // Which affordance is shown: the URL input, or the "Uploaded file" badge
  // state. Last-action-wins: a successful upload switches to the badge view,
  // "Use an external URL instead" switches back to the input.
  const [showLogoUrlField, setShowLogoUrlField] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const fileInput = useRef<HTMLInputElement>(null);

  const updateProfile = useUpdateOrgProfile(org);
  const uploadLogo = useUploadOrgLogo(org);
  const clearLogo = useClearOrgLogo(org);

  // Seed the form from the loaded organization, and re-seed whenever the
  // server-side identity actually changes (a different org, or a save that
  // came back with new values). Adjusting state during render rather than in
  // an effect is the documented pattern for "reset state when a prop changes"
  // — an effect here would cascade an extra render on every org load.
  //
  // Two distinct triggers share this block, and only one of them may
  // override an in-progress edit:
  //  - `org` itself changing is a genuine navigation to a different
  //    organization (this component is never remounted for that — see
  //    organization.settings.tsx, no `key={org}`) — any unsaved edits for
  //    the org being left are stale and always discarded.
  //  - `current` changing while `org` stays the same is typically the
  //    background /auth/me refresh that follows a save (applyResult below
  //    awaits AuthContext.refreshUser()). That refresh can land well after
  //    the save, so it must never clobber a field the user has since
  //    started editing — each field's own *Touched flag gates it. Without
  //    this guard, saving the name and then immediately editing the slug
  //    silently reverts the slug once the refresh lands (spec
  //    2026-08-28-13 audit — reproduced 17/20 runs before this fix).
  const syncKey = `${org}|${current?.name ?? ""}|${current?.logoUrl ?? ""}`;
  const [syncedFrom, setSyncedFrom] = useState<string | null>(null);
  const [syncedOrg, setSyncedOrg] = useState<string | null>(null);
  const orgChanged = syncedOrg !== org;

  // Shared by the render-time sync below and by applyResult (after a save,
  // upload or clear) — both need to re-derive the logo source from a fresh
  // server value, resetting the draft/touched/mode trio in lockstep so the
  // URL field can never end up holding an uploaded path. `force` bypasses
  // the touched guard: applyResult always forces, since it reflects the
  // user's own just-completed action, not a stale background refresh.
  const syncLogoState = (url: string, force = false) => {
    setLogoUrl(url);
    if (!force && logoUrlTouched) return;
    const uploaded = isUploadedLogoPath(url);
    setShowLogoUrlField(!uploaded);
    setLogoUrlDraft(uploaded ? "" : url);
    setLogoUrlTouched(false);
  };

  if (orgChanged) {
    setSyncedOrg(org);
    setSyncedFrom(syncKey);
    setName(current?.name ?? "");
    setSlug(org);
    setNameTouched(false);
    setSlugTouched(false);
    syncLogoState(current?.logoUrl ?? "", true);
  } else if (syncedFrom !== syncKey) {
    setSyncedFrom(syncKey);
    if (!nameTouched) setName(current?.name ?? "");
    if (!slugTouched) setSlug(org);
    syncLogoState(current?.logoUrl ?? "");
  }

  const busy =
    updateProfile.isPending || uploadLogo.isPending || clearLogo.isPending;
  const slugChanged = slug !== org;

  // `savedProfile` is true only when this result came from the name/slug
  // form submitting (handleSubmit) — the one flow where the user has just
  // explicitly confirmed both fields, so it's safe to adopt them synchronously
  // and mark them clean immediately. Doing that here, rather than waiting for
  // the refreshUser() round trip below to update `current` and flow through
  // the render-time reseed, closes the exact race the audit reproduced
  // 17/20 runs: without it, a slug edit typed in the gap between the save
  // and refreshUser's response would still be live (touched) when the
  // refresh landed, but nothing had cleared the *previous* save's touched
  // flag yet from that later, slower path. Upload/clear (handleFile,
  // handleClearLogo) never confirm name/slug, so they must NOT sync or
  // untouch those fields — any concurrent name/slug edit stays exactly as
  // the user left it, protected by the render-time reseed's own touched
  // guard once refreshUser eventually resolves.
  const applyResult = async (
    result: OrgProfileResponse,
    savedProfile = false,
  ) => {
    if (savedProfile) {
      setName(result.name);
      setNameTouched(false);
      setSlug(result.slug);
      setSlugTouched(false);
    }
    syncLogoState(result.logoUrl ?? "", true);

    if (result.accessToken && result.slug !== org) {
      // Swap the session BEFORE navigating: the token in hand is scoped to the
      // old slug and every request to the new URL would 403 with it.
      await adoptRenamedOrgSession({
        slug: result.slug,
        accessToken: result.accessToken,
        refreshToken: result.refreshToken,
        expiresIn: result.expiresIn,
      });
      await navigate({
        to: "/orgs/$org/organization/settings",
        params: { org: result.slug },
      });
      return;
    }

    await refreshUser();
  };

  const reportError = (err: unknown) => {
    const message =
      err instanceof ApiError ? err.message : t("settings.unexpectedError");
    setError(message);
    toast.error(message);
  };

  const handleSubmit = async (event: React.FormEvent) => {
    event.preventDefault();
    setError(null);

    try {
      const result = await updateProfile.mutateAsync({
        name,
        slug,
        // Send logoUrl only when the user explicitly typed into (or cleared)
        // the external-URL field this session. An uploaded logo is a
        // relative /pub/assets path the endpoint refuses by design, and
        // merely switching the view to "Use an external URL instead" without
        // typing anything must not clear it — that only happens once the
        // user has actually edited the field.
        ...(logoUrlTouched &&
        (logoUrlDraft.startsWith("http") || logoUrlDraft === "")
          ? { logoUrl: logoUrlDraft === "" ? null : logoUrlDraft }
          : {}),
      });
      toast.success(t("settings.saved"));
      await applyResult(result, true);
    } catch (err) {
      reportError(err);
    }
  };

  const handleFile = async (event: React.ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0];
    event.target.value = "";

    if (!file) return;

    setError(null);

    try {
      const result = await uploadLogo.mutateAsync(file);
      toast.success(t("settings.profile.logoUploaded", "Logo updated."));
      await applyResult(result);
    } catch (err) {
      reportError(err);
    }
  };

  const handleClearLogo = async () => {
    setError(null);

    try {
      const result = await clearLogo.mutateAsync();
      await applyResult(result);
    } catch (err) {
      reportError(err);
    }
  };

  return (
    <Card data-testid="org-profile-card">
      <CardHeader>
        <CardTitle>
          {t("settings.profile.title", "Organization profile")}
        </CardTitle>
        <CardDescription>
          {t(
            "settings.profile.description",
            "The organization's name, its URL slug, and the logo shown across the dashboard and its status pages.",
          )}
        </CardDescription>
      </CardHeader>
      <CardContent>
        <form onSubmit={handleSubmit} className="space-y-4">
          {error && (
            <Alert variant="destructive">
              <AlertCircle className="h-4 w-4" />
              <AlertDescription data-testid="org-profile-error">
                {error}
              </AlertDescription>
            </Alert>
          )}

          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-2">
              <Label htmlFor="org-profile-name">
                {t("settings.profile.name", "Name")}
              </Label>
              <Input
                id="org-profile-name"
                value={name}
                // Deliberately does NOT derive the slug: this card edits a
                // live organization, where the slug is a load-bearing address
                // (dashboard links, status pages, badges, embedded widgets).
                // Renaming it is an explicit act, never a side effect of
                // retitling the org — unlike the create form on /no-org, where
                // auto-slugify is the right default.
                onChange={(event) => {
                  setName(event.target.value);
                  setNameTouched(true);
                }}
                disabled={busy}
                data-testid="org-profile-name"
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="org-profile-slug">
                {t("settings.profile.slug", "URL slug")}
              </Label>
              <Input
                id="org-profile-slug"
                value={slug}
                onChange={(event) => {
                  setSlug(event.target.value);
                  setSlugTouched(true);
                }}
                disabled={busy}
                data-testid="org-profile-slug"
              />
            </div>
          </div>

          {slugChanged && (
            <Alert variant="warning" data-testid="org-profile-rename-warning">
              <AlertCircle className="h-4 w-4" />
              <AlertDescription>
                {t(
                  "settings.profile.renameWarning",
                  "Every URL of this organization changes — dashboard links, status pages, SVG badges and embedded widgets. Existing links keep working: the old address redirects to the new one, but only until another organization claims it.",
                )}
              </AlertDescription>
            </Alert>
          )}

          <div className="space-y-2">
            <Label htmlFor="org-profile-logo">
              {t("settings.profile.logo", "Logo")}
            </Label>
            <div className="flex flex-wrap items-center gap-3">
              <div className="flex h-14 w-14 shrink-0 items-center justify-center overflow-hidden rounded-md border bg-muted">
                {logoUrl ? (
                  <img
                    src={logoUrl}
                    alt=""
                    className="h-full w-full object-contain"
                    data-testid="org-profile-logo-preview"
                  />
                ) : (
                  <Building2 className="h-6 w-6 text-muted-foreground" />
                )}
              </div>
              {showLogoUrlField ? (
                <Input
                  id="org-profile-logo"
                  type="url"
                  inputMode="url"
                  placeholder="https://example.com/logo.png"
                  value={logoUrlDraft}
                  onChange={(event) => {
                    setLogoUrlDraft(event.target.value);
                    setLogoUrlTouched(true);
                  }}
                  disabled={busy}
                  className="min-w-0 flex-1"
                  data-testid="org-profile-logo-url"
                />
              ) : (
                <div className="flex min-w-0 flex-1 flex-wrap items-center gap-x-3 gap-y-1">
                  <Badge
                    variant="secondary"
                    data-testid="org-profile-logo-badge"
                  >
                    {t("settings.profile.uploadedBadge", "Uploaded file")}
                  </Badge>
                  <Button
                    id="org-profile-logo"
                    type="button"
                    variant="link"
                    className="h-auto p-0 text-xs"
                    onClick={() => {
                      setShowLogoUrlField(true);
                      setLogoUrlDraft("");
                      setLogoUrlTouched(false);
                    }}
                    disabled={busy}
                    data-testid="org-profile-logo-use-url"
                  >
                    {t(
                      "settings.profile.useExternalUrl",
                      "Use an external URL instead",
                    )}
                  </Button>
                </div>
              )}
              <input
                ref={fileInput}
                type="file"
                accept={LOGO_ACCEPT}
                onChange={handleFile}
                className="hidden"
                data-testid="org-profile-logo-file"
              />
              <Button
                type="button"
                variant="outline"
                onClick={() => fileInput.current?.click()}
                disabled={busy}
                data-testid="org-profile-logo-upload"
              >
                {uploadLogo.isPending ? (
                  <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                ) : (
                  <Upload className="mr-2 h-4 w-4" />
                )}
                {logoUrl
                  ? t("settings.profile.replace", "Replace")
                  : t("settings.profile.upload", "Upload")}
              </Button>
              {logoUrl && (
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  className="text-destructive"
                  onClick={handleClearLogo}
                  disabled={busy}
                  aria-label={t("settings.profile.removeLogo", "Remove logo")}
                  data-testid="org-profile-logo-clear"
                >
                  <Trash2 className="h-4 w-4" />
                </Button>
              )}
            </div>
            <p className="text-xs text-muted-foreground">
              {showLogoUrlField
                ? t(
                    "settings.profile.logoHelp",
                    "Paste an image URL, or upload a PNG, JPEG, WebP, GIF or SVG of up to 1 MB.",
                  )
                : t(
                    "settings.profile.logoHelpUploaded",
                    "This logo was uploaded. Upload a new file to replace it, or switch to an external URL.",
                  )}
            </p>
          </div>

          <Button type="submit" disabled={busy} data-testid="org-profile-save">
            {updateProfile.isPending ? (
              <>
                <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                {tc("saving")}
              </>
            ) : (
              tc("save")
            )}
          </Button>
        </form>
      </CardContent>
    </Card>
  );
}
