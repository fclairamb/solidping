import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { useAuth } from "@/contexts/AuthContext";
import { LANGUAGES, resolveLang, type LanguageCode } from "@/lib/languages";

/**
 * Flag menu that switches the dashboard language. Signed in, the choice is
 * also saved as the user's language, the one their emails are written in
 * (spec 2026-10-21-01); signed out it stays in this browser.
 */
export function LanguageSwitcher() {
  const { i18n, t } = useTranslation("common");
  const { changeLanguage } = useAuth();
  const currentLang = resolveLang(i18n.language);
  const currentFlag = LANGUAGES.find((l) => l.code === currentLang)?.flag;

  const pick = (code: LanguageCode) => {
    changeLanguage(code).catch(() => {
      toast.error(t("languageSaveFailed"));
    });
  };

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          className="inline-flex items-center justify-center rounded-md text-xl leading-none ring-offset-background transition-colors hover:bg-accent h-9 w-9"
          title={currentLang.toUpperCase()}
          data-testid="language-switcher"
        >
          {currentFlag}
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        {LANGUAGES.map(({ code, flag, label }) => (
          <DropdownMenuItem
            key={code}
            onClick={() => pick(code)}
            data-testid={`language-switcher-option-${code}`}
          >
            <span className="text-base">{flag}</span>
            {label}
            {currentLang === code && <span className="ml-auto">&#10003;</span>}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
