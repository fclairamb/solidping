package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/orgslug"
)

// Organization-profile errors.
var (
	// ErrInvalidOrgName is returned when the requested name is empty or too long.
	ErrInvalidOrgName = errors.New("organization name is invalid")
	// ErrInvalidLogoURL is returned when the requested logo URL is not an
	// absolute http(s) URL of a reasonable length.
	ErrInvalidLogoURL = errors.New("organization logo URL is invalid")
)

// Profile field limits. The name bound is a sanity cap, not a product rule; the
// logo bound keeps a pathological data: URI out of the column.
const (
	maxOrgNameLength = 100
	maxLogoURLLength = 2048
)

// UpdateOrgProfileRequest is the body of PATCH /api/v1/orgs/:org.
//
// Every field is optional with PATCH semantics, which forces a distinction JSON
// pointers alone cannot express: "logoUrl absent" (leave it alone) versus
// "logoUrl: null" (clear it). The custom UnmarshalJSON records presence per
// field, so LogoURLSet is what decides whether the column is touched at all.
type UpdateOrgProfileRequest struct {
	Name    *string
	Slug    *string
	LogoURL *string
	// LogoURLSet reports that the request carried a logoUrl key at all. With
	// LogoURL nil (or empty) it means "clear the logo".
	LogoURLSet bool
}

// UnmarshalJSON decodes the request while tracking which keys were present.
func (r *UpdateOrgProfileRequest) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("decode org profile request: %w", err)
	}

	decode := func(key string) (*string, bool, error) {
		value, present := raw[key]
		if !present {
			return nil, false, nil
		}

		var parsed *string
		if err := json.Unmarshal(value, &parsed); err != nil {
			return nil, true, fmt.Errorf("decode %q: %w", key, err)
		}

		return parsed, true, nil
	}

	var err error
	if r.Name, _, err = decode("name"); err != nil {
		return err
	}

	if r.Slug, _, err = decode("slug"); err != nil {
		return err
	}

	if r.LogoURL, r.LogoURLSet, err = decode("logoUrl"); err != nil {
		return err
	}

	return nil
}

// OrgProfileResponse is the updated organization profile.
//
// When the slug changed it also carries a fresh org-scoped session: the caller's
// current access token has the OLD slug in its orgSlug claim, and
// AuthorizeOrgAccess compares that claim with the URL, so without a re-mint the
// very next request to the renamed org would 403. Other sessions self-heal on
// their next Refresh, which derives the claim from the org row by UID.
type OrgProfileResponse struct {
	UID     string  `json:"uid"`
	Slug    string  `json:"slug"`
	Name    string  `json:"name"`
	LogoURL *string `json:"logoUrl"`
	// PreviousSlug is the slug that was just freed; it keeps redirecting to
	// Slug until another organization claims it. Empty when the slug did not
	// change.
	PreviousSlug string `json:"previousSlug,omitempty"`
	AccessToken  string `json:"accessToken,omitempty"`
	RefreshToken string `json:"refreshToken,omitempty"`
	ExpiresIn    int    `json:"expiresIn,omitempty"`
	TokenType    string `json:"tokenType,omitempty"`
}

// UpdateOrgProfile changes an organization's name, slug and/or logo URL.
// Owner-only — the route is guarded by middleware.RequireOrgOwner; this
// function assumes that check already ran.
//
// Renaming the slug is the interesting part. It moves every URL of the
// organization, including the intentionally public ones customers have pasted
// elsewhere (status pages, SVG badges, the /embed/v1 widget). Those links keep
// working: the old slug is recorded as an alias
// (models.OrganizationPreviousSlug) and middleware.OrgSlugRedirect answers a
// permanent redirect to the new one. Two rules bound that promise:
//
//   - An alias can never shadow the organization now living at that slug —
//     resolution tries the live slug first, and db.UpdateOrganization /
//     db.CreateOrganization release any alias on a slug they hand out.
//   - The guarantee lasts only until another organization claims the freed
//     slug, which is what the dashboard warns about before saving.
func (s *Service) UpdateOrgProfile(
	ctx context.Context, orgSlug string, userUID string,
	req UpdateOrgProfileRequest, authContext Context,
) (*OrgProfileResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrOrganizationNotFound
		}

		return nil, err
	}

	update, newSlug, err := s.buildOrgProfileUpdate(ctx, org, req)
	if err != nil {
		return nil, err
	}

	if updErr := s.db.UpdateOrganization(ctx, org.UID, update); updErr != nil {
		return nil, fmt.Errorf("failed to update organization: %w", updErr)
	}

	// Replacing or clearing an uploaded logo retires its file row, which is
	// also what stops /pub/org-logos/:uid from serving it. Best-effort: the org
	// already points elsewhere, so a surviving row is wasted storage, not a
	// leak.
	if (update.LogoURL != nil || update.ClearLogoURL) && org.LogoFileUID != nil {
		if delErr := s.db.DeleteFile(ctx, org.UID, *org.LogoFileUID); delErr != nil {
			slog.WarnContext(ctx, "failed to retire previous organization logo file",
				"error", delErr, "orgUID", org.UID, "fileUID", *org.LogoFileUID)
		}
	}

	previousSlug := ""

	if newSlug != "" {
		previousSlug = org.Slug

		if aliasErr := s.recordSlugRename(ctx, org.UID, previousSlug); aliasErr != nil {
			return nil, aliasErr
		}
	}

	updated, err := s.db.GetOrganization(ctx, org.UID)
	if err != nil {
		return nil, fmt.Errorf("failed to reload organization: %w", err)
	}

	resp := &OrgProfileResponse{
		UID:          updated.UID,
		Slug:         updated.Slug,
		Name:         updated.Name,
		LogoURL:      updated.LogoURL,
		PreviousSlug: previousSlug,
	}

	if newSlug == "" {
		return resp, nil
	}

	slog.InfoContext(ctx, "organization renamed",
		"orgUID", org.UID, "previousSlug", previousSlug, "slug", updated.Slug)

	return s.attachRenameSession(ctx, userUID, updated, authContext, resp)
}

// buildOrgProfileUpdate validates the request and turns it into a database
// update. The returned newSlug is empty when the slug is not changing.
func (s *Service) buildOrgProfileUpdate(
	ctx context.Context, org *models.Organization, req UpdateOrgProfileRequest,
) (models.OrganizationUpdate, string, error) {
	var update models.OrganizationUpdate

	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" || len(name) > maxOrgNameLength {
			return update, "", ErrInvalidOrgName
		}

		update.Name = &name
	}

	newSlug := ""

	if req.Slug != nil && *req.Slug != org.Slug {
		slug := *req.Slug
		if !orgslug.IsValid(slug) {
			return update, "", ErrInvalidOrgSlug
		}

		// A live organization always owns its slug: only a slug held by
		// nobody, or held solely as another org's rename alias, is claimable.
		existing, err := s.db.GetOrganizationBySlug(ctx, slug)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return update, "", err
		}

		if existing != nil && existing.UID != org.UID {
			return update, "", ErrOrgSlugTaken
		}

		update.Slug = &slug
		newSlug = slug
	}

	if req.LogoURLSet {
		logo, err := normalizeLogoURL(req.LogoURL)
		if err != nil {
			return update, "", err
		}

		if logo == "" {
			// Clearing an uploaded logo also drops the file pointer, which is
			// what un-publishes the blob on /pub/org-logos/:uid.
			update.ClearLogoURL = true
			update.ClearLogoFileUID = true
		} else {
			update.LogoURL = &logo
			update.ClearLogoFileUID = true
		}
	}

	return update, newSlug, nil
}

// recordSlugRename persists the alias for the slug the organization just freed.
//
// Releasing any alias held on the NEW slug is not done here: UpdateOrganization
// does it for every slug write, which also covers an organization renaming back
// onto a slug it still holds as its own alias.
func (s *Service) recordSlugRename(ctx context.Context, orgUID, previousSlug string) error {
	if err := s.db.AddOrganizationPreviousSlug(ctx, orgUID, previousSlug); err != nil {
		return fmt.Errorf("failed to record previous slug: %w", err)
	}

	return nil
}

// attachRenameSession mints a session scoped to the renamed org and attaches it
// to the response.
func (s *Service) attachRenameSession(
	ctx context.Context, userUID string, org *models.Organization,
	authContext Context, resp *OrgProfileResponse,
) (*OrgProfileResponse, error) {
	role := string(models.MemberRoleOwner)
	if member, err := s.db.GetMemberByUserAndOrg(ctx, userUID, org.UID); err == nil && member != nil {
		role = string(member.Role)
	}

	session, err := s.mintOrgSession(ctx, userUID, org, role, authContext)
	if err != nil {
		return nil, err
	}

	resp.AccessToken = session.AccessToken
	resp.RefreshToken = session.RefreshToken
	resp.ExpiresIn = session.ExpiresIn
	resp.TokenType = session.TokenType

	return resp, nil
}

// normalizeLogoURL validates a client-supplied logo URL. Only absolute http(s)
// URLs are accepted: the "/pub/org-logos/<uid>" form is produced exclusively by
// the upload endpoint, so accepting it here would let a caller point one org's
// logo at another org's file. An empty/null value means "clear".
func normalizeLogoURL(raw *string) (string, error) {
	if raw == nil {
		return "", nil
	}

	value := strings.TrimSpace(*raw)
	if value == "" {
		return "", nil
	}

	if len(value) > maxLogoURLLength {
		return "", ErrInvalidLogoURL
	}

	parsed, err := url.Parse(value)
	if err != nil {
		return "", ErrInvalidLogoURL
	}

	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", ErrInvalidLogoURL
	}

	return value, nil
}

// orgSession is a freshly minted org-scoped session.
type orgSession struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
	TokenType    string
}

// mintOrgSession issues a refresh-token row plus an access token whose orgSlug
// claim is the given org's CURRENT slug. It is the shared implementation behind
// CreateOrg and a slug rename: both hand the caller a token for an org their
// current claims do not match.
func (s *Service) mintOrgSession(
	ctx context.Context, userUID string, org *models.Organization, role string, authContext Context,
) (*orgSession, error) {
	refreshTokenValue, err := s.generateRefreshToken()
	if err != nil {
		return nil, err
	}

	now := time.Now()

	refreshToken := models.NewUserToken(userUID, &org.UID, refreshTokenValue, models.TokenTypeRefresh)
	expiresAt := s.refreshTokenExpiry(ctx, org.UID, now, now)
	refreshToken.ExpiresAt = &expiresAt
	refreshToken.LastActiveAt = &now
	refreshToken.Properties = models.JSONMap{
		keyCreatedWith: authContext.ToMap(),
	}

	if createTokenErr := s.db.CreateUserToken(ctx, refreshToken); createTokenErr != nil {
		return nil, createTokenErr
	}

	s.enforceSessionCap(ctx, userUID)

	accessToken, err := s.generateAccessToken(userUID, org.Slug, role, refreshToken.UID)
	if err != nil {
		return nil, err
	}

	return &orgSession{
		AccessToken:  accessToken,
		RefreshToken: refreshTokenValue,
		ExpiresIn:    int(s.cfg.AccessTokenExpiry.Seconds()),
		TokenType:    tokenTypeBearer,
	}, nil
}
