import { createFileRoute } from "@tanstack/react-router";
import { StatusPageRoute } from "@/components/pages/status-page-route";

export const Route = createFileRoute("/$org/$slug")({
  component: StatusPageRoute,
});
