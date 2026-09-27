import { createFileRoute } from "@tanstack/react-router";
import { DefaultStatusPage } from "@/components/pages/default-status-page";

export const Route = createFileRoute("/$org/")({
  component: DefaultStatusPage,
});
