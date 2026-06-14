/**
 * Legacy route — kept alive to handle bookmarked URLs.
 * Redirects to the new flat notification route with ?from=incident:{incidentUid}.
 */
import { useEffect } from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";

export const Route = createFileRoute(
  "/orgs/$org/incidents/$incidentUid_/notifications/$notificationUid",
)({
  component: LegacyNotificationRedirect,
});

function LegacyNotificationRedirect() {
  const navigate = useNavigate();
  const { org, incidentUid, notificationUid } = Route.useParams();

  useEffect(() => {
    void navigate({
      to: "/orgs/$org/notifications/$notificationUid",
      params: { org, notificationUid },
      search: { from: `incident:${incidentUid}` },
      replace: true,
    });
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  return null;
}
