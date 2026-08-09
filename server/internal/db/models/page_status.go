package models

// PageStatus is a status page's page-level rollup status (spec
// 2026-08-08-05). It is a distinct vocabulary from CheckStatus: it adds
// "maintenance" (no individual check has this concept) and gives "unknown" a
// real, honest meaning instead of silently collapsing into "operational".
type PageStatus string

// The page-level rollup vocabulary. Kept as string constants (rather than an
// int enum like CheckStatus) because this value is only ever produced at the
// read boundary and serialized straight onto the public wire — there is no
// storage representation to keep compact.
const (
	PageStatusOperational PageStatus = "operational"
	PageStatusDegraded    PageStatus = "degraded"
	PageStatusDown        PageStatus = "down"
	PageStatusMaintenance PageStatus = "maintenance"
	PageStatusUnknown     PageStatus = "unknown"
)

// PageResourceStatus is the minimal per-resource input RollupPageStatus
// needs: the resource's live check status (a check's own CheckStatus, or a
// check-group resource's already rolled-up status via RollupGroupStatus) and
// whether it is currently inside an active maintenance window.
type PageResourceStatus struct {
	Status        CheckStatus
	InMaintenance bool
}

// PageStatusCounts tallies how many resources landed in each PageStatus
// category. Per-resource classification applies the same maintenance-masking
// rule as the overall rollup: a resource in maintenance is counted as
// Maintenance regardless of its underlying check status.
type PageStatusCounts struct {
	Operational int
	Degraded    int
	Down        int
	Maintenance int
	Unknown     int
}

// RollupPageStatus computes a status page's overall status from the live
// status of every resource across every section of the page. It is pure and
// exported — no DB access, no request context — so spec 2026-08-08-06 (the
// public summary endpoint) and spec 2026-08-08-07 (the page-level SVG badge)
// can call it directly against the same resource list ViewStatusPage builds,
// without reimplementing the aggregation.
//
// Rules, evaluated in priority order over the whole resource list:
//  1. Resources currently in maintenance are excluded from the outage scan —
//     maintenance masks failures, so a resource that goes down *during* its
//     own maintenance window never taints the page.
//  2. Else any non-maintenance resource with CheckStatusDown -> PageStatusDown.
//  3. Else any resource with CheckStatusDegraded or CheckStatusWarning ->
//     PageStatusDegraded.
//  4. Else if any resource is in maintenance -> PageStatusMaintenance.
//  5. Else if no resource has a usable status (all CheckStatusCreated / no
//     resources at all) -> PageStatusUnknown.
//  6. Else -> PageStatusOperational.
func RollupPageStatus(resources []PageResourceStatus) (PageStatus, PageStatusCounts) {
	var counts PageStatusCounts

	var (
		hasDown        bool
		hasDegraded    bool
		hasMaintenance bool
		hasUsable      bool
	)

	for _, resource := range resources {
		switch {
		case resource.InMaintenance:
			counts.Maintenance++

			hasMaintenance = true
		case resource.Status == CheckStatusDown:
			counts.Down++

			hasDown = true
		case resource.Status == CheckStatusDegraded || resource.Status == CheckStatusWarning:
			counts.Degraded++

			hasDegraded = true
		case resource.Status == CheckStatusUp || resource.Status == CheckStatusValidating:
			counts.Operational++

			hasUsable = true
		default:
			// CheckStatusCreated, or any status not otherwise classified —
			// no usable data yet.
			counts.Unknown++
		}
	}

	switch {
	case hasDown:
		return PageStatusDown, counts
	case hasDegraded:
		return PageStatusDegraded, counts
	case hasMaintenance:
		return PageStatusMaintenance, counts
	case !hasUsable:
		return PageStatusUnknown, counts
	default:
		return PageStatusOperational, counts
	}
}
