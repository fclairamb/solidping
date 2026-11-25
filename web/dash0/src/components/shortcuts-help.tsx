import { useState } from "react";
import { useTranslation } from "react-i18next";

import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Kbd } from "@/components/ui/kbd";
import { useHotkey } from "@/hooks/use-hotkey";

const SHORTCUTS: { keys: string[]; labelKey: string }[] = [
  { keys: ["Ctrl/Cmd", "K"], labelKey: "shortcuts.commandMenu" },
  { keys: ["/"], labelKey: "shortcuts.focusSearch" },
  { keys: ["Esc"], labelKey: "shortcuts.resetFilters" },
  { keys: ["n"], labelKey: "shortcuts.newCheck" },
  { keys: ["Ctrl/Cmd", "Enter"], labelKey: "shortcuts.submitForm" },
  { keys: ["?"], labelKey: "shortcuts.help" },
];

/** The list of shortcuts, shared by the dialog and the design reference page. */
export function ShortcutsList() {
  const { t } = useTranslation("common");
  return (
    <ul className="space-y-2" data-testid="shortcuts-list">
      {SHORTCUTS.map((s) => (
        <li key={s.labelKey} className="flex items-center justify-between gap-4 text-sm">
          <span>{t(s.labelKey)}</span>
          <span className="flex items-center gap-1">
            {s.keys.map((k) => (
              <Kbd key={k}>{k}</Kbd>
            ))}
          </span>
        </li>
      ))}
    </ul>
  );
}

/** The `?` dialog listing keyboard shortcuts. Mounted once in the org layout. */
export function ShortcutsHelp() {
  const { t } = useTranslation("common");
  const [open, setOpen] = useState(false);
  // "?" is Shift+/ : the key value is "?" and no Ctrl/Cmd/Alt is held.
  useHotkey("?", () => setOpen(true), { preventDefault: true });

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogContent data-testid="shortcuts-help">
        <DialogHeader>
          <DialogTitle>{t("shortcuts.title")}</DialogTitle>
        </DialogHeader>
        <ShortcutsList />
      </DialogContent>
    </Dialog>
  );
}
