import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Lock } from "lucide-react";
import { useUnlockStatusPage } from "@/api/hooks";
import { Logo } from "@/components/ui/logo";
import { ApiError } from "@/api/client";

/**
 * The unlock screen for a password-protected status page (spec 2026-08-21-07).
 *
 * It is shown in place of the whole page — not as an overlay — because there is
 * genuinely nothing to render behind it: the API answered 401 and no page data
 * exists on the client. A blurred-out "preview" would be a lie about what the
 * browser holds.
 *
 * The page's own name is deliberately NOT shown here. All this component was
 * given is an org/slug pair from the URL; asking the server for a title before
 * the visitor has proved anything would be exactly the leak the password is
 * meant to prevent.
 */
export function UnlockForm({
  org,
  slug,
  onUnlocked,
}: {
  org: string;
  slug?: string;
  /** Called after a successful unlock so the caller can refetch the page. */
  onUnlocked: () => void;
}) {
  const { t } = useTranslation();
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const unlock = useUnlockStatusPage(org, slug);

  const handleSubmit = async (event: React.FormEvent) => {
    event.preventDefault();
    setError(null);

    try {
      await unlock.mutateAsync(password);
      setPassword("");
      onUnlocked();
    } catch (err) {
      // A wrong password and a rate-limit both come back as an error with a
      // message the server already worded for a visitor; anything else falls
      // back to the generic string so a network blip never renders as blank.
      setError(
        err instanceof ApiError ? err.message : t("unlockGenericError"),
      );
    }
  };

  return (
    <div className="min-h-screen flex items-center justify-center px-4 py-12">
      <div className="w-full max-w-sm">
        <div className="flex flex-col items-center gap-3 text-center">
          <Logo size={32} />
          <div className="flex items-center gap-2 text-lg font-semibold tracking-tight">
            <Lock className="h-4 w-4" aria-hidden />
            {t("unlockTitle")}
          </div>
          <p className="text-sm text-muted-foreground">
            {t("unlockDescription")}
          </p>
        </div>

        <form onSubmit={handleSubmit} className="mt-6 space-y-3">
          <label htmlFor="status-page-password" className="sr-only">
            {t("unlockPasswordLabel")}
          </label>
          <input
            id="status-page-password"
            type="password"
            autoComplete="current-password"
            autoFocus
            required
            value={password}
            onChange={(event) => setPassword(event.target.value)}
            placeholder={t("unlockPasswordLabel")}
            data-testid="status-page-password-input"
            className="w-full rounded-md border border-border bg-background px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
          />
          {error && (
            <p
              className="text-sm text-destructive"
              role="alert"
              data-testid="status-page-password-error"
            >
              {error}
            </p>
          )}
          <button
            type="submit"
            disabled={unlock.isPending || password.length === 0}
            data-testid="status-page-unlock-submit"
            className="w-full rounded-md bg-primary px-3 py-2 text-sm font-medium text-primary-foreground disabled:opacity-50"
          >
            {unlock.isPending ? t("unlockSubmitting") : t("unlockSubmit")}
          </button>
        </form>
      </div>
    </div>
  );
}
