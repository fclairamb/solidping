import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Moon, Sun } from "lucide-react";

function getInitialTheme(): "light" | "dark" {
  const stored = localStorage.getItem("theme");
  const prefersDark = window.matchMedia("(prefers-color-scheme: dark)").matches;
  return stored === "dark" || (!stored && prefersDark) ? "dark" : "light";
}

/**
 * Light/dark toggle, ported from dash0
 * (web/dash0/src/components/ui/theme-toggle.tsx) so the public status page
 * matches the operator console's theming mechanism exactly: same
 * localStorage("theme") key, same three effective states (stored light,
 * stored dark, follow-system).
 *
 * The state here starts from the same class index.html's inline no-flash
 * script already applied to <html> before this component ever mounts (see
 * the script in index.html) — this hook just needs to agree with that
 * script's logic so the first render's `theme` state matches what's already
 * on the page, with no visible flip.
 */
export function ThemeToggle({ className }: { className?: string }) {
  const { t } = useTranslation();
  const [theme, setTheme] = useState<"light" | "dark">(getInitialTheme);

  useEffect(() => {
    document.documentElement.classList.toggle("dark", theme === "dark");
  }, [theme]);

  const toggleTheme = () => {
    const newTheme = theme === "light" ? "dark" : "light";
    setTheme(newTheme);
    localStorage.setItem("theme", newTheme);
  };

  return (
    <button
      type="button"
      data-testid="theme-toggle"
      onClick={toggleTheme}
      className={
        className ??
        "inline-flex items-center justify-center rounded-md text-sm font-medium ring-offset-background transition-colors hover:bg-accent hover:text-accent-foreground h-9 w-9"
      }
      aria-label={theme === "light" ? t("switchToDarkMode") : t("switchToLightMode")}
    >
      {theme === "light" ? (
        <Moon className="h-4 w-4" />
      ) : (
        <Sun className="h-4 w-4" />
      )}
    </button>
  );
}
