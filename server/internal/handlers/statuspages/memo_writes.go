package statuspages

// The write choke points (spec 2026-09-22-09).
//
// Every status-page row write in this package goes through one of the wrappers
// below, and each wrapper performs the write and then evicts the page's
// memoized public view. No production code in this package may call
// `s.db.{Create,Update,Delete,Reorder}StatusPage…` directly —
// TestPageMemoWrites_NoDirectStatusPageWrites parses the package and fails,
// naming the function, if one does.
//
// # Why a choke point and not a habit
//
// The first version of this spec left the eviction as a second statement at
// each write site, and listed the sites in a table. That caught a site whose
// eviction was deleted; it did not catch `clearDefaultStatusPage`, which was
// already demoting OTHER pages' `isDefault` and evicting none of them — a write
// path nobody thought to put in the table, which is exactly the failure a table
// of remembered sites cannot see.
//
// A wrapper turns "remember to evict" into "you cannot write without saying
// which page this changes". The write path still has to name its page and its
// PageMemoWritePath, so a genuinely body-neutral write can still opt out — but
// it has to do so out loud, with WritePathNoPublicChange, rather than by
// forgetting.

import (
	"context"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// WritePathNoPublicChange marks a write that changes no byte of any public
// payload, so it deliberately evicts nothing.
//
// Today that is kiosk-token mint and revoke: the token's hash lives on the page
// row, and `HasKioskToken` is stripped from every public payload by
// resolvePublicBranding, precisely because whether a wallboard credential exists
// is operator information. Evicting here would throw away a hot page's view for
// a change no reader can observe.
//
// It is a named constant rather than an empty string so the choice is visible at
// the call site and greppable from here.
const WritePathNoPublicChange PageMemoWritePath = "statuspages.noPublicChange"

// evictAfterWrite evicts the page unless the write path declared itself
// body-neutral.
func (s *Service) evictAfterWrite(path PageMemoWritePath, pageUID string) {
	if path == WritePathNoPublicChange {
		return
	}

	s.invalidatePageMemo(path, pageUID)
}

// createStatusPageRow creates a page together with its default section and
// resources.
//
// A brand-new page has no memoized view to evict, so this one is a choke point
// for uniformity rather than for correctness: the rule "every write goes through
// a wrapper" is what the scan can check, and an exception would be the hole the
// next write path slips through.
func (s *Service) createStatusPageRow(
	ctx context.Context,
	path PageMemoWritePath,
	page *models.StatusPage,
	section *models.StatusPageSection,
	resources []*models.StatusPageResource,
) error {
	if err := s.db.CreateStatusPageWithDefaultSection(ctx, page, section, resources); err != nil {
		return err
	}

	s.evictAfterWrite(path, page.UID)

	return nil
}

// writeStatusPageRow patches a page row and evicts it.
func (s *Service) writeStatusPageRow(
	ctx context.Context, path PageMemoWritePath, pageUID string, update *models.StatusPageUpdate,
) error {
	if err := s.db.UpdateStatusPage(ctx, pageUID, update); err != nil {
		return err
	}

	s.evictAfterWrite(path, pageUID)

	return nil
}

// deleteStatusPageRow deletes a page and evicts it. The eviction is what stops a
// deleted page from rendering out of the memo for the rest of the TTL.
func (s *Service) deleteStatusPageRow(ctx context.Context, path PageMemoWritePath, pageUID string) error {
	if err := s.db.DeleteStatusPage(ctx, pageUID); err != nil {
		return err
	}

	s.evictAfterWrite(path, pageUID)

	return nil
}

// writeStatusPageCustomDomainRow patches a page's custom-domain state and
// evicts it: the summary's `page.url` is the verified custom domain when there
// is one.
func (s *Service) writeStatusPageCustomDomainRow(
	ctx context.Context, path PageMemoWritePath, pageUID string,
	update *models.StatusPageCustomDomainUpdate,
) error {
	if err := s.db.UpdateStatusPageCustomDomain(ctx, pageUID, update); err != nil {
		return err
	}

	s.evictAfterWrite(path, pageUID)

	return nil
}

// createStatusPageSectionRow inserts a section and evicts its page.
func (s *Service) createStatusPageSectionRow(
	ctx context.Context, path PageMemoWritePath, pageUID string, section *models.StatusPageSection,
) error {
	if err := s.db.CreateStatusPageSection(ctx, section); err != nil {
		return err
	}

	s.evictAfterWrite(path, pageUID)

	return nil
}

// writeStatusPageSectionRow patches a section and evicts its page.
func (s *Service) writeStatusPageSectionRow(
	ctx context.Context, path PageMemoWritePath, pageUID, sectionUID string,
	update *models.StatusPageSectionUpdate,
) error {
	if err := s.db.UpdateStatusPageSection(ctx, sectionUID, update); err != nil {
		return err
	}

	s.evictAfterWrite(path, pageUID)

	return nil
}

// deleteStatusPageSectionRow deletes a section and evicts its page.
func (s *Service) deleteStatusPageSectionRow(
	ctx context.Context, path PageMemoWritePath, pageUID, sectionUID string,
) error {
	if err := s.db.DeleteStatusPageSection(ctx, sectionUID); err != nil {
		return err
	}

	s.evictAfterWrite(path, pageUID)

	return nil
}

// reorderStatusPageSectionRows renumbers a page's sections and evicts it.
func (s *Service) reorderStatusPageSectionRows(
	ctx context.Context, path PageMemoWritePath, pageUID string, orderedUIDs []string,
) error {
	if err := s.db.ReorderStatusPageSections(ctx, pageUID, orderedUIDs); err != nil {
		return err
	}

	s.evictAfterWrite(path, pageUID)

	return nil
}

// createStatusPageResourceRow inserts a component and evicts its page.
func (s *Service) createStatusPageResourceRow(
	ctx context.Context, path PageMemoWritePath, pageUID string, resource *models.StatusPageResource,
) error {
	if err := s.db.CreateStatusPageResource(ctx, resource); err != nil {
		return err
	}

	s.evictAfterWrite(path, pageUID)

	return nil
}

// writeStatusPageResourceRow patches a component and evicts its page.
func (s *Service) writeStatusPageResourceRow(
	ctx context.Context, path PageMemoWritePath, pageUID, resourceUID string,
	update *models.StatusPageResourceUpdate,
) error {
	if err := s.db.UpdateStatusPageResource(ctx, resourceUID, update); err != nil {
		return err
	}

	s.evictAfterWrite(path, pageUID)

	return nil
}

// deleteStatusPageResourceRow deletes a component and evicts its page.
func (s *Service) deleteStatusPageResourceRow(
	ctx context.Context, path PageMemoWritePath, pageUID, resourceUID string,
) error {
	if err := s.db.DeleteStatusPageResource(ctx, resourceUID); err != nil {
		return err
	}

	s.evictAfterWrite(path, pageUID)

	return nil
}

// reorderStatusPageResourceRows renumbers a section's components and evicts the
// page.
func (s *Service) reorderStatusPageResourceRows(
	ctx context.Context, path PageMemoWritePath, pageUID, sectionUID string, orderedUIDs []string,
) error {
	if err := s.db.ReorderStatusPageResources(ctx, sectionUID, orderedUIDs); err != nil {
		return err
	}

	s.evictAfterWrite(path, pageUID)

	return nil
}

// PageMemoWriteWrappers are the only functions in this package allowed to call a
// status-page row write on s.db. The scan test reads this list, so adding a
// wrapper means adding it here — and a write path that reaches past them fails
// the test by name.
//
//nolint:gochecknoglobals // a declarative table, read-only after init
var PageMemoWriteWrappers = []string{
	"createStatusPageRow",
	"writeStatusPageRow",
	"deleteStatusPageRow",
	"writeStatusPageCustomDomainRow",
	"createStatusPageSectionRow",
	"writeStatusPageSectionRow",
	"deleteStatusPageSectionRow",
	"reorderStatusPageSectionRows",
	"createStatusPageResourceRow",
	"writeStatusPageResourceRow",
	"deleteStatusPageResourceRow",
	"reorderStatusPageResourceRows",
}
