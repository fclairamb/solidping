import { Trans, useTranslation } from "react-i18next";
import { CheckCircle2 } from "lucide-react";

import { AuroraPanel } from "@/components/ui/aurora-panel";
import { Logo } from "@/components/ui/logo";
import { ThemeToggle } from "@/components/ui/theme-toggle";
import { LanguageSwitcher } from "@/components/shared/language-switcher";

// Bright, luminous lift of the brand crimson — readable on the dark aurora.
const ACCENT = "text-[oklch(0.82_0.13_5)]";

/**
 * Split-screen shell shared by every auth / onboarding flow (login, register,
 * forgot + reset password, registration confirmation). A brand aurora
 * marketing panel fills the left half on large screens; the page's own card
 * renders centered in the right column. Below `lg` the panel is hidden and only
 * the card shows, so each flow stays fully usable on mobile. All copy comes
 * from the `auth` i18n namespace (`marketing.*`).
 */
export function AuthSplitLayout({ children }: { children: React.ReactNode }) {
  const { t } = useTranslation("auth");
  const features = [
    t("marketing.features.protocols"),
    t("marketing.features.workers"),
    t("marketing.features.incidents"),
  ];

  return (
    <div className="relative grid min-h-screen bg-background lg:grid-cols-2">
      {/* Language + theme controls — pinned top-right, over the form column on
          every breakpoint (the aurora panel is hidden below lg). */}
      <div className="absolute right-4 top-4 z-20 flex items-center gap-1">
        <LanguageSwitcher />
        <ThemeToggle className="inline-flex h-9 w-9 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground" />
      </div>

      {/* Brand aurora marketing panel — large screens only. */}
      <AuroraPanel className="hidden p-12 lg:block xl:p-16">
        <Logo size={32} variant="wordmark" className="text-white" />
        <div className="flex flex-1 flex-col justify-center">
          <div className="glass max-w-md space-y-6 rounded-3xl p-8">
            <h2 className="text-3xl font-bold leading-tight tracking-tight">
              <Trans
                t={t}
                i18nKey="marketing.headline"
                components={{ accent: <span className={ACCENT} /> }}
              />
            </h2>
            <p className="text-white/70">{t("marketing.description")}</p>
            <ul className="space-y-3 text-sm text-white/90">
              {features.map((line) => (
                <li key={line} className="flex items-center gap-3">
                  <CheckCircle2 className={`h-5 w-5 shrink-0 ${ACCENT}`} />
                  {line}
                </li>
              ))}
            </ul>
          </div>
        </div>
        <p className="text-xs text-white/50">© SolidPing</p>
      </AuroraPanel>

      {/* Form / card column. */}
      <div className="flex items-center justify-center p-4">{children}</div>
    </div>
  );
}
