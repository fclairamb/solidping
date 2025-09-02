# Frontend URL Conventions

## General Principle
Frontend URLs should mirror the API endpoint structure to maintain consistency and predictability across the application.

## URL Structure

### Pattern
```
/dash/orgs/{orgSlug}/[resource]/[resourceSlug]/[sub-resource]
```

### Conventions
- **Prefix**: All authenticated dashboard routes start with `/dashboard`
- **Organization scoping**: Routes are scoped to organizations using `/orgs/{orgSlug}`
- **Parameter naming**:
  - Use `{orgSlug}` for organization identifiers (not `{org}` or `{orgUid}`)
  - Use `{resourceSlug}` for resource identifiers (e.g., `{checkSlug}`, `{incidentSlug}`)
  - Slugs are human-readable identifiers, not UUIDs
- **Hierarchy**: URLs follow the resource hierarchy, matching API structure

## Examples

### Organization Routes
- `/dash/orgs/{orgSlug}` - Organization dashboard/overview
- `/dash/orgs/{orgSlug}/settings` - Organization settings

### Check Routes
- `/dash/orgs/{orgSlug}/checks` - List all checks for the organization
- `/dash/orgs/{orgSlug}/checks/{checkSlug}` - Check details and monitoring data
- `/dash/orgs/{orgSlug}/checks/{checkSlug}/edit` - Edit check configuration
- `/dash/orgs/{orgSlug}/checks/new` - Create new check

### Incident Routes
- `/dash/orgs/{orgSlug}/incidents` - List all incidents for the organization
- `/dash/orgs/{orgSlug}/incidents/{incidentUid}` - Incident details

### User/Auth Routes
- `/dash/orgs/{orgSlug}/users` - List organization users
- `/dash/orgs/{orgSlug}/users/{userUid}` - User profile/details
## Relationship to API Endpoints

Frontend routes mirror API structure but with differences:

| Frontend URL | API Endpoint |
|-------------|--------------|
| `/dash/orgs/{orgSlug}/checks` | `GET /api/v1/orgs/{org}/checks` |
| `/dash/orgs/{orgSlug}/checks/{checkSlug}` | `GET /api/v1/orgs/{org}/checks/{checkUid}` |
| `/dahs/orgs/{orgSlug}/incidents` | `GET /api/v1/orgs/{org}/incidents` |

**Key differences:**
- Frontend uses `/dashboard` prefix instead of `/api/v1`
- Frontend uses human-readable slugs (`{orgSlug}`, `{checkSlug}`)
- API uses technical identifiers (`{org}`, `{checkUid}`)

## Benefits
- **Consistency**: Predictable URL structure across the application
- **Discoverability**: Users and developers can guess URLs
- **SEO-friendly**: Human-readable slugs are better for search engines
- **Maintainability**: Clear mapping between frontend and backend resources
