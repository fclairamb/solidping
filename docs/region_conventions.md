# Region Naming Conventions

## Overview

Regions use a hierarchical format to enable flexible matching at different geographic levels.

## Format

```
$continent-$region-$city
```

Each component should use lowercase identifiers (e.g., `eu`, `fr`, `idf`, `paris`).

## Matching Patterns

The hierarchical structure supports wildcard matching at any level:

| Pattern | Matches |
|---------|---------|
| `$continent-*` | All locations in a continent |
| `$continent-$region-*` | All locations in a region |
| `$continent-$region-$city` | Specific city |

## Examples

| Region ID | Description |
|-----------|-------------|
| `eu-west-paris-1` | Paris, Île-de-France, France, Europe |
| `eu-west-munich-1` | Munich, Bavaria, Germany, Europe |
| `us-west-sf-1` | San Francisco, California, USA, North America |
| `as-jp-tokyo-1` | Tokyo, Japan, Asia |

### Matching Examples

- `eu-*` matches all European locations
- `eu-fr-*` matches all French locations
- `eu-fr-idf-*` matches all Île-de-France locations
- `eu-fr-idf-paris` matches only Paris
