import type { ReactNode } from "react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";

interface PromotedMethodButtonProps {
  icon: ReactNode;
  label: string;
  /** The translated "Last used" badge text. */
  badge: string;
  onClick: () => void;
  disabled?: boolean;
  "data-testid": string;
}

/**
 * The full-width "last used" sign-in button at the top of /login (OAuth
 * provider or passkey), with its "Last used" badge.
 *
 * It wraps instead of overflowing: the base Button is `whitespace-nowrap` with
 * a fixed height, which fits "Sign in with passkey · Last used" in English but
 * pushed the label and the badge out of the card on a phone in French, German
 * and Spanish (solidping-business#32). Here the label may wrap and the badge
 * drops to its own line when the row is too narrow.
 */
export function PromotedMethodButton({
  icon,
  label,
  badge,
  onClick,
  disabled,
  "data-testid": testId,
}: PromotedMethodButtonProps) {
  return (
    <Button
      type="button"
      variant="outline"
      className="h-auto min-h-9 w-full flex-wrap gap-x-2 gap-y-1 whitespace-normal py-2 text-center"
      onClick={onClick}
      disabled={disabled}
      data-testid={testId}
    >
      {icon}
      <span className="min-w-0">{label}</span>
      <Badge variant="secondary" data-testid="login-last-used-badge">
        {badge}
      </Badge>
    </Button>
  );
}
