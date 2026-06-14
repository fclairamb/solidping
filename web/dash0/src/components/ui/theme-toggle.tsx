import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Moon, Sun } from "lucide-react";

function getInitialTheme(): "light" | "dark" {
  const stored = localStorage.getItem("theme");
  const prefersDark = window.matchMedia("(prefers-color-scheme: dark)").matches;
  return stored === "dark" || (!stored && prefersDark) ? "dark" : "light";
}

/**
 * Light/dark toggle. Owns the `theme` state, mirrors it onto the
 * `html.dark` class, and persists the choice to localStorage — the same
 * mechanism the sidebar and the auth screens share, so the preference is
 * consistent everywhere it appears.
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
