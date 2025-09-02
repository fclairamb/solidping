import { LampDesk } from "lucide-react";
import { Button } from "./button";
import { useTheme } from "./theme-provider";

export function ThemeSwither() {
  const { setTheme, theme } = useTheme();
  return (
    <Button
      size="icon"
      variant="secondary"
      className="relative"
      onClick={() => setTheme(theme === "light" ? "dark" : "light")}
    >
      <LampDesk className="z-20" />
      <div className="size-4 rounded-full bg-amber-500/50 blur-xs opacity-0 dark:opacity-100 absolute translate-x-1 z-10" />
    </Button>
  );
}
