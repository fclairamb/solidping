import { createFileRoute, Navigate } from "@tanstack/react-router";
import { useAuth } from "@/contexts/AuthContext";

export const Route = createFileRoute("/")({
  component: RootRedirect,
});

function RootRedirect() {
  const { org } = useAuth();
  // Redirect to org-based route, default to "default"
  return <Navigate to="/orgs/$org" params={{ org: org || "default" }} />;
}
