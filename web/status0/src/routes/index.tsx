import { createFileRoute } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";

export const Route = createFileRoute("/")({
  component: IndexPage,
});

function IndexPage() {
  const { t } = useTranslation();

  return (
    <div className="min-h-screen flex items-center justify-center">
      <div className="text-center">
        <h1 className="text-2xl font-bold">{t("solidpingStatus")}</h1>
        <p className="mt-2 text-muted-foreground">
          {t("visitStatusPage")}
        </p>
      </div>
    </div>
  );
}
