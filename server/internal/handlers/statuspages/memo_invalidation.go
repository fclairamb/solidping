package statuspages

// The invalidation table (spec 2026-09-22-09).
//
// The memo in memo.go holds a computed public view for statuspagecache.PageMemoTTL.
// That is acceptable for a check result flipping — the HTTP directive already
// promises a reader nothing fresher — and NOT acceptable for an operator's own
// edit: someone who renames a section and reloads the page has to see the new
// name, not a fifteen-second ghost of the old one.
//
// So every write that changes what a public read returns evicts the page. The
// risk is a write path that forgets, which fails silently and only in
// production, so the list of them is a TABLE here rather than a habit spread
// across four packages:
//
//   - Each write path has a PageMemoWritePath constant, and this package's own
//     write paths PASS that constant when they evict. The constant is therefore
//     referenced from the code, not just from a test.
//   - pageMemoWritePathSites records, for every constant, the file and function
//     that must carry the eviction. TestPageMemoInvalidation_EveryWritePathEvicts
//     parses those functions and fails when one of them no longer invalidates —
//     including the ones in other packages, which is where a refactor is most
//     likely to drop the call.
//   - The end-to-end table in memo_test.go warms the memo, performs the write
//     and asserts the next read recomputed.
//
// What this does NOT do is notice a write path nobody added a row for. That
// would need a whitelist of every mutating method in four packages, which rots
// faster than the thing it guards. The rule is "a new write path that changes
// the public body gets a row here"; the tests make a row that stops working
// impossible to miss.

// PageMemoWritePath names one write path that changes what a public
// status-page read returns. The value is the qualified function, so a failing
// test names something greppable.
type PageMemoWritePath string

// The status-page CRUD write paths, in this package.
const (
	// WritePathUpdateStatusPage covers every page-level setting a public read
	// reflects: name, visibility, history period, availability toggles,
	// branding, custom CSS.
	WritePathUpdateStatusPage PageMemoWritePath = "statuspages.UpdateStatusPage"
	// WritePathDeleteStatusPage evicts a page that no longer exists. Without it
	// a deleted page keeps rendering for up to the TTL.
	WritePathDeleteStatusPage PageMemoWritePath = "statuspages.DeleteStatusPage"
	// WritePathCreateSection adds a section to the public body.
	WritePathCreateSection PageMemoWritePath = "statuspages.CreateSection"
	// WritePathUpdateSection renames, repositions or re-selectors a section.
	WritePathUpdateSection PageMemoWritePath = "statuspages.UpdateSection"
	// WritePathDeleteSection removes a section and everything under it.
	WritePathDeleteSection PageMemoWritePath = "statuspages.DeleteSection"
	// WritePathReorderSections changes the order the sections render in.
	WritePathReorderSections PageMemoWritePath = "statuspages.ReorderSections"
	// WritePathCreateResource adds a component.
	WritePathCreateResource PageMemoWritePath = "statuspages.CreateResource"
	// WritePathUpdateResource changes a component's public name, explanation or
	// target.
	WritePathUpdateResource PageMemoWritePath = "statuspages.UpdateResource"
	// WritePathDeleteResource removes a component.
	WritePathDeleteResource PageMemoWritePath = "statuspages.DeleteResource"
	// WritePathReorderResources changes the order components render in.
	WritePathReorderResources PageMemoWritePath = "statuspages.ReorderResources"
	// WritePathSetCustomDomain points the page at a (new) custom domain. The
	// summary's page.url is the verified custom domain when there is one, so
	// this changes a public field even though nothing about the page's content
	// moved.
	WritePathSetCustomDomain PageMemoWritePath = "statuspages.setCustomDomain"
	// WritePathClearCustomDomain drops the custom domain, sending page.url back
	// to the path-based URL.
	WritePathClearCustomDomain PageMemoWritePath = "statuspages.clearCustomDomain"
	// WritePathVerifyCustomDomain is the synchronous Verify button: it is what
	// promotes a configured domain into page.url, and what demotes it back out.
	WritePathVerifyCustomDomain PageMemoWritePath = "statuspages.VerifyCustomDomain"
	// WritePathSelectorReconcile is selector materialization — the one write
	// path that is also a READ path (maybeReconcileOnView). It evicts only when
	// it actually wrote something, because the backstop runs on every view and
	// an unconditional eviction there would defeat the memo entirely.
	WritePathSelectorReconcile PageMemoWritePath = "statuspages.reconcilePage"
)

// The brand-asset write paths, in package statuspageassets. A page's public
// body carries its logo and favicon URLs through resolvePublicBranding, so an
// upload that does not evict leaves the old logo on the page.
const (
	// WritePathAssetUpload is a logo or favicon upload.
	WritePathAssetUpload PageMemoWritePath = "statuspageassets.Upload"
	// WritePathAssetClear removes a logo or favicon.
	WritePathAssetClear PageMemoWritePath = "statuspageassets.Clear"
)

// The incident-publication write paths, in package incidentpublications. These
// drive activeIncidents[] and the banner — the part of a status page a reader
// is looking for during the one minute it matters, which is exactly when a
// stale copy is least forgivable.
const (
	// WritePathCreatePublication is an operator publishing an incident.
	WritePathCreatePublication PageMemoWritePath = "incidentpublications.CreatePublication"
	// WritePathUpdatePublication edits a published incident's title, body or
	// impact.
	WritePathUpdatePublication PageMemoWritePath = "incidentpublications.UpdatePublication"
	// WritePathAppendUpdate posts an update onto a publication's thread.
	WritePathAppendUpdate PageMemoWritePath = "incidentpublications.AppendUpdate"
	// WritePathPublishIncident publishes an incident onto a page.
	WritePathPublishIncident PageMemoWritePath = "incidentpublications.PublishIncident"
	// WritePathUnpublishIncident withdraws a publication.
	WritePathUnpublishIncident PageMemoWritePath = "incidentpublications.UnpublishIncident"
	// WritePathResolveRetroactively publishes an already-resolved incident.
	WritePathResolveRetroactively PageMemoWritePath = "incidentpublications.resolveRetroactively"
	// WritePathAutoPublish is the auto-publish job.
	WritePathAutoPublish PageMemoWritePath = "incidentpublications.AutoPublish"
	// WritePathApplyResolvePolicy is the auto-resolve path.
	WritePathApplyResolvePolicy PageMemoWritePath = "incidentpublications.applyResolvePolicy"
	// WritePathOnIncidentReopened re-opens a publication whose incident came
	// back.
	WritePathOnIncidentReopened PageMemoWritePath = "incidentpublications.OnIncidentReopened"
	// WritePathPostUpdate is the status-update row a publication posts onto the
	// page's timeline.
	WritePathPostUpdate PageMemoWritePath = "incidentpublications.postUpdate"
)

// The status-update write paths, in package statusupdates — the page's
// recentUpdates timeline.
const (
	// WritePathCreateStatusUpdate posts a status update.
	WritePathCreateStatusUpdate PageMemoWritePath = "statusupdates.CreateStatusUpdate"
	// WritePathUpdateStatusUpdate edits one.
	WritePathUpdateStatusUpdate PageMemoWritePath = "statusupdates.UpdateStatusUpdate"
	// WritePathDeleteStatusUpdate removes one.
	WritePathDeleteStatusUpdate PageMemoWritePath = "statusupdates.DeleteStatusUpdate"
)

// pageMemoWritePathSite locates the code that must evict for one write path.
type pageMemoWritePathSite struct {
	// Path is the write path this row is about.
	Path PageMemoWritePath
	// File is the source file, relative to the server module root.
	File string
	// Func is the function (method or free function) whose body must contain an
	// eviction call.
	Func string
}

// The files the table points at. Named constants because the same path appears
// on every row of a package's block, and because a typo in one of them is a row
// the completeness test can no longer check.
const (
	fileStatusPagesService = "internal/handlers/statuspages/service.go"
	fileStatusPagesDomain  = "internal/handlers/statuspages/custom_domain.go"
	fileStatusPagesSel     = "internal/handlers/statuspages/selector.go"
	fileAssetsService      = "internal/handlers/statuspageassets/service.go"
	filePublicationsSvc    = "internal/handlers/incidentpublications/service.go"
	filePublicationsPolicy = "internal/handlers/incidentpublications/policy.go"
	fileStatusUpdatesSvc   = "internal/handlers/statusupdates/service.go"
)

// PageMemoWritePaths is THE list of write paths that evict the view memo, each
// with the file and function that must carry the eviction. Ordered as the spec
// lists them: this package, then assets, publications and status updates.
//
//nolint:gochecknoglobals // a declarative table, read-only after init
var PageMemoWritePaths = []pageMemoWritePathSite{
	{WritePathUpdateStatusPage, fileStatusPagesService, "UpdateStatusPage"},
	{WritePathDeleteStatusPage, fileStatusPagesService, "DeleteStatusPage"},
	{WritePathCreateSection, fileStatusPagesService, "CreateSection"},
	{WritePathUpdateSection, fileStatusPagesService, "UpdateSection"},
	{WritePathDeleteSection, fileStatusPagesService, "DeleteSection"},
	{WritePathReorderSections, fileStatusPagesService, "ReorderSections"},
	{WritePathCreateResource, fileStatusPagesService, "CreateResource"},
	{WritePathUpdateResource, fileStatusPagesService, "UpdateResource"},
	{WritePathDeleteResource, fileStatusPagesService, "DeleteResource"},
	{WritePathReorderResources, fileStatusPagesService, "ReorderResources"},
	{WritePathSetCustomDomain, fileStatusPagesDomain, "setCustomDomain"},
	{WritePathClearCustomDomain, fileStatusPagesDomain, "clearCustomDomain"},
	{WritePathVerifyCustomDomain, fileStatusPagesDomain, "VerifyCustomDomain"},
	{WritePathSelectorReconcile, fileStatusPagesSel, "reconcilePage"},

	{WritePathAssetUpload, fileAssetsService, "Upload"},
	{WritePathAssetClear, fileAssetsService, "Clear"},

	{WritePathCreatePublication, filePublicationsSvc, "CreatePublication"},
	{WritePathUpdatePublication, filePublicationsSvc, "UpdatePublication"},
	{WritePathAppendUpdate, filePublicationsSvc, "AppendUpdate"},
	{WritePathPublishIncident, filePublicationsSvc, "PublishIncident"},
	{WritePathUnpublishIncident, filePublicationsSvc, "UnpublishIncident"},
	{WritePathResolveRetroactively, filePublicationsSvc, "resolveRetroactively"},
	{WritePathPostUpdate, filePublicationsSvc, "postUpdate"},
	{WritePathAutoPublish, filePublicationsPolicy, "AutoPublish"},
	{WritePathApplyResolvePolicy, filePublicationsPolicy, "applyResolvePolicy"},
	{WritePathOnIncidentReopened, filePublicationsPolicy, "OnIncidentReopened"},

	{WritePathCreateStatusUpdate, fileStatusUpdatesSvc, "CreateStatusUpdate"},
	{WritePathUpdateStatusUpdate, fileStatusUpdatesSvc, "UpdateStatusUpdate"},
	{WritePathDeleteStatusUpdate, fileStatusUpdatesSvc, "DeleteStatusUpdate"},
}

// PageMemoEvictionCalls are the method names that count as an eviction when the
// completeness test scans a write path's body. Three names because the call
// looks different depending on where it is made: this package evicts through
// its own helper (naming the write path), the other packages through the
// injected PageMemoInvalidator.
//
//nolint:gochecknoglobals // a declarative table, read-only after init
var PageMemoEvictionCalls = []string{
	"invalidatePageMemo",
	"Invalidate",
	"InvalidateOrg",
	// incidentpublications pairs each publication row write with its eviction in
	// one helper, so the write path calls the helper rather than the eviction.
	// Naming them here keeps the scan honest without asking that package to
	// un-pair what is better paired.
	"createPublicationRow",
	"updatePublicationRow",
	"softDeletePublicationRow",
	"createPageStatusUpdate",
}

// invalidatePageMemo evicts one page's memoized views.
//
// It takes the write path purely so the call site names its row in
// PageMemoWritePaths: the parameter is unused at runtime, and that is the
// point — it costs one identifier at each call site and buys a grep from the
// table to the code that honors it.
func (s *Service) invalidatePageMemo(_ PageMemoWritePath, pageUID string) {
	s.memo.invalidate(pageUID)
}

// Invalidate implements PageMemoInvalidator for the other packages' write
// paths: they know the page UID and nothing about this package's internals.
func (s *Service) Invalidate(pageUID string) {
	s.memo.invalidate(pageUID)
}

// InvalidateOrg evicts every memoized view in an organization. For a write path
// that knows something changed but cannot resolve which page it changed —
// coarser than Invalidate, and still bounded by one organization.
func (s *Service) InvalidateOrg(orgUID string) {
	s.memo.invalidateOrg(orgUID)
}
