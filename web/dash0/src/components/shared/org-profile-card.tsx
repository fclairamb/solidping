import { useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "@tanstack/react-router";
import { toast } from "sonner";
import { AlertCircle, Building2, Loader2, Trash2, Upload } from "lucide-react";
import { Alert, AlertDescription } from "@/components/ui/alert";
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
  const [logoUrl, setLogoUrl] = useState("");
  const [error, setError] = useState<string | null>(null);

  const fileInput = useRef<HTMLInputElement>(null);

  const updateProfile = useUpdateOrgProfile(org);
  const uploadLogo = useUploadOrgLogo(org);
  const clearLogo = useClearOrgLogo(org);

  // Seed the form from the loaded organization, and re-seed whenever the
  // server-side identity actually changes (a different org, or a save that
  // came back with new values). Adjusting state during render rather than in
  // an effect is the documented pattern for "reset state when a prop changes"
  // — an effect here would cascade an extra render on every org load. The sync
  // key is deliberately narrow, so a user mid-edit is never clobbered by an
  // unrelated /me refresh.
  const syncKey = `${org}|${current?.name ?? ""}|${current?.logoUrl ?? ""}`;
  const [syncedFrom, setSyncedFrom] = useState<string | null>(null);

  if (syncedFrom !== syncKey) {
    setSyncedFrom(syncKey);
    setName(current?.name ?? "");
    setSlug(org);
    setLogoUrl(current?.logoUrl ?? "");
  }

  const busy =
    updateProfile.isPending || uploadLogo.isPending || clearLogo.isPending;
  const slugChanged = slug !== org;

  const applyResult = async (result: OrgProfileResponse) => {
    setLogoUrl(result.logoUrl ?? "");

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
        // Send logoUrl only when it is an external URL the user typed. An
        // uploaded logo is a relative /pub/assets path the endpoint refuses
        // by design, and re-sending it would clear the upload.
        ...(logoUrl.startsWith("http") || logoUrl === ""
          ? { logoUrl: logoUrl === "" ? null : logoUrl }
          : {}),
      });
      toast.success(t("settings.saved"));
      await applyResult(result);
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
                onChange={(event) => setName(event.target.value)}
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
                onChange={(event) => setSlug(event.target.value)}
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
              <Input
                id="org-profile-logo"
                type="url"
                inputMode="url"
                placeholder="https://example.com/logo.png"
                value={logoUrl}
                onChange={(event) => setLogoUrl(event.target.value)}
                disabled={busy}
                className="min-w-0 flex-1"
                data-testid="org-profile-logo-url"
              />
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
                {t("settings.profile.upload", "Upload")}
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
              {t(
                "settings.profile.logoHelp",
                "Paste an image URL, or upload a PNG, JPEG, WebP, GIF or SVG of up to 1 MB.",
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
