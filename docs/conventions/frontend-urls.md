# Frontend URL Conventions

## General Principle
Frontend URLs mirror the API endpoint structure for consistency and predictability.

## URL Structure

### Pattern
```
/orgs/{org}/[resource]/[resourceUid]/[sub-resource]
```

### Conventions
- **Organization scoping**: Routes are scoped to organizations using `/orgs/{org}`
- **Parameter naming**: Uses `$org`, `$checkUid`, `$statusPageUid`, `$incidentUid`, `$token`
- **Routing**: TanStack Router with file-based routing (convention over configuration)
- **Route files**: `web/dash0/src/routes/` and `web/status0/src/routes/`

## Dashboard Routes (dash0)

### Public Routes (no auth)
| Path | Description |
|------|-------------|
| `/login` | User login |
| `/forgot-password` | Password reset request |
| `/no-org` | No organization error page |
| `/invite/$token` | Accept invitation |
| `/confirm-registration/$token` | Email confirmation |
| `/reset-password/$token` | Password reset with token |

### Organization Routes
| Path | Description |
|------|-------------|
| `/orgs/$org/` | Organization home/dashboard |
| `/orgs/$org/login` | Organization-level login |
| `/orgs/$org/register` | Organization-level registration |
| `/orgs/$org/badges` | Badge management |
| `/orgs/$org/events` | Event viewer |

### Check Routes
| Path | Description |
|------|-------------|
| `/orgs/$org/checks/` | Checks list |
| `/orgs/$org/checks/new` | Create new check |
| `/orgs/$org/checks/$checkUid/` | Check details |
| `/orgs/$org/checks/$checkUid/edit` | Edit check |

### Incident Routes
| Path | Description |
|------|-------------|
| `/orgs/$org/incidents/` | Incidents list |
| `/orgs/$org/incidents/$incidentUid` | Incident details |

### Account Routes
| Path | Description |
|------|-------------|
| `/orgs/$org/account/` | Account overview |
| `/orgs/$org/account/profile` | User profile settings |
| `/orgs/$org/account/tokens` | API token management |

### Organization Settings Routes
| Path | Description |
|------|-------------|
| `/orgs/$org/organization/` | Organization overview |
| `/orgs/$org/organization/settings` | Organization settings |
| `/orgs/$org/organization/invitations` | User invitations |

### Server Settings Routes (super admin)
| Path | Description |
|------|-------------|
| `/orgs/$org/server/` | Server settings overview |
| `/orgs/$org/server/auth` | Authentication settings |
| `/orgs/$org/server/mail` | Email/mail settings |
| `/orgs/$org/server/performance` | Performance settings |
| `/orgs/$org/server/web` | Web monitoring settings |

### Status Pages Routes
| Path | Description |
|------|-------------|
| `/orgs/$org/status-pages/` | Status pages list |
| `/orgs/$org/status-pages/new` | Create new status page |
| `/orgs/$org/status-pages/$statusPageUid/` | Status page view |
| `/orgs/$org/status-pages/$statusPageUid/edit` | Edit status page |

### Test Routes (dev/test mode)
| Path | Description |
|------|-------------|
| `/orgs/$org/test/` | Testing overview |
| `/orgs/$org/test/bulk` | Bulk test operations |
| `/orgs/$org/test/generate` | Generate test data |
| `/orgs/$org/test/reset` | Reset test data |
| `/orgs/$org/test/templates` | Test templates |

## Status Page Routes (status0)

| Path | Description |
|------|-------------|
| `/` | Main index |
| `/$org` | Organization status page |
| `/$org/$slug` | Specific status page by slug |

## Frontend-to-API Mapping

| Frontend URL | API Endpoint |
|-------------|--------------|
| `/orgs/$org/checks` | `GET /api/v1/orgs/:org/checks` |
| `/orgs/$org/checks/$checkUid` | `GET /api/v1/orgs/:org/checks/:checkUid` |
| `/orgs/$org/incidents` | `GET /api/v1/orgs/:org/incidents` |
| `/orgs/$org/incidents/$incidentUid` | `GET /api/v1/orgs/:org/incidents/:uid` |
| `/orgs/$org/status-pages` | `GET /api/v1/orgs/:org/status-pages` |

**Key differences:**
- Frontend uses `$` prefix for path parameters (TanStack Router convention)
- API uses `:` prefix for path parameters (bunrouter convention)
- Both use UIDs for resource identification
