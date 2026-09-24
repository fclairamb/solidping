import { useTranslation } from "react-i18next";
import { Badge } from "@/components/ui/badge";
import { statusStyle } from "@/lib/status-style";
import { cn } from "@/lib/utils";

interface StatusBadgeProps {
  status: string | undefined | null;
  className?: string;
}

export function StatusBadge({ status, className }: StatusBadgeProps) {
  const { t } = useTranslation("checks");
  if (!status) return null;

  const style = statusStyle(status);

  // Every status reads its localized label from status.* — never the raw
  // wire token. Printing the raw word for anything with a secondary badge is
  // how "created", "abandoned" and friends leaked untranslated into fr/de/es;
  // an unrecognized future status still falls back to its own token through
  // defaultLabel rather than to the key.
  const Icon = style.icon;
  const label =
    style.labelKey === "status.unknown" && status !== "unknown"
      ? t(`status.${status}`, style.defaultLabel)
      : t(style.labelKey, style.defaultLabel);

  return (
    <Badge
      variant={style.badgeVariant}
      className={cn(Icon && "gap-1", className)}
      data-status={status}
    >
      {Icon && <Icon className="h-3 w-3" aria-hidden="true" />}
      {label}
    </Badge>
  );
}
