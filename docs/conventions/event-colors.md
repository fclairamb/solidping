# Event Color Conventions

Color assignments for events displayed in the dashboard, following monitoring industry conventions.

## Color Mapping

### Check Events

| Event | Color | Class | Meaning |
|---|---|---|---|
| `check.created` | Green | `text-green-500` | New resource |
| `check.updated` | Blue | `text-blue-400` | Routine change |
| `check.deleted` | Orange | `text-orange-500` | Destructive action |

### Incident Events

| Event | Color | Class | Meaning |
|---|---|---|---|
| `incident.created` | Red | `text-red-500` | Something is broken |
| `incident.escalated` | Dark red | `text-red-700` | Getting worse |
| `incident.acknowledged` | Yellow | `text-yellow-500` | Noticed, not yet fixed |
| `incident.resolved` | Green | `text-green-500` | Crisis over |
| `incident.reopened` | Orange | `text-orange-500` | Regression |

## Principles

1. **Red** is reserved for active incidents — never use for routine actions
2. **Green** means resolution or positive creation
3. **Blue** is informational/neutral
4. **Orange** signals caution (deletions, regressions)
5. **Yellow** indicates an in-progress/acknowledged state
