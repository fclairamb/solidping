import { useTranslation } from "react-i18next";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";

const LANGUAGES = [
  { code: "en", flag: "\u{1F1EC}\u{1F1E7}", label: "English" },
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

  const switchLanguage = (code: string) => {
    i18n.changeLanguage(code);
    const url = new URL(window.location.href);
    url.searchParams.set("lang", code);
    window.history.replaceState(null, "", url.toString());
  };

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          className="text-xl leading-none p-1 rounded transition-colors hover:bg-accent"
          title={currentLang.toUpperCase()}
        >
          {currentFlag}
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        {LANGUAGES.map(({ code, flag, label }) => (
          <DropdownMenuItem
            key={code}
            onClick={() => switchLanguage(code)}
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
