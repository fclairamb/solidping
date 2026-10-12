package mcp

// Per-class tool-annotation constructors (MCP 2025-06-18). Every tool
// definition picks exactly one; together with the prose description they
// form the tool's behavior disclosure.
//
// openWorldHint is false everywhere but on the probe tools (probe and its
// legacy aliases run_js_script, fetch_page, browser_snapshot, spec
// 2026-10-03-07): every other tool
// operates on the SolidPing server's own data and never reaches out to an
// external entity on the caller's behalf.
//
// The split between the classes:
//
//	readOnly  — no writes at all; idempotent by definition.
//	create    — makes a new resource; repeating it makes ANOTHER one, so
//	            not idempotent, and nothing existing is destroyed.
//	update    — PATCH-style edit of one resource: omitted fields are kept,
//	            so the same call twice lands in the same state.
//	replace   — full-replacement write whose documented effect includes
//	            clearing a collection (an empty list removes everything it
//	            covers), so it is flagged destructive.
//	manage    — several actions under one name, one of them a delete:
//	            annotated for the worst one (destructive, not idempotent).
//	delete    — destructive by nature; repeating it has no ADDITIONAL
//	            effect (the second call finds nothing left to remove), so
//	            like HTTP DELETE it stays idempotent.
func readOnlyAnnotations(title string) *ToolAnnotations {
	return &ToolAnnotations{
		Title:           title,
		ReadOnlyHint:    true,
		DestructiveHint: false,
		IdempotentHint:  true,
		OpenWorldHint:   false,
	}
}

// probeAnnotations is a tool that writes nothing but reaches the target the
// caller names (a script run, a page fetch): open-world, and idempotent like
// every read (a repeat has no additional effect on SolidPing).
func probeAnnotations(title string) *ToolAnnotations {
	return &ToolAnnotations{
		Title:           title,
		ReadOnlyHint:    true,
		DestructiveHint: false,
		IdempotentHint:  true,
		OpenWorldHint:   true,
	}
}

func createAnnotations(title string) *ToolAnnotations {
	return &ToolAnnotations{
		Title:           title,
		ReadOnlyHint:    false,
		DestructiveHint: false,
		IdempotentHint:  false,
		OpenWorldHint:   false,
	}
}

func updateAnnotations(title string) *ToolAnnotations {
	return &ToolAnnotations{
		Title:           title,
		ReadOnlyHint:    false,
		DestructiveHint: false,
		IdempotentHint:  true,
		OpenWorldHint:   false,
	}
}

func replaceAnnotations(title string) *ToolAnnotations {
	return &ToolAnnotations{
		Title:           title,
		ReadOnlyHint:    false,
		DestructiveHint: true,
		IdempotentHint:  true,
		OpenWorldHint:   false,
	}
}

func deleteAnnotations(title string) *ToolAnnotations {
	return &ToolAnnotations{
		Title:           title,
		ReadOnlyHint:    false,
		DestructiveHint: true,
		IdempotentHint:  true,
		OpenWorldHint:   false,
	}
}

// manageAnnotations is a by-action write tool (manage_status_page_*, spec
// 2026-10-12-06) annotated for its WORST action: MCP 2025-06-18 has no
// per-action hints, and one of the actions deletes, so the tool says
// destructive; one of them creates, so it does not claim idempotency.
func manageAnnotations(title string) *ToolAnnotations {
	return &ToolAnnotations{
		Title:           title,
		ReadOnlyHint:    false,
		DestructiveHint: true,
		IdempotentHint:  false,
		OpenWorldHint:   false,
	}
}
