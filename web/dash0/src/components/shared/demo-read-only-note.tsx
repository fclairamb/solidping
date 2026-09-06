import { useTranslation } from "react-i18next";
import { Lock } from "lucide-react";
import { useAuth } from "@/contexts/AuthContext";

interface DemoReadOnlyNoteProps {
  /** Optional test id, so a page can be asserted on individually. */
  testId?: string;
}

/**
 * DemoReadOnlyNote replaces a create/edit affordance a demo session cannot use
 * (spec 2026-09-06-02, §8 "hide what cannot be done").
 *
 * It exists because a disabled button with no explanation reads as a bug. The
 * server's write guard is what actually refuses these writes; this only spares
 * the visitor from discovering that by clicking.
 *
 * Renders nothing for an ordinary session, so every page it appears on is
 * unchanged for real customers.
 */
export function DemoReadOnlyNote({ testId = "demo-read-only-note" }: DemoReadOnlyNoteProps) {
  const { t } = useTranslation(["org"]);
  const { user } = useAuth();

  if (!user?.isDemo) {
    return null;
  }

  return (
    <p
      className="flex items-center gap-1.5 text-sm text-muted-foreground"
      data-testid={testId}
    >
      <Lock className="h-3.5 w-3.5" />
      <span>
        {t("org:demo.readOnly")} — {t("org:demo.readOnlyHint")}
      </span>
    </p>
  );
}
