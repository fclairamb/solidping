import { Bug } from "lucide-react";
import { useTranslation } from "react-i18next";

interface FeedbackButtonProps {
  onClick: () => void;
  isCapturing?: boolean;
}

export function FeedbackButton({ onClick, isCapturing }: FeedbackButtonProps) {
  const { t } = useTranslation("feedback");

  return (
    <button
      type="button"
      data-testid="feedback-button"
      onClick={onClick}
      disabled={isCapturing}
      aria-label={t("trigger_title")}
      title={t("trigger_title")}
      className="inline-flex items-center justify-center rounded-md text-sm font-medium ring-offset-background transition-colors hover:bg-accent hover:text-accent-foreground h-9 w-9 disabled:opacity-50"
    >
      <Bug className="h-4 w-4" />
    </button>
  );
}
