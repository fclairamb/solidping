# Command Palette: "New Check" shortcut

## Goal

Add a **New Check** entry to the Command Palette so users can quickly navigate to the check creation form without leaving the keyboard.

## Behaviour

- Entry label: **"New Check"**
- Appears in the Command Palette regardless of the current page, as long as the user is inside an org context (i.e. `$org` is available in the route).
- Selecting the entry navigates to `/:org/checks/new`.
- The entry should include a suitable icon (e.g. `Plus` or `PlusCircle`) and a short description such as "Create a new monitoring check".

## Out of scope

- No changes to the check creation form itself.
- No keyboard shortcut beyond the palette entry.
