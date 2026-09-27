import { createFileRoute } from "@tanstack/react-router";
import { IndexPage } from "@/components/pages/index-page";

export const Route = createFileRoute("/")({
  component: IndexPage,
});
