import { useTranslation } from "react-i18next";
import { Badge } from "@/components/ui/badge";

type Status = "up" | "down" | "error" | "validating" | "created" | string;

interface StatusBadgeProps {
  status: Status | undefined | null;
  className?: string;
}

export function StatusBadge({ status, className }: StatusBadgeProps) {
  const { t } = useTranslation("checks");
  if (!status) return null;

  if (status === "up") {
    return (
      <Badge variant="success" className={className}>
        {t("status.up", "up")}
      </Badge>
    );
  }
  if (status === "down" || status === "error") {
    return (
      <Badge variant="destructive" className={className}>
        {t("status.down", status)}
      </Badge>
    );
  }
  if (status === "validating" || status === "warning") {
    return (
      <Badge variant="warning" className={className}>
        {t("status.validating", status)}
      </Badge>
    );
  }
  // created, unknown, or any future value
  return (
    <Badge variant="secondary" className={className}>
      {status}
    </Badge>
  );
}
