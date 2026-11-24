// The "Host and port / Unix socket" target of the redis and postgres forms
// (spec 2026-11-24-03), same switch as the tcp form. Whether it is offered is
// the shared useLocalSocketOffered decision; a stored socket is always shown.
import { useTranslation } from "react-i18next";
import { cn } from "@/lib/utils";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { getFieldError } from "@/hooks/use-check-validation";
import { useLocalSocketOffered } from "../local-socket";
import type { FieldErrors } from "./common";
import { validationMessage } from "./common";

export type SocketTargetMode = "host" | "socket";

export const socketRequired = (socket: string): FieldErrors =>
  socket
    ? []
    : [{ name: "socket", message: validationMessage("socketRequired") }];

/** Whether the target switch is shown for a form whose state is `mode`. */
export function useShowTargetSwitch(mode: SocketTargetMode): boolean {
  const offered = useLocalSocketOffered();
  return offered || mode === "socket";
}

interface SocketTargetSwitchProps {
  /** testid prefix: `redis` gives redis-target-mode, redis-socket-path... */
  prefix: string;
  mode: SocketTargetMode;
  socket: string;
  placeholder: string;
  errors: FieldErrors;
  onModeChange: (mode: SocketTargetMode) => void;
  onSocketChange: (socket: string) => void;
}

/** The target-mode select, and the socket path input in socket mode. */
export function SocketTargetSwitch({
  prefix,
  mode,
  socket,
  placeholder,
  errors,
  onModeChange,
  onSocketChange,
}: SocketTargetSwitchProps) {
  const { t } = useTranslation("checks");
  const error = getFieldError(errors, "socket");
  return (
    <>
      <div className="space-y-2">
        <Label htmlFor={`${prefix}TargetMode`}>{t("network.targetMode")}</Label>
        <Select
          value={mode}
          onValueChange={(value) => onModeChange(value as SocketTargetMode)}
        >
          <SelectTrigger
            id={`${prefix}TargetMode`}
            className="w-56"
            data-testid={`${prefix}-target-mode`}
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="host" data-testid={`${prefix}-target-mode-host`}>
              {t("network.targetHostPort")}
            </SelectItem>
            <SelectItem
              value="socket"
              data-testid={`${prefix}-target-mode-socket`}
            >
              {t("network.targetSocket")}
            </SelectItem>
          </SelectContent>
        </Select>
      </div>
      {mode === "socket" && (
        <div className="space-y-2">
          <Label htmlFor={`${prefix}SocketPath`}>
            {t("network.socketPath")}
          </Label>
          <Input
            id={`${prefix}SocketPath`}
            type="text"
            placeholder={placeholder}
            value={socket}
            onChange={(e) => onSocketChange(e.target.value)}
            className={cn("font-mono", error && "border-destructive")}
            data-testid={`${prefix}-socket-path`}
          />
          <p className="text-xs text-muted-foreground">
            {t("network.socketHelp")}
          </p>
          {error && (
            <p
              className="text-xs text-destructive"
              data-testid={`${prefix}-socket-error`}
            >
              {error}
            </p>
          )}
        </div>
      )}
    </>
  );
}
