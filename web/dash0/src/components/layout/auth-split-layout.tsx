import { Trans, useTranslation } from "react-i18next";
import { CheckCircle2 } from "lucide-react";

import { AuroraPanel } from "@/components/ui/aurora-panel";
import { Logo } from "@/components/ui/logo";
import { ThemeToggle } from "@/components/ui/theme-toggle";
import { LanguageSwitcher } from "@/components/shared/language-switcher";

type AuthSplitLayoutProps = {
  children: React.ReactNode;
  /**
   * Show the SolidPing wordmark above the card below `lg`, where the aurora
   * panel (which carries it on large screens) is hidden. Default true. Pass
   * false only when the page's card already shows the wordmark itself
   * (login), so a phone never sees it twice.
   */
  mobileWordmark?: boolean;
};

/**
 * Split-screen shell shared by every auth / onboarding flow (login, register,
 * forgot + reset password, change password, registration confirmation). The
 * always-dark aurora marketing panel fills the left half on large screens;
 * the page's own card renders centered in the right column, over the page
 * glow (`bg-page-glow`) at every breakpoint. Below `lg` the panel is hidden,
 * so the glow and the wordmark above the card carry the identity and each
 * flow stays fully usable on mobile. The headline accent and the feature
 * checks use the `--aurora-accent` luminous cyan. All copy comes from the
 * `auth` i18n namespace (`marketing.*`).
 */
export function AuthSplitLayout({ children, mobileWordmark = true }: AuthSplitLayoutProps) {
  const { t } = useTranslation("auth");
  const features = [
    t("marketing.features.protocols"),
    t("marketing.features.workers"),
    t("marketing.features.incidents"),
  ];

  return (
    <div className="relative grid min-h-screen bg-background lg:grid-cols-2">
      {/* Brand aurora marketing panel — large screens only. */}
      <AuroraPanel className="hidden p-12 lg:block xl:p-16" data-testid="auth-aurora-panel">
        <Logo size={32} variant="wordmark" className="text-white" />
        <div className="flex flex-1 flex-col justify-center">
          <div className="glass max-w-md space-y-6 rounded-3xl p-8">
            <h2 className="text-3xl font-bold leading-tight tracking-tight">
              <Trans
                t={t}
                i18nKey="marketing.headline"
                components={{
                  accent: <span className="text-aurora-accent" data-testid="auth-aurora-accent" />,
                }}
              />
            </h2>
            <p className="text-white/70">{t("marketing.description")}</p>
            <ul className="space-y-3 text-sm text-white/90">
              {features.map((line) => (
                <li key={line} className="flex items-center gap-3">
                  <CheckCircle2 className="h-5 w-5 shrink-0 text-aurora-accent" />
                  {line}
                </li>
              ))}
            </ul>
          </div>
        </div>
        <p className="text-xs text-white/50">© SolidPing</p>
      </AuroraPanel>

      {/* Form / card column, over the page glow at every breakpoint. */}
      <div
        className="flex flex-col items-center justify-center gap-6 bg-page-glow p-4"
        data-testid="auth-form-column"
      >
        {mobileWordmark && (
          <div className="flex justify-center lg:hidden" data-testid="auth-mobile-wordmark">
            <Logo size={32} variant="wordmark" />
          </div>
        )}
        <div className="relative w-full max-w-md">
          {/* Language + theme controls — pinned to the card's top-right corner. */}
          <div className="absolute right-2 top-2 z-20 flex items-center gap-1">
            <LanguageSwitcher />
            <ThemeToggle className="inline-flex h-9 w-9 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground" />
          </div>
          {children}
        </div>
      </div>
    </div>
  );
}
