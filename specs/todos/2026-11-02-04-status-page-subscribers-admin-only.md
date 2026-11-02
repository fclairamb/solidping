---
model: opus
effort: low
pr: batch
automerge: false
---

# Viewers can list a status page's subscriber email addresses

## Problem

Found by the security scan of 2026-11-02 (finding AC5). Severity: low. Any viewer of the org can trigger it, and so can a viewer's PAT.

- `GET /api/v1/orgs/:org/status-pages/:statusPageUid/subscribers` is registered on `orgStatusPages`, an `orgGroup` whose write floor lets every GET through (`server/internal/app/server.go:2148`).
- Neither `ListSubscribers` in the handler (`server/internal/handlers/statussubscribers/handler.go:291`) nor the service (`service.go:537-570`) checks the role.
- The response carries every subscriber's email address, channel, scope, incident and failure count.
- These are third parties' addresses (people who subscribed on the public page), not org data. Elsewhere, viewers are kept away from admin-only families: the events service withholds them (`server/internal/handlers/events/service.go:171-185`).
- The route comment calls it "Authed admin". The removal route already needs write access.

## Proposal

1. Require org admin for the subscriber list: `RequireOrgAdmin` on the route, or `HasOrgRole(admin)` in `ListSubscribers`. Answer `403 FORBIDDEN` to viewers and users.
2. If dash0 shows the subscriber list on the status page settings, hide it below admin, with the usual "Permission Denied" handling for a 403.
3. Mention the role in `wiki/api-specification/` for that endpoint.

## Tests

- `server/internal/handlers/statussubscribers` (or the app router test that builds the real chain): a viewer and a user get 403 on the list. An admin gets 200 with the addresses (positive control). DELETE keeps its current rule.

## Open questions

- Should `user` (write) members keep the list? **Recommended:** no, admin only: it is personal data of third parties, and nothing a `user` does needs it.
