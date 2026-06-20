import { useTranslation } from "react-i18next";
import { Badge } from "@/components/ui/badge";
import { statusStyle } from "@/lib/status-style";

interface StatusBadgeProps {
  status: string | undefined | null;
  className?: string;
}

export function StatusBadge({ status, className }: StatusBadgeProps) {
  const { t } = useTranslation("checks");
  if (!status) return null;

  const style = statusStyle(status);

  // created/unknown keep their raw token as the label (secondary badge);
  // every known status reads its localized label from status.*.
  const label =
    style.badgeVariant === "secondary"
      ? status
      : t(style.labelKey, style.defaultLabel);

  return (
    <Badge variant={style.badgeVariant} className={className}>
      {label}
    </Badge>
  );
}
