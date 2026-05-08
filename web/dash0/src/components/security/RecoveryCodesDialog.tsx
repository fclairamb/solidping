import { useState } from "react";
import { useTranslation } from "react-i18next";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Copy } from "lucide-react";
import { toast } from "sonner";

interface RecoveryCodesDialogProps {
  open: boolean;
  codes: string[];
  onClose: () => void;
}

// Shown once after a successful Confirm2FA. Codes never echo back —
// users must save them now or lose them.
export function RecoveryCodesDialog({ open, codes, onClose }: RecoveryCodesDialogProps) {
  const { t } = useTranslation(["account", "common"]);
  const [saved, setSaved] = useState(false);

  const copyAll = () => {
    navigator.clipboard.writeText(codes.join("\n"));
    toast.success(t("common:copied"));
  };

  return (
    <Dialog open={open} onOpenChange={(o) => !o && saved && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("account:security.totp.recoveryTitle")}</DialogTitle>
          <DialogDescription>
            {t("account:security.totp.recoverySubtitle")}
          </DialogDescription>
        </DialogHeader>
        <div
          data-testid="2fa-recovery-codes"
          className="grid grid-cols-2 gap-2 rounded border p-3 font-mono text-sm"
        >
          {codes.map((code) => (
            <div key={code}>{code}</div>
          ))}
        </div>
        <Button
          variant="outline"
          data-testid="2fa-recovery-copy"
          onClick={copyAll}
          className="w-full"
        >
          <Copy className="mr-2 h-4 w-4" />
          {t("account:security.totp.copyAll")}
        </Button>
        <label className="flex items-center gap-2 text-sm">
          <Checkbox
            data-testid="2fa-recovery-saved-checkbox"
            checked={saved}
            onCheckedChange={(v) => setSaved(v === true)}
          />
          {t("account:security.totp.savedCheck")}
        </label>
        <DialogFooter>
          <Button
            data-testid="2fa-recovery-done"
            disabled={!saved}
            onClick={onClose}
          >
            {t("account:security.totp.done")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
