package regionsweep

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/regionoutage"
	"github.com/fclairamb/solidping/server/internal/regions"
)

// Notice limits.
const (
	// maxListedChecks caps the checks named in one email. An org with 400
	// blind checks needs to know it is 400, not to scroll through them.
	maxListedChecks = 20
	// maxEventCheckUIDs caps the check UIDs carried in the event payload.
	maxEventCheckUIDs = 100
	// noticeTimeLayout is how a timestamp reads in a notice.
	noticeTimeLayout = "2006-01-02 15:04 UTC"
)

// Payload keys of the region.offline / region.recovered events.
const (
	payloadRegion        = "region"
	payloadRegionName    = "regionName"
	payloadSince         = "since"
	payloadBlindChecks   = "blindChecks"
	payloadReducedChecks = "reducedChecks"
	payloadCheckUIDs     = "checkUids"
	payloadRecoveredAt   = "recoveredAt"
	payloadDurationSecs  = "durationSeconds"
)

// sweptCheck is the narrow projection of a check the org notice needs.
type sweptCheck struct {
	UID             string `bun:"uid"`
	OrganizationUID string `bun:"organization_uid"`
	Name            string `bun:"name"`
	Slug            string `bun:"slug"`
	Internal        bool   `bun:"internal"`
	Enabled         bool   `bun:"enabled"`
}

// orgImpact is what one dark region does to one org.
type orgImpact struct {
	orgUID  string
	blind   []sweptCheck
	reduced int
}

// orgNotice is one org told about an outage.
type orgNotice struct {
	orgUID string
	blind  int
}

// orgUIDsOf extracts the org UIDs of a set of notices.
func orgUIDsOf(notices []orgNotice) []string {
	out := make([]string, 0, len(notices))
	for i := range notices {
		out = append(out, notices[i].orgUID)
	}

	return out
}

// classifier answers "which regions does this check run from, and is each of
// them dark after this sweep?" for the whole sweep.
type classifier struct {
	// regionsByCheck is every job region of every region-pinned check.
	regionsByCheck map[string][]string
	// orgByCheck is the org owning each check (from its jobs).
	orgByCheck map[string]string
	// darkCloud is the set of cloud regions dark after this sweep.
	darkCloud map[string]bool
	// privateLive is live workers per (org slug, `@` slug) private row.
	privateLive map[string]int
	// orgSlugs resolves an org UID to its slug, for the private lookup.
	orgSlugs map[string]string
}

// newClassifier indexes the sweep's jobs and region states.
func (r *sweepRun) newClassifier(ctx context.Context) (*classifier, error) {
	if r.classifier != nil {
		return r.classifier, nil
	}

	cls := &classifier{
		regionsByCheck: make(map[string][]string),
		orgByCheck:     make(map[string]string),
		darkCloud:      make(map[string]bool),
		privateLive:    make(map[string]int),
		orgSlugs:       make(map[string]string),
	}

	for i := range r.jobs {
		job := &r.jobs[i]
		cls.regionsByCheck[job.CheckUID] = append(cls.regionsByCheck[job.CheckUID], job.Region)
		cls.orgByCheck[job.CheckUID] = job.OrganizationUID
	}

	for _, state := range r.states {
		if state.isDarkAfter() {
			cls.darkCloud[state.slug] = true
		}
	}

	hasPrivate := false

	for i := range r.report.Regions {
		row := &r.report.Regions[i]
		if isCloudRow(row) {
			continue
		}

		hasPrivate = true
		cls.privateLive[row.Organization+"/"+row.Slug] = row.LiveWorkers
	}

	if hasPrivate {
		if err := cls.loadOrgSlugs(ctx, r); err != nil {
			return nil, err
		}
	}

	r.classifier = cls

	return cls, nil
}

// loadOrgSlugs resolves every job's org to its slug, the name RegionHealth
// keys private rows by.
func (c *classifier) loadOrgSlugs(ctx context.Context, r *sweepRun) error {
	uids := make(map[string]bool)
	for _, orgUID := range c.orgByCheck {
		uids[orgUID] = true
	}

	if len(uids) == 0 {
		return nil
	}

	list := make([]string, 0, len(uids))
	for uid := range uids {
		list = append(list, uid)
	}

	var rows []struct {
		UID  string `bun:"uid"`
		Slug string `bun:"slug"`
	}

	if err := r.deps.DB.DB().NewSelect().
		TableExpr("organizations").
		ColumnExpr("uid, slug").
		Where("uid IN (?)", bun.List(list)).
		Scan(ctx, &rows); err != nil {
		return fmt.Errorf("list organization slugs for region sweep: %w", err)
	}

	for i := range rows {
		c.orgSlugs[rows[i].UID] = rows[i].Slug
	}

	return nil
}

// regionDark reports whether one of a check's job regions is out of service
// after this sweep. Cloud: the sweep's own verdict. Private: no live agent —
// used only to tell blind from reduced, never reported by this sweep.
func (c *classifier) regionDark(orgUID, region string) bool {
	if !regions.IsPrivateRegion(region) {
		return c.darkCloud[region]
	}

	slug := c.orgSlugs[orgUID]
	if slug == "" {
		slug = orgUID
	}

	return c.privateLive[slug+"/"+region] == 0
}

// blind reports whether every job region of a check is out of service.
func (c *classifier) blind(checkUID string) bool {
	orgUID := c.orgByCheck[checkUID]

	for _, region := range c.regionsByCheck[checkUID] {
		if !c.regionDark(orgUID, region) {
			return false
		}
	}

	return true
}

// impacts classifies every check with a job in the region, per org. Internal,
// disabled and deleted checks are left out: nobody is waiting on them.
func (r *sweepRun) impacts(ctx context.Context, region string) ([]orgImpact, error) {
	cls, err := r.newClassifier(ctx)
	if err != nil {
		return nil, err
	}

	uids := make([]string, 0)

	for i := range r.jobs {
		if r.jobs[i].Region == region {
			uids = append(uids, r.jobs[i].CheckUID)
		}
	}

	if len(uids) == 0 {
		return nil, nil
	}

	var rows []sweptCheck

	if err := r.deps.DB.DB().NewSelect().
		TableExpr("checks").
		ColumnExpr("uid, organization_uid, name, slug, internal, enabled").
		Where("uid IN (?)", bun.List(uids)).
		Where("deleted_at IS NULL").
		Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("list checks of region %s: %w", region, err)
	}

	byOrg := make(map[string]*orgImpact)

	for i := range rows {
		check := rows[i]
		if check.Internal || !check.Enabled {
			continue
		}

		impact := byOrg[check.OrganizationUID]
		if impact == nil {
			impact = &orgImpact{orgUID: check.OrganizationUID}
			byOrg[check.OrganizationUID] = impact
		}

		if cls.blind(check.UID) {
			impact.blind = append(impact.blind, check)
		} else {
			impact.reduced++
		}
	}

	out := make([]orgImpact, 0, len(byOrg))

	for _, impact := range byOrg {
		sort.Slice(impact.blind, func(i, j int) bool {
			return strings.ToLower(impact.blind[i].Name) < strings.ToLower(impact.blind[j].Name)
		})
		out = append(out, *impact)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].orgUID < out[j].orgUID })

	return out, nil
}

// orgOfflineNotices tells every org with a blind check, and not yet told,
// that the region is offline. It returns the orgs it told.
func (r *sweepRun) orgOfflineNotices(
	ctx context.Context, state *regionState, marker *regionoutage.Marker,
) ([]orgNotice, error) {
	impacts, err := r.impacts(ctx, state.slug)
	if err != nil {
		return nil, err
	}

	notices := make([]orgNotice, 0, len(impacts))
	name := r.regionDisplayName(ctx, state.slug)

	for i := range impacts {
		impact := &impacts[i]
		if len(impact.blind) == 0 || marker.HasNotified(impact.orgUID) {
			continue
		}

		r.sendOffline(ctx, state.slug, name, marker.Since, impact)
		notices = append(notices, orgNotice{orgUID: impact.orgUID, blind: len(impact.blind)})
	}

	return notices, nil
}

// sendOffline writes the org's region.offline event and emails its owners
// and admins. Every failure is logged and swallowed: one org's mail must not
// stop the others from being told, nor stall the sweep.
func (r *sweepRun) sendOffline(ctx context.Context, region, name string, since time.Time, impact *orgImpact) {
	checkUIDs := make([]any, 0, min(len(impact.blind), maxEventCheckUIDs))
	for i := range impact.blind {
		if i >= maxEventCheckUIDs {
			break
		}

		checkUIDs = append(checkUIDs, impact.blind[i].UID)
	}

	r.recordEvent(ctx, impact.orgUID, models.EventTypeRegionOffline, models.JSONMap{
		payloadRegion:        region,
		payloadRegionName:    name,
		payloadSince:         since.UTC().Format(time.RFC3339),
		payloadBlindChecks:   len(impact.blind),
		payloadReducedChecks: impact.reduced,
		payloadCheckUIDs:     checkUIDs,
	})

	org := r.loadOrg(ctx, impact.orgUID)
	if org == nil {
		return
	}

	listed := impact.blind
	if len(listed) > maxListedChecks {
		listed = listed[:maxListedChecks]
	}

	items := make([]map[string]any, 0, len(listed))
	for i := range listed {
		items = append(items, map[string]any{
			"Name": checkName(&listed[i]),
			"URL":  r.dashboardURL(org.Slug, "checks/"+listed[i].UID),
		})
	}

	data := map[string]any{
		"RegionName":   name,
		"Since":        since.UTC().Format(noticeTimeLayout),
		"BlindCount":   len(impact.blind),
		"ReducedCount": impact.reduced,
		"MoreCount":    len(impact.blind) - len(listed),
		"Checks":       items,
		"ChecksURL":    r.dashboardURL(org.Slug, "checks"),
	}

	r.mailAdmins(ctx, org, email.TemplateRegionOffline, data)
}

// orgRecoveredNotices tells exactly the orgs on record that the outage is
// over. It returns the orgs it told.
func (r *sweepRun) orgRecoveredNotices(
	ctx context.Context, state *regionState, marker *regionoutage.Marker,
) []string {
	if len(marker.NotifiedOrgs) == 0 {
		return nil
	}

	name := r.regionDisplayName(ctx, state.slug)
	duration := r.now.Sub(marker.Since)
	// Back online vs. nothing left to serve: a region whose checks were all
	// migrated away "recovers" with no live worker.
	moved := state.row == nil || state.row.LiveWorkers == 0

	told := make([]string, 0, len(marker.NotifiedOrgs))

	for _, orgUID := range marker.NotifiedOrgs {
		r.recordEvent(ctx, orgUID, models.EventTypeRegionRecovered, models.JSONMap{
			payloadRegion:       state.slug,
			payloadRegionName:   name,
			payloadSince:        marker.Since.UTC().Format(time.RFC3339),
			payloadRecoveredAt:  r.now.UTC().Format(time.RFC3339),
			payloadDurationSecs: int64(duration / time.Second),
		})

		told = append(told, orgUID)

		org := r.loadOrg(ctx, orgUID)
		if org == nil {
			continue
		}

		r.mailAdmins(ctx, org, email.TemplateRegionRecovered, map[string]any{
			"RegionName":  name,
			"Since":       marker.Since.UTC().Format(noticeTimeLayout),
			"RecoveredAt": r.now.UTC().Format(noticeTimeLayout),
			"Duration":    humanDuration(duration),
			"Moved":       moved,
			"ChecksURL":   r.dashboardURL(org.Slug, "checks"),
		})
	}

	return told
}

// recordEvent writes one org-scoped audit event.
func (r *sweepRun) recordEvent(ctx context.Context, orgUID string, eventType models.EventType, payload models.JSONMap) {
	event := models.NewEvent(orgUID, eventType, models.ActorTypeSystem)
	event.Payload = payload

	if err := r.deps.DB.CreateEvent(ctx, event); err != nil {
		r.deps.Logger.ErrorContext(ctx, "Failed to record a region outage event",
			"organization_uid", orgUID, "eventType", string(eventType), "error", err)
	}
}

// loadOrg reads an org, nil (and logged) when it cannot.
func (r *sweepRun) loadOrg(ctx context.Context, orgUID string) *models.Organization {
	org, err := r.deps.DB.GetOrganization(ctx, orgUID)
	if err != nil || org == nil {
		r.deps.Logger.WarnContext(ctx, "Region outage notice: cannot load the organization",
			"organization_uid", orgUID, "error", err)

		return nil
	}

	return org
}

// emailJobConfig is the subset of jobtypes.EmailJobConfig this package
// writes. Declared here because jobtypes imports this package.
type emailJobConfig struct {
	To           []string       `json:"to"`
	Template     string         `json:"template,omitempty"`
	TemplateData map[string]any `json:"templateData,omitempty"`
}

// mailAdmins queues one email per owner/admin of the org — the
// custom-domain demotion pattern (customdomain.mailDemoted).
func (r *sweepRun) mailAdmins(ctx context.Context, org *models.Organization, template string, data map[string]any) {
	if r.deps.Jobs == nil {
		return
	}

	members, err := r.deps.DB.ListMembersByOrg(ctx, org.UID)
	if err != nil {
		r.deps.Logger.WarnContext(ctx, "Region outage notice: cannot list the organization's admins",
			"organization_uid", org.UID, "error", err)

		return
	}

	email.ApplyOrgBranding(data, org.Name, org.Slug, org.LogoURL)

	for _, member := range members {
		if !member.Role.AtLeast(models.MemberRoleAdmin) || member.User == nil || member.User.Email == "" {
			continue
		}

		raw, marshalErr := json.Marshal(emailJobConfig{
			To:       []string{member.User.Email},
			Template: template,
			// Transactional operator mail: an admin cannot opt out of being
			// told their checks stopped running.
			TemplateData: data,
		})
		if marshalErr != nil {
			r.deps.Logger.ErrorContext(ctx, "Failed to marshal a region outage email", "error", marshalErr)

			return
		}

		if _, jobErr := r.deps.Jobs.CreateJob(ctx, org.UID, string(jobdef.JobTypeEmail), raw, nil); jobErr != nil {
			r.deps.Logger.ErrorContext(ctx, "Failed to enqueue a region outage email",
				"organization_uid", org.UID, "error", jobErr)
		}
	}
}

// dashboardURL builds a dash0 link under one org, empty without a base URL.
func (r *sweepRun) dashboardURL(orgSlug, path string) string {
	if r.deps.BaseURL == "" {
		return ""
	}

	return fmt.Sprintf("%s%s/orgs/%s/%s", strings.TrimRight(r.deps.BaseURL, "/"),
		config.DashboardBasePath, orgSlug, path)
}

// regionDisplayName is "Name (slug)" for a declared region with a name, the
// bare slug otherwise.
func (r *sweepRun) regionDisplayName(ctx context.Context, slug string) string {
	defs, err := regions.NewService(r.deps.DB).GetGlobalRegions(ctx)
	if err != nil {
		return slug
	}

	for i := range defs {
		if defs[i].Slug == slug && defs[i].Name != "" && defs[i].Name != slug {
			return fmt.Sprintf("%s (%s)", defs[i].Name, slug)
		}
	}

	return slug
}

// checkName is how a check reads in a notice.
func checkName(check *sweptCheck) string {
	if check.Name != "" {
		return check.Name
	}

	return check.Slug
}

// humanDuration renders an outage length at a glance: "7h52m", "12m".
func humanDuration(d time.Duration) string {
	if d < time.Minute {
		return d.Round(time.Second).String()
	}

	d = d.Round(time.Minute)
	hours := int(d / time.Hour)
	minutes := int((d % time.Hour) / time.Minute)

	switch {
	case hours >= 24:
		return fmt.Sprintf("%dd%dh%dm", hours/24, hours%24, minutes)
	case hours > 0:
		return fmt.Sprintf("%dh%dm", hours, minutes)
	default:
		return fmt.Sprintf("%dm", minutes)
	}
}
