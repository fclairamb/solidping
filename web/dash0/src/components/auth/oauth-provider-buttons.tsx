import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { ProviderIcon } from "@/components/auth/provider-icon";
import { startOAuthLogin } from "@/lib/login-destination";
import type { AuthProvider } from "@/api/hooks";

export interface OAuthProviderButtonsProps {
  /** Org slug the sign-in is scoped to. */
  org: string;
  /**
   * Providers to render, already filtered by the caller — /login removes the
   * one it promoted to the "last used" slot, /register shows them all.
   */
  providers: AuthProvider[] | undefined;
  disabled?: boolean;
  /**
   * The deep link captured on the way into /login, forwarded so the provider
   * round-trip lands back on it. /register omits it: a visitor creating an
   * account was not bounced off a protected page.
   */
  returnTo?: string;
  /** Namespaces the `data-testid`s so the two pages stay distinguishable. */
  testIdPrefix: "login" | "register";
}

/**
 * The two-column grid of third-party sign-in buttons, plus the "or" divider
 * that separates it from the password form below.
 *
 * Shared by /login and /register (spec 2026-09-09-02): every provider callback
 * runs `findOrCreateUser`, so a first-time "Continue with GitHub" *is*
 * registration — there is no separate sign-up endpoint to build, and the
 * register page was simply missing the buttons.
 *
 * Clicking always records `oauth:<type>` as the last-used method, on both
 * pages. On /register that is the point: the account being created now should
 * be promoted on the *next* visit to /login.
 *
 * Renders nothing when there is no provider to show, so callers can drop it in
 * unconditionally without repeating the emptiness check.
 */
export function OAuthProviderButtons({
  org,
  providers,
  disabled,
  returnTo,
  testIdPrefix,
}: OAuthProviderButtonsProps) {
  const { t: tc } = useTranslation("common");

  if (!providers || providers.length === 0) return null;

  return (
    <div className="mb-3">
      <div className="grid grid-cols-2 gap-2">
        {providers.map((provider) => (
          <Button
            key={provider.type}
            variant="outline"
            size="sm"
            className="w-full"
            disabled={disabled}
            onClick={() =>
              startOAuthLogin({ org, providerType: provider.type, returnTo })
            }
            data-testid={`${testIdPrefix}-oauth-${provider.type}`}
          >
            <ProviderIcon type={provider.type} className="mr-1.5 h-4 w-4" />
            {provider.name}
          </Button>
        ))}
      </div>
      <div className="relative my-3">
        <div className="absolute inset-0 flex items-center">
          <span className="w-full border-t" />
        </div>
        <div className="relative flex justify-center text-xs uppercase">
          <span className="bg-card px-2 text-muted-foreground">{tc("or")}</span>
        </div>
      </div>
    </div>
  );
}
