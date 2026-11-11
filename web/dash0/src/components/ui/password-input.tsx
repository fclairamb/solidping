import * as React from "react";
import { useTranslation } from "react-i18next";
import { Eye, EyeOff } from "lucide-react";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";
import { NO_CAPTURE_CLASS } from "@/lib/no-capture";

type PasswordInputProps = Omit<React.ComponentProps<"input">, "type"> & {
  showLabel?: string;
  hideLabel?: string;
};

const PasswordInput = React.forwardRef<HTMLInputElement, PasswordInputProps>(
  (
    {
      className,
      showLabel,
      hideLabel,
      onBlur,
      ...props
    },
    ref
  ) => {
    const { t } = useTranslation("auth");
    const [visible, setVisible] = React.useState(false);

    const toggleLabel = visible
      ? hideLabel ?? t("hidePassword")
      : showLabel ?? t("showPassword");

    const handleBlur = React.useCallback(
      (event: React.FocusEvent<HTMLInputElement>) => {
        // Re-mask on blur to defeat shoulder-surfing on an unattended screen.
        // Browser password managers behave the same way on autofill.
        setVisible(false);
        onBlur?.(event);
      },
      [onBlur]
    );

    return (
      <div className="relative">
        <Input
          {...props}
          ref={ref}
          type={visible ? "text" : "password"}
          onBlur={handleBlur}
          // Revealed, the value is plain text that rrweb no longer masks:
          // keep the field out of session replay either way (spec 2026-11-11-05).
          className={cn("pr-10", NO_CAPTURE_CLASS, className)}
        />
        <button
          type="button"
          aria-label={toggleLabel}
          aria-pressed={visible}
          onClick={() => setVisible((v) => !v)}
          // Render the toggle as an icon button at least 44px tappable for
          // touch — the visible icon is 16px but the hit area covers the
          // input height plus padding.
          className="absolute inset-y-0 right-0 flex h-full w-10 items-center justify-center rounded-md text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50"
          disabled={props.disabled}
          tabIndex={0}
        >
          {visible ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
        </button>
      </div>
    );
  }
);
PasswordInput.displayName = "PasswordInput";

export { PasswordInput };
