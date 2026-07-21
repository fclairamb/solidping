# Status Pages

Status pages, their sections and resources, subscribers (double opt-in),
status updates, and the public read surfaces.

## Pages

### GET /api/v1/orgs/:org/status-pages
List status pages. Auth: required

### POST /api/v1/orgs/:org/status-pages
Create a status page. Auth: required

### GET /api/v1/orgs/:org/status-pages/:statusPageUid
Get a status page. Auth: required

### PATCH /api/v1/orgs/:org/status-pages/:statusPageUid
Update a status page. Auth: required

### DELETE /api/v1/orgs/:org/status-pages/:statusPageUid
Delete a status page. Auth: required

## Sections

### GET /api/v1/orgs/:org/status-pages/:statusPageUid/sections
List sections of a status page. Auth: required

### POST /api/v1/orgs/:org/status-pages/:statusPageUid/sections
Create a section. Auth: required

### POST /api/v1/orgs/:org/status-pages/:statusPageUid/sections/reorder
Reorder the sections of a status page in one call (body carries the ordered
uid list). Auth: required

### GET /api/v1/orgs/:org/status-pages/:statusPageUid/sections/:sectionUid
Get a section. Auth: required

### PATCH /api/v1/orgs/:org/status-pages/:statusPageUid/sections/:sectionUid
Update a section. Auth: required

### DELETE /api/v1/orgs/:org/status-pages/:statusPageUid/sections/:sectionUid
Delete a section. Auth: required

## Resources

### GET /api/v1/orgs/:org/status-pages/:statusPageUid/sections/:sectionUid/resources
List resources in a section. Auth: required

### POST /api/v1/orgs/:org/status-pages/:statusPageUid/sections/:sectionUid/resources
Add a resource to a section. Auth: required

### POST /api/v1/orgs/:org/status-pages/:statusPageUid/sections/:sectionUid/resources/reorder
Reorder the resources within a section in one call. Auth: required

### PATCH /api/v1/orgs/:org/status-pages/:statusPageUid/sections/:sectionUid/resources/:resourceUid
Update a resource. Auth: required

### DELETE /api/v1/orgs/:org/status-pages/:statusPageUid/sections/:sectionUid/resources/:resourceUid
Remove a resource. Auth: required

## Subscribers

Subscription is **double opt-in**: anyone can request a subscription from the
public status page, but nothing is delivered until the emailed confirmation
link is followed. Double opt-in is the primary anti-abuse control, which is why
`POST …/subscribers` is public while listing and deleting are not.

### GET /api/v1/orgs/:org/status-pages/:statusPageUid/subscribers
List the subscribers of a status page. Auth: required

### POST /api/v1/orgs/:org/status-pages/:statusPageUid/subscribers
Request a subscription. Sends a confirmation email; creates nothing deliverable
until confirmed. Auth: **public**

### DELETE /api/v1/orgs/:org/status-pages/:statusPageUid/subscribers/:uid
Remove a subscriber. Auth: required

### GET /api/v1/public/status-subscribers/confirm
Confirm a subscription from the emailed link (signed token). Auth: public

### GET /api/v1/public/status-subscribers/unsubscribe
Unsubscribe from the emailed link (signed token). Auth: public

## Status updates

Human-written incident/maintenance narrative published on the status page. All
require auth.

### GET /api/v1/orgs/:org/status-updates
List status updates.

### POST /api/v1/orgs/:org/status-updates
Create a status update (notifies confirmed subscribers).

### GET /api/v1/orgs/:org/status-updates/:uid
Get a status update.

### PATCH /api/v1/orgs/:org/status-updates/:uid
Update a status update.

### DELETE /api/v1/orgs/:org/status-updates/:uid
Delete a status update.

## Public views

### GET /api/v1/status-pages/:org
View the default status page for an organization. Auth: public

### GET /api/v1/status-pages/:org/:slug
View a specific status page by slug. Auth: public

### GET /api/v1/status-pages/:org/:slug/feed.xml
RSS/Atom feed of the page's status updates. Auth: public
