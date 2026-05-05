import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

type Preset = "15m" | "1h" | "4h" | "tomorrow9am" | "custom";

interface SnoozeDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  isPending?: boolean;
  // Receives either {duration: "15m"} for the relative quick-selects (the
  // backend re-anchors to wall-clock), or {until: "<RFC3339>"} for
  // tomorrow-9am. Reason is optional.
  onSubmit: (payload: { duration?: string; until?: string; reason?: string }) => void;
}

function tomorrow9amISO(): string {
  const next = new Date();
  next.setDate(next.getDate() + 1);
  next.setHours(9, 0, 0, 0);
  return next.toISOString();
}

export function SnoozeDialog({ open, onOpenChange, isPending, onSubmit }: SnoozeDialogProps) {
  const { t } = useTranslation("incidents");
  const [preset, setPreset] = useState<Preset>("1h");
  const [custom, setCustom] = useState("");
  const [reason, setReason] = useState("");

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();

    const trimmedReason = reason.trim();

    if (preset === "tomorrow9am") {
      onSubmit({ until: tomorrow9amISO(), reason: trimmedReason || undefined });
      return;
    }

    const duration =
      preset === "custom" ? custom.trim() : preset; // "15m" | "1h" | "4h"

    if (!duration) {
      return;
    }

    onSubmit({ duration, reason: trimmedReason || undefined });
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("snoozeDialog.title")}</DialogTitle>
          <DialogDescription>{t("snoozeDialog.description")}</DialogDescription>
        </DialogHeader>
        <form onSubmit={handleSubmit} className="space-y-4">
          <div className="space-y-2">
            <Label>{t("snoozeDialog.quickSelect")}</Label>
            <div className="grid grid-cols-2 gap-2">
              {(
                [
                  { key: "15m", label: t("snoozeDialog.until15m") },
                  { key: "1h", label: t("snoozeDialog.until1h") },
                  { key: "4h", label: t("snoozeDialog.until4h") },
                  { key: "tomorrow9am", label: t("snoozeDialog.untilTomorrow9am") },
                ] as Array<{ key: Preset; label: string }>
              ).map((opt) => (
                <Button
                  key={opt.key}
                  type="button"
                  variant={preset === opt.key ? "default" : "outline"}
                  size="sm"
                  onClick={() => setPreset(opt.key)}
                >
                  {opt.label}
                </Button>
              ))}
            </div>
          </div>

          <div className="space-y-2">
            <Label htmlFor="snooze-custom">{t("snoozeDialog.customDuration")}</Label>
            <Input
              id="snooze-custom"
              value={custom}
              onChange={(e) => {
                setCustom(e.target.value);
                if (e.target.value) {
                  setPreset("custom");
                }
              }}
              placeholder="2h"
              disabled={isPending}
            />
          </div>

          <div className="space-y-2">
            <Label htmlFor="snooze-reason">{t("snoozeDialog.reason")}</Label>
            <Input
              id="snooze-reason"
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              placeholder={t("snoozeDialog.reasonPlaceholder")}
              disabled={isPending}
            />
          </div>

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenChange(false)}
              disabled={isPending}
            >
              {t("snoozeDialog.cancel")}
            </Button>
            <Button type="submit" disabled={isPending}>
              {isPending ? <Loader2 className="mr-2 h-4 w-4 animate-spin" /> : null}
              {t("snoozeDialog.submit")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
