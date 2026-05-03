import { useTranslation } from "react-i18next";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";

const LANGUAGES = [
  { code: "en", flag: "\u{1F1FA}\u{1F1F8}", label: "English" },
  { code: "fr", flag: "\u{1F1EB}\u{1F1F7}", label: "Français" },
  { code: "de", flag: "\u{1F1E9}\u{1F1EA}", label: "Deutsch" },
  { code: "es", flag: "\u{1F1EA}\u{1F1F8}", label: "Español" },
] as const;

type LanguageCode = (typeof LANGUAGES)[number]["code"];

function resolveLang(lang: string | undefined): LanguageCode {
  const prefix = lang?.split("-")[0]?.toLowerCase();
  const match = LANGUAGES.find((l) => l.code === prefix);
  return match ? match.code : "en";
}

export function LanguageSwitcher() {
  const { i18n } = useTranslation();
  const currentLang = resolveLang(i18n.language);
  const currentFlag = LANGUAGES.find((l) => l.code === currentLang)?.flag;

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          className="inline-flex items-center justify-center rounded-md text-xl leading-none ring-offset-background transition-colors hover:bg-accent h-9 w-9"
          title={currentLang.toUpperCase()}
        >
          {currentFlag}
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        {LANGUAGES.map(({ code, flag, label }) => (
          <DropdownMenuItem
            key={code}
            onClick={() => i18n.changeLanguage(code)}
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
