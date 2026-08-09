import { useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Trash2 } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";

/**
 * DangerZone is the red-bordered card that groups irreversible actions at the
 * bottom of a settings page. Keep the actions inside destructive (Trash2 +
 * `variant="destructive"`) — that is the repo-wide rule for anything that
 * cannot be undone.
 */
export function DangerZone({
  title,
  description,
  children,
  testId,
}: {
  title: string;
  description?: string;
  children: ReactNode;
  testId?: string;
}) {
  return (
    <Card className="mt-6 border-destructive/50" data-testid={testId}>
      <CardHeader>
        <CardTitle className="text-destructive">{title}</CardTitle>
        {description && <CardDescription>{description}</CardDescription>}
      </CardHeader>
      <CardContent>{children}</CardContent>
    </Card>
  );
}

/**
 * ConfirmByTypingButton is a destructive button whose AlertDialog only enables
 * its confirm action once the user has retyped `confirmValue` exactly. Use it
 * when the action destroys more than one object (deleting an organization, a
 * whole status page, …) and a plain yes/no confirmation is too cheap.
 */
export function ConfirmByTypingButton({
  buttonLabel,
  title,
  description,
  confirmValue,
  confirmLabel,
  inputLabel,
  onConfirm,
  disabled,
  testId,
}: {
  buttonLabel: string;
  title: string;
  description: string;
  confirmValue: string;
  confirmLabel: string;
  inputLabel: string;
  onConfirm: () => void | Promise<void>;
  disabled?: boolean;
  testId?: string;
}) {
  const { t: tc } = useTranslation("common");
  const [open, setOpen] = useState(false);
  const [typed, setTyped] = useState("");

  const matches = typed === confirmValue;

  return (
    <>
      <Button
        variant="destructive"
        disabled={disabled}
        onClick={() => {
          setTyped("");
          setOpen(true);
        }}
        data-testid={testId}
      >
        <Trash2 className="h-4 w-4" />
        {buttonLabel}
      </Button>

      <AlertDialog
        open={open}
        onOpenChange={(next) => {
          setOpen(next);
          if (!next) setTyped("");
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{title}</AlertDialogTitle>
            <AlertDialogDescription>{description}</AlertDialogDescription>
          </AlertDialogHeader>

          <div className="space-y-2">
            <Label htmlFor="confirm-by-typing">{inputLabel}</Label>
            <Input
              id="confirm-by-typing"
              value={typed}
              autoComplete="off"
              onChange={(e) => setTyped(e.target.value)}
              data-testid={testId ? `${testId}-input` : undefined}
            />
          </div>

          <AlertDialogFooter>
            <AlertDialogCancel>{tc("cancel")}</AlertDialogCancel>
            <AlertDialogAction
              disabled={!matches || disabled}
              onClick={(e) => {
                if (!matches) {
                  e.preventDefault();
                  return;
                }
                void onConfirm();
              }}
              data-testid={testId ? `${testId}-confirm` : undefined}
            >
              {confirmLabel}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}
