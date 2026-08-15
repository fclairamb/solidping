package cli

import (
	"github.com/urfave/cli/v3"
)

// GetCommands returns all CLI commands
//
//nolint:funlen,maintidx // Command definitions are inherently long
func GetCommands() []*cli.Command {
	return []*cli.Command{
		{
			Name:  "auth",
			Usage: "Authentication commands",
			Flags: GetGlobalFlags(),
			Commands: []*cli.Command{
				{
					Name: "login",
					Usage: "Login by approving a one-time code in any browser (default), " +
						"or with --with-password / --email / --password, or a pasted --token",
					Flags: []cli.Flag{
						&cli.BoolFlag{
							Name:  "with-password",
							Usage: "Log in with email and password instead of the device flow",
						},
						&cli.StringFlag{
							Name:    "email",
							Aliases: []string{"e"},
							Usage:   "Email for non-interactive password login",
							Sources: cli.EnvVars("SOLIDPING_EMAIL"),
						},
						&cli.StringFlag{
							Name:    "password",
							Aliases: []string{"p"},
							Usage:   "Password for non-interactive password login",
							Sources: cli.EnvVars("SOLIDPING_PASSWORD"),
						},
						&cli.StringFlag{
							Name:    flagToken,
							Aliases: []string{"t"},
							Usage:   "Save a pasted Personal Access Token (headless machines)",
							Sources: cli.EnvVars("SP_TOKEN"),
						},
					},
					Action: authLoginAction,
				},
				{
					Name:   "logout",
					Usage:  "Logout and clear session token",
					Action: authLogoutAction,
				},
				{
					Name:   "me",
					Usage:  "Show current authenticated user",
					Action: authMeAction,
				},
				{
					Name:      "switch-org",
					Usage:     "Switch to a different organization",
					ArgsUsage: "<org>",
					Action:    authSwitchOrgAction,
				},
				{
					Name:  "register",
					Usage: "Register a new account (self-service)",
					Flags: []cli.Flag{
						&cli.StringFlag{Name: flagEmail, Aliases: []string{"e"}, Usage: "Email address"},
						&cli.StringFlag{Name: flagPassword, Aliases: []string{"p"}, Usage: "Password (min 8 chars)"},
						&cli.StringFlag{Name: flagName, Usage: "Display name"},
					},
					Action: authRegisterAction,
				},
				{
					Name:      "confirm-registration",
					Usage:     "Confirm a registration with the emailed token",
					ArgsUsage: "<token>",
					Action:    authConfirmRegistrationAction,
				},
				{
					Name:  "request-password-reset",
					Usage: "Request a password reset email",
					Flags: []cli.Flag{
						&cli.StringFlag{Name: flagEmail, Aliases: []string{"e"}, Usage: "Email address"},
					},
					Action: authRequestPasswordResetAction,
				},
				{
					Name:  "reset-password",
					Usage: "Set a new password using a reset token",
					Flags: []cli.Flag{
						&cli.StringFlag{Name: flagToken, Aliases: []string{"t"}, Usage: "Reset token from the emailed link"},
						&cli.StringFlag{Name: flagPassword, Aliases: []string{"p"}, Usage: "New password (min 8 chars)"},
					},
					Action: authResetPasswordAction,
				},
				{
					Name:  "update",
					Usage: "Update the authenticated user's profile",
					Flags: []cli.Flag{
						&cli.StringFlag{Name: flagName, Usage: "New display name"},
					},
					Action: authUpdateAction,
				},
				{
					Name:   "providers",
					Usage:  "List configured auth providers (OAuth/SSO/LDAP)",
					Action: authProvidersAction,
				},
				{
					Name:      "invite",
					Usage:     "Inspect an invitation by token",
					ArgsUsage: "<token>",
					Action:    authInviteAction,
				},
				{
					Name:  "accept-invite",
					Usage: "Accept an invitation",
					Flags: []cli.Flag{
						&cli.StringFlag{Name: flagToken, Aliases: []string{"t"}, Usage: "Invitation token from the emailed link"},
						&cli.StringFlag{Name: flagName, Usage: "Display name (new users)"},
						&cli.StringFlag{Name: flagPassword, Aliases: []string{"p"}, Usage: "Password (new users, min 8 chars)"},
					},
					Action: authAcceptInviteAction,
				},
			},
		},
		{
			Name:  "server",
			Usage: "Server management commands",
			Flags: GetGlobalFlags(),
			Commands: []*cli.Command{
				{
					Name:   "health",
					Usage:  "Check server health",
					Action: serverHealthAction,
				},
				{
					Name:   "version",
					Usage:  "Get server version",
					Action: serverVersionAction,
				},
				{
					Name:   "limits",
					Usage:  "Show rate-limit and concurrency state for the caller",
					Action: serverLimitsAction,
				},
				{
					Name:   "memory",
					Usage:  "Show a runtime memory snapshot (super-admin)",
					Action: serverMemoryAction,
				},
				{
					Name:  "cost-distribution",
					Usage: "Show the scheduler cost/delay distribution (super-admin)",
					Flags: []cli.Flag{
						&cli.IntFlag{
							Name:  flagThresholdMs,
							Usage: "Candidate fast/slow boundary in milliseconds (default 1000)",
						},
					},
					Action: serverCostDistributionAction,
				},
			},
		},
		{
			Name:    "checks",
			Aliases: []string{flagCheck},
			Usage:   "Manage health checks",
			Flags:   GetGlobalFlags(),
			Commands: []*cli.Command{
				{
					Name:  flagList,
					Usage: "List all checks",
					Flags: []cli.Flag{
						&cli.BoolFlag{
							Name:  "with-last-result",
							Usage: "Include last execution result for each check",
						},
						&cli.BoolFlag{
							Name:  "internal",
							Usage: "Show only internal checks",
						},
						&cli.BoolFlag{
							Name:  flagAll,
							Usage: "Show all checks (internal + non-internal)",
						},
					},
					Action: checksListAction,
				},
				{
					Name:      flagGet,
					Usage:     "Get check details",
					ArgsUsage: argUIDSlug,
					Action:    checksGetAction,
				},
				{
					Name:      flagAdd,
					Usage:     "Add a new check",
					ArgsUsage: "<url>",
					Flags: []cli.Flag{
						&cli.StringFlag{
							Name:  flagType,
							Value: "http",
							Usage: "Check type (http, tcp, ping, dns, ssl)",
						},
						&cli.StringFlag{
							Name:  flagInterval,
							Usage: "Check interval (e.g., 5s, 1m)",
						},
						&cli.StringFlag{
							Name:  "timeout",
							Usage: "Request timeout (e.g., 2s, 5s)",
						},
						&cli.StringFlag{
							Name:  flagName,
							Usage: usageHumanReadableName,
						},
						&cli.StringFlag{
							Name:  keySlug,
							Usage: "Unique identifier slug",
						},
						&cli.IntFlag{
							Name:    "number",
							Aliases: []string{"nb"},
							Value:   1,
							Usage:   "Number of checks to create (1 to 10,000)",
						},
					},
					Action: checksAddAction,
				},
				{
					Name:      cmdUpdate,
					Usage:     "Update a check",
					ArgsUsage: argUIDSlug,
					Flags: []cli.Flag{
						&cli.StringFlag{
							Name:  flagName,
							Usage: usageHumanReadableName,
						},
						&cli.StringFlag{
							Name:  keySlug,
							Usage: "Unique identifier slug",
						},
						&cli.BoolFlag{
							Name:  "enabled",
							Usage: "Enable the check",
						},
						&cli.BoolFlag{
							Name:  "disabled",
							Usage: "Disable the check",
						},
						&cli.StringFlag{
							Name:  flagInterval,
							Usage: "Check interval (e.g., 5s, 1m, or HH:MM:SS)",
						},
					},
					Action: checksUpdateAction,
				},
				{
					Name:      "upsert",
					Usage:     "Create or update a check by slug",
					ArgsUsage: "<slug> <url>",
					Flags: []cli.Flag{
						&cli.StringFlag{
							Name:  flagType,
							Usage: "Check type (http, tcp, ping, dns, ssl)",
						},
						&cli.StringFlag{
							Name:  flagName,
							Usage: usageHumanReadableName,
						},
						&cli.StringFlag{
							Name:  flagInterval,
							Usage: "Check interval (e.g., 5s, 1m, or HH:MM:SS)",
						},
						&cli.StringFlag{
							Name:  "timeout",
							Usage: "Request timeout (e.g., 2s, 5s)",
						},
					},
					Action: checksUpsertAction,
				},
				{
					Name:      "validate",
					Usage:     "Validate a check definition or a whole export document (offline, no token/network for a document)",
					ArgsUsage: "[file]",
					Flags: []cli.Flag{
						&cli.StringFlag{
							Name:    flagFile,
							Aliases: []string{"f"},
							Usage:   "Check definition or export document (JSON or YAML); reads stdin if omitted",
						},
					},
					Action: checksValidateAction,
				},
				{
					Name:      "clone",
					Usage:     "Clone an existing check",
					ArgsUsage: argUIDSlug,
					Flags: []cli.Flag{
						&cli.StringFlag{
							Name:  flagName,
							Usage: "Name for the cloned check",
						},
						&cli.StringFlag{
							Name:  keySlug,
							Usage: "Slug for the cloned check (auto-generated if omitted)",
						},
						&cli.StringFlag{
							Name:  flagDescription,
							Usage: "Description for the cloned check",
						},
						&cli.StringFlag{
							Name:  "group",
							Usage: "Check group UID for the cloned check",
						},
						&cli.BoolFlag{
							Name:  "enabled",
							Usage: "Whether the clone is enabled",
						},
					},
					Action: checksCloneAction,
				},
				{
					Name:      "availability",
					Usage:     "Show availability rollups for a check",
					ArgsUsage: argUIDSlug,
					Flags: []cli.Flag{
						&cli.StringFlag{
							Name:  "periods",
							Usage: "Comma-separated period tokens (e.g., 24h,7d,30d,90d,today,mtd,ytd)",
						},
						&cli.StringFlag{
							Name:  "tz",
							Usage: "IANA time zone for calendar tokens (default UTC)",
						},
					},
					Action: checksAvailabilityAction,
				},
				{
					Name:      flagEvents,
					Usage:     "List events for a check",
					ArgsUsage: argUIDSlug,
					Flags: []cli.Flag{
						&cli.StringFlag{
							Name:  flagCursor,
							Usage: usagePaginationCursor,
						},
						&cli.IntFlag{
							Name:  flagSize,
							Usage: "Results per page",
							Value: 20,
						},
					},
					Action: checksEventsAction,
				},
				{
					Name:      flagRemove,
					Aliases:   []string{"rm", cmdDelete},
					Usage:     "Remove a check",
					ArgsUsage: argUIDSlug,
					Action:    checksRemoveAction,
				},
				{
					Name:  "deps",
					Usage: "Manage check dependencies",
					Commands: []*cli.Command{
						{
							Name:      flagList,
							Usage:     "List parents of a check",
							ArgsUsage: "<child-slug>",
							Action:    checksDepsListAction,
						},
						{
							Name:      flagAdd,
							Usage:     "Add a parent dependency",
							ArgsUsage: argChildParent,
							Flags: []cli.Flag{
								&cli.StringFlag{
									Name:  "kind",
									Usage: "Dependency kind: hard or soft",
									Value: depKindHard,
								},
								&cli.StringFlag{
									Name:  flagDescription,
									Usage: "Optional description for the edge",
								},
							},
							Action: checksDepsAddAction,
						},
						{
							Name:      flagRemove,
							Aliases:   []string{"rm"},
							Usage:     "Drop a parent dependency",
							ArgsUsage: argChildParent,
							Action:    checksDepsRemoveAction,
						},
						{
							Name:      cmdSet,
							Usage:     "Replace the full dependsOn set for a child check (PUT-by-slug semantics)",
							ArgsUsage: "<child-slug>",
							Flags: []cli.Flag{
								&cli.StringFlag{
									Name:     "from",
									Usage:    "YAML or JSON file with {dependsOn: [{parentSlug, kind, description?}]}",
									Required: true,
								},
							},
							Action: checksDepsSetAction,
						},
						{
							Name:      cmdUpdate,
							Usage:     "Update a parent dependency edge (kind/description)",
							ArgsUsage: argChildParent,
							Flags: []cli.Flag{
								&cli.StringFlag{
									Name:  "kind",
									Usage: "Dependency kind: hard or soft",
								},
								&cli.StringFlag{
									Name:  flagDescription,
									Usage: "Description for the edge",
								},
							},
							Action: checksDepsUpdateAction,
						},
						{
							Name:   "graph",
							Usage:  "Show the org-wide dependency graph",
							Action: checksDepsGraphAction,
						},
					},
				},
				{
					Name:  "channels",
					Usage: "Manage the notification channels bound to a check",
					Commands: []*cli.Command{
						{
							Name:      flagList,
							Usage:     "List channels bound to a check",
							ArgsUsage: argUIDSlug,
							Action:    checksChannelsListAction,
						},
						{
							Name:      cmdSet,
							Usage:     "Replace the full set of channels bound to a check",
							ArgsUsage: "<uid|slug> [<connection-uid>...]",
							Action:    checksChannelsSetAction,
						},
						{
							Name:      flagAdd,
							Usage:     "Bind a single channel to a check",
							ArgsUsage: "<uid|slug> <connection-uid>",
							Action:    checksChannelsAddAction,
						},
						{
							Name:      flagRemove,
							Aliases:   []string{"rm"},
							Usage:     "Unbind a single channel from a check",
							ArgsUsage: "<uid|slug> <connection-uid>",
							Action:    checksChannelsRemoveAction,
						},
					},
				},
				{
					Name:  "export",
					Usage: "Export all checks as a portable JSON or YAML document (admin-only)",
					Flags: []cli.Flag{
						&cli.StringFlag{
							Name:  flagFile,
							Usage: "Write the export to a file instead of stdout",
						},
						&cli.StringFlag{
							Name: flagFormat,
							Usage: "Output format: yaml or json (default: inferred from --file's " +
								"extension, else json)",
						},
					},
					Action: checksExportAction,
				},
				{
					Name:  "import",
					Usage: "Import checks from an export document (idempotent upsert on slug, never deletes, admin-only)",
					Description: "Loads a JSON or YAML export document and upserts each check by slug. " +
						"This never deletes: a check removed from the file simply stays untouched. Always " +
						"start from a fresh `sp checks export` before hand-editing, and use `sp apply --prune` " +
						"instead if you need delete-by-absence.",
					ArgsUsage: "<file>",
					Flags: []cli.Flag{
						&cli.BoolFlag{
							Name:  "dry-run",
							Usage: "Preview created/updated counts without mutating",
						},
					},
					Action: checksImportAction,
				},
				{
					Name:      "diff",
					Usage:     "Show how a local export file differs from what SolidPing currently holds",
					ArgsUsage: "<file>",
					Action:    checksDiffAction,
				},
			},
		},
		{
			Name:      "apply",
			Usage:     "Reconcile checks against a declarative manifest (config-as-code, admin-only)",
			ArgsUsage: "<manifest>",
			Flags: append(GetGlobalFlags(),
				&cli.StringFlag{
					Name:    flagFile,
					Aliases: []string{"f"},
					Usage:   "Manifest file (JSON or YAML)",
				},
				&cli.BoolFlag{
					Name:  "dry-run",
					Usage: "Show the reconcile plan without mutating anything",
				},
				&cli.BoolFlag{
					Name:  "prune",
					Usage: "Delete managed checks that are absent from the manifest",
				},
				&cli.BoolFlag{
					Name:  "force",
					Usage: "Lift the deletion cap for this apply (use with --prune)",
				},
				&cli.BoolFlag{
					Name:    "yes",
					Aliases: []string{"y"},
					Usage:   "Skip the confirmation prompt and apply immediately",
				},
			),
			Action: applyAction,
		},
		{
			Name:    "results",
			Aliases: []string{"result"},
			Usage:   "View check results",
			Flags:   GetGlobalFlags(),
			Commands: []*cli.Command{
				{
					Name:  flagList,
					Usage: "List check results with filtering",
					Flags: []cli.Flag{
						&cli.StringFlag{
							Name:  flagCheck,
							Usage: "Filter by check UID or slug (comma-separated for multiple)",
						},
						&cli.StringFlag{
							Name:  "check-type",
							Usage: "Filter by check type: http, dns, ping, ssl (comma-separated)",
						},
						&cli.StringFlag{
							Name:  flagStatus,
							Usage: "Filter by status: up, down, unknown (comma-separated)",
						},
						&cli.StringFlag{
							Name:  "region",
							Usage: "Filter by region (comma-separated)",
						},
						&cli.StringFlag{
							Name:  "period-type",
							Usage: "Filter by period type: raw, hour, day, month (comma-separated)",
						},
						&cli.StringFlag{
							Name:  flagCursor,
							Usage: usagePaginationCursor,
						},
						&cli.IntFlag{
							Name:  flagSize,
							Usage: usageResultsPerPage,
							Value: 20,
						},
						&cli.StringFlag{
							Name:  "with",
							Usage: "Optional fields to include (comma-separated): metrics,output,durationMs,region",
						},
						&cli.BoolFlag{
							Name:  "auto",
							Usage: "Automatically fetch all pages (ignores --cursor flag)",
						},
					},
					Action: resultsListAction,
				},
				{
					Name:      flagGet,
					Usage:     "Get a single result by check and result UID",
					ArgsUsage: "<check> <uid>",
					Flags: []cli.Flag{
						&cli.StringFlag{
							Name:  "region",
							Usage: "Narrow neighbor scope to a region (comma-separated)",
						},
					},
					Action: resultsGetAction,
				},
			},
		},
		{
			Name:    "incidents",
			Aliases: []string{"incident"},
			Usage:   "Manage incidents",
			Flags:   GetGlobalFlags(),
			Commands: []*cli.Command{
				{
					Name:  flagList,
					Usage: "List incidents",
					Flags: []cli.Flag{
						&cli.StringFlag{
							Name:  flagCheck,
							Usage: "Filter by check UID (comma-separated for multiple)",
						},
						&cli.StringFlag{
							Name:  "state",
							Usage: "Filter by state: active, resolved (comma-separated)",
						},
						&cli.StringFlag{
							Name:  flagCursor,
							Usage: usagePaginationCursor,
						},
						&cli.IntFlag{
							Name:  flagSize,
							Usage: usageResultsPerPage,
							Value: 20,
						},
					},
					Action: incidentsListAction,
				},
				{
					Name:      flagGet,
					Usage:     "Get incident details",
					ArgsUsage: argUID,
					Action:    incidentsGetAction,
				},
				{
					Name:      flagEvents,
					Usage:     "List events for an incident",
					ArgsUsage: argUID,
					Flags: []cli.Flag{
						&cli.StringFlag{
							Name:  flagCursor,
							Usage: usagePaginationCursor,
						},
						&cli.IntFlag{
							Name:  flagSize,
							Usage: "Results per page",
							Value: 20,
						},
					},
					Action: incidentsEventsAction,
				},
				{
					Name:      "ack",
					Usage:     "Acknowledge an incident",
					ArgsUsage: argUID,
					Flags: []cli.Flag{
						&cli.StringFlag{
							Name:  "note",
							Usage: "Optional note recorded with the acknowledgement",
						},
					},
					Action: incidentsAckAction,
				},
				{
					Name:      "unack",
					Usage:     "Remove acknowledgement from an incident",
					ArgsUsage: argUID,
					Action:    incidentsUnackAction,
				},
				{
					Name:      "snooze",
					Usage:     "Snooze an incident for a duration or until a time",
					ArgsUsage: argUID,
					Flags: []cli.Flag{
						&cli.StringFlag{
							Name:  "duration",
							Usage: "Relative snooze duration (e.g., 1h, 30m)",
						},
						&cli.StringFlag{
							Name:  "until",
							Usage: "Absolute snooze end as an RFC3339 timestamp",
						},
						&cli.StringFlag{
							Name:  "reason",
							Usage: "Optional reason for the snooze",
						},
					},
					Action: incidentsSnoozeAction,
				},
				{
					Name:      "unsnooze",
					Usage:     "Clear an incident snooze",
					ArgsUsage: argUID,
					Action:    incidentsUnsnoozeAction,
				},
				{
					Name:      "resolve",
					Usage:     "Resolve an incident",
					ArgsUsage: argUID,
					Flags: []cli.Flag{
						&cli.StringFlag{
							Name:  "note",
							Usage: "Optional note recorded with the resolution",
						},
					},
					Action: incidentsResolveAction,
				},
			},
		},
		{
			Name:    "channels",
			Aliases: []string{"channel"},
			Usage:   "Manage notification channels (integrations)",
			Flags:   GetGlobalFlags(),
			Commands: []*cli.Command{
				{
					Name:  flagList,
					Usage: "List notification channels",
					Flags: []cli.Flag{
						&cli.StringFlag{
							Name:  flagType,
							Usage: "Filter by channel type (e.g., slack, webhook)",
						},
					},
					Action: channelsListAction,
				},
				{
					Name:  cmdCreate,
					Usage: "Create a notification channel",
					Flags: []cli.Flag{
						&cli.StringFlag{
							Name:     flagType,
							Usage:    "Channel type (slack, discord, webhook, email, ...)",
							Required: true,
						},
						&cli.StringFlag{
							Name:     flagName,
							Usage:    usageHumanReadableName,
							Required: true,
						},
						&cli.StringFlag{
							Name:  flagSettings,
							Usage: "Channel settings as a JSON object",
						},
						&cli.BoolFlag{
							Name:  flagDisabled,
							Usage: "Create the channel disabled",
						},
						&cli.BoolFlag{
							Name:  flagDefault,
							Usage: "Mark the channel as a default target",
						},
					},
					Action: channelsCreateAction,
				},
				{
					Name:      flagGet,
					Usage:     "Get channel details",
					ArgsUsage: argUID,
					Action:    channelsGetAction,
				},
				{
					Name:      cmdUpdate,
					Usage:     "Update a channel",
					ArgsUsage: argUID,
					Flags: []cli.Flag{
						&cli.StringFlag{
							Name:  flagName,
							Usage: usageHumanReadableName,
						},
						&cli.StringFlag{
							Name:  flagSettings,
							Usage: "Channel settings as a JSON object",
						},
						&cli.BoolFlag{
							Name:  flagEnabled,
							Usage: "Enable the channel",
						},
						&cli.BoolFlag{
							Name:  flagDisabled,
							Usage: "Disable the channel",
						},
						&cli.BoolFlag{
							Name:  flagDefault,
							Usage: "Mark the channel as a default target",
						},
						&cli.BoolFlag{
							Name:  flagNoDefault,
							Usage: "Unmark the channel as a default target",
						},
					},
					Action: channelsUpdateAction,
				},
				{
					Name:      flagRemove,
					Aliases:   []string{"rm", cmdDelete},
					Usage:     "Remove a channel",
					ArgsUsage: argUID,
					Action:    channelsRemoveAction,
				},
				{
					Name:      "rotate-secret",
					Usage:     "Rotate a webhook channel's signing secret",
					ArgsUsage: argUID,
					Action:    channelsRotateSecretAction,
				},
				{
					Name:      cmdTest,
					Usage:     "Send a sample notification through a channel",
					ArgsUsage: argUID,
					Action:    channelsTestAction,
				},
			},
		},
		{
			Name:    flagEvents,
			Aliases: []string{"event"},
			Usage:   "View audit events",
			Flags:   GetGlobalFlags(),
			Commands: []*cli.Command{
				{
					Name:  flagList,
					Usage: "List events",
					Flags: []cli.Flag{
						&cli.StringFlag{
							Name:  flagType,
							Usage: "Filter by event type (comma-separated)",
						},
						&cli.StringFlag{
							Name:  flagCheck,
							Usage: "Filter by check UID",
						},
						&cli.StringFlag{
							Name:  "incident",
							Usage: "Filter by incident UID",
						},
						&cli.StringFlag{
							Name:  flagCursor,
							Usage: usagePaginationCursor,
						},
						&cli.IntFlag{
							Name:  flagSize,
							Usage: usageResultsPerPage,
							Value: 20,
						},
					},
					Action: eventsListAction,
				},
			},
		},
		{
			Name:    "tokens",
			Aliases: []string{flagToken},
			Usage:   "Manage personal access tokens",
			Flags:   GetGlobalFlags(),
			Commands: []*cli.Command{
				{
					Name:  flagList,
					Usage: "List personal access tokens",
					Flags: []cli.Flag{
						&cli.BoolFlag{
							Name:  flagAll,
							Usage: "List tokens across all organizations",
						},
					},
					Action: tokensListAction,
				},
				{
					Name:  cmdCreate,
					Usage: "Create a personal access token",
					Flags: []cli.Flag{
						&cli.StringFlag{
							Name:     flagName,
							Usage:    "Token name",
							Required: true,
						},
						&cli.StringFlag{
							Name:  "expires",
							Usage: "Expiration: 7d, 30d, 90d, 1y, never",
						},
					},
					Action: tokensCreateAction,
				},
				{
					Name:      "revoke",
					Usage:     "Revoke a personal access token",
					ArgsUsage: argUID,
					Action:    tokensRevokeAction,
				},
			},
		},
		{
			Name:    "members",
			Aliases: []string{"member"},
			Usage:   "Manage organization members",
			Flags:   GetGlobalFlags(),
			Commands: []*cli.Command{
				{
					Name:   flagList,
					Usage:  "List organization members",
					Action: membersListAction,
				},
				{
					Name:      "add",
					Usage:     "Add a member to the organization",
					ArgsUsage: "<email>",
					Flags: []cli.Flag{
						&cli.StringFlag{
							Name:  "role",
							Value: "member",
							Usage: "Member role: admin, member, viewer",
						},
					},
					Action: membersAddAction,
				},
				{
					Name:      flagGet,
					Usage:     "Get member details",
					ArgsUsage: argUID,
					Action:    membersGetAction,
				},
				{
					Name:      cmdUpdate,
					Usage:     "Update a member",
					ArgsUsage: argUID,
					Flags: []cli.Flag{
						&cli.StringFlag{
							Name:  "role",
							Usage: "Member role: admin, member, viewer",
						},
					},
					Action: membersUpdateAction,
				},
				{
					Name:      "remove",
					Aliases:   []string{"rm"},
					Usage:     "Remove a member from the organization",
					ArgsUsage: argUID,
					Action:    membersRemoveAction,
				},
			},
		},
		{
			Name:    "jobs",
			Aliases: []string{"job"},
			Usage:   "Manage background jobs",
			Flags:   GetGlobalFlags(),
			Commands: []*cli.Command{
				{
					Name:  flagList,
					Usage: "List jobs",
					Flags: []cli.Flag{
						&cli.StringFlag{
							Name:  flagType,
							Usage: "Filter by job type",
						},
						&cli.StringFlag{
							Name:  flagStatus,
							Usage: "Filter by status",
						},
					},
					Action: jobsListAction,
				},
				{
					Name:      flagGet,
					Usage:     "Get job details",
					ArgsUsage: argUID,
					Action:    jobsGetAction,
				},
				{
					Name:  cmdCreate,
					Usage: "Create a job (org admin; allowlisted types only — currently 'sleep')",
					Description: "The server only accepts allowlisted job types on the public create\n" +
						"endpoint — currently 'sleep' alone. Types such as 'email' and 'webhook'\n" +
						"are refused with 403: SolidPing enqueues those itself, and exposing them\n" +
						"would mean an arbitrary mail sender and arbitrary outbound HTTP.",
					Flags: []cli.Flag{
						&cli.StringFlag{
							Name:     "type",
							Usage:    "Job type (allowlisted types only — currently 'sleep')",
							Required: true,
						},
						&cli.StringFlag{
							Name:  "config",
							Usage: "Job config as JSON string",
						},
					},
					Action: jobsCreateAction,
				},
				{
					Name:      cmdCancel,
					Usage:     "Cancel a job",
					ArgsUsage: argUID,
					Action:    jobsCancelAction,
				},
				{
					Name:   "stats",
					Usage:  "Show background-job & check-schedule stats (admin)",
					Action: jobsStatsAction,
				},
				{
					Name:  "admin",
					Usage: "Admin views over the background-jobs queue",
					Commands: []*cli.Command{
						{
							Name:   flagList,
							Usage:  "List background jobs (admin)",
							Flags:  jobAdminListFlags(),
							Action: jobsAdminListAction,
						},
						{
							Name:      flagGet,
							Usage:     "Get a background job (admin)",
							ArgsUsage: argUID,
							Action:    jobsAdminGetAction,
						},
						{
							Name:      "chain",
							Usage:     "Show a job's retry chain (admin)",
							ArgsUsage: argUID,
							Action:    jobsAdminChainAction,
						},
					},
				},
			},
		},
		{
			Name:    "check-jobs",
			Aliases: []string{"check-job", "cj"},
			Usage:   "Inspect the check-schedule (check_jobs) table",
			Flags:   GetGlobalFlags(),
			Commands: []*cli.Command{
				{
					Name:   flagList,
					Usage:  "List check-schedule jobs",
					Flags:  checkJobPaginationFlags(),
					Action: checkJobsListAction,
				},
				{
					Name:      flagGet,
					Usage:     "Get a check-schedule job",
					ArgsUsage: argUID,
					Action:    checkJobsGetAction,
				},
			},
		},
		{
			Name:    "system",
			Aliases: []string{"sys"},
			Usage:   "Manage system parameters and inspect jobs across all orgs",
			Flags:   GetGlobalFlags(),
			Commands: []*cli.Command{
				{
					Name:   flagList,
					Usage:  "List system parameters",
					Action: systemListAction,
				},
				{
					Name:      flagGet,
					Usage:     "Get a system parameter",
					ArgsUsage: "<key>",
					Action:    systemGetAction,
				},
				{
					Name:      cmdSet,
					Usage:     "Set a system parameter",
					ArgsUsage: "<key> <value>",
					Flags: []cli.Flag{
						&cli.BoolFlag{
							Name:  "secret",
							Usage: "Mark parameter as secret",
						},
					},
					Action: systemSetAction,
				},
				{
					Name:      cmdDelete,
					Aliases:   []string{"rm"},
					Usage:     "Delete a system parameter",
					ArgsUsage: "<key>",
					Action:    systemDeleteAction,
				},
				{
					Name:  "jobs",
					Usage: "Inspect background jobs across all orgs (super-admin)",
					Commands: []*cli.Command{
						{
							Name:   "stats",
							Usage:  "Show job & check-schedule stats across all orgs",
							Action: systemJobsStatsAction,
						},
						{
							Name:   flagList,
							Usage:  "List background jobs across all orgs",
							Flags:  jobAdminListFlags(),
							Action: systemJobsListAction,
						},
						{
							Name:      flagGet,
							Usage:     "Get a background job across all orgs",
							ArgsUsage: argUID,
							Action:    systemJobsGetAction,
						},
						{
							Name:      "chain",
							Usage:     "Show a job's retry chain across all orgs",
							ArgsUsage: argUID,
							Action:    systemJobsChainAction,
						},
					},
				},
				{
					Name:  "check-jobs",
					Usage: "Inspect the check-schedule across all orgs (super-admin)",
					Commands: []*cli.Command{
						{
							Name:   flagList,
							Usage:  "List check-schedule jobs across all orgs",
							Flags:  checkJobPaginationFlags(),
							Action: systemCheckJobsListAction,
						},
						{
							Name:      flagGet,
							Usage:     "Get a check-schedule job across all orgs",
							ArgsUsage: argUID,
							Action:    systemCheckJobsGetAction,
						},
					},
				},
				{
					Name:      "test-email",
					Usage:     "Send a test email using the saved SMTP settings (super-admin)",
					ArgsUsage: "<recipient>",
					Action:    systemTestEmailAction,
				},
				{
					Name:  "email-inbox",
					Usage: "Inspect and control the JMAP email inbox (super-admin)",
					Commands: []*cli.Command{
						{
							Name:   cmdConfig,
							Usage:  "Show the saved email-inbox configuration",
							Action: systemEmailInboxConfigAction,
						},
						{
							Name:   "status",
							Usage:  "Show the email-inbox connection status",
							Action: systemEmailInboxStatusAction,
						},
						{
							Name:   cmdTest,
							Usage:  "Test the email-inbox connection",
							Action: systemEmailInboxTestAction,
						},
						{
							Name:   "sync",
							Usage:  "Trigger an immediate email-inbox sync",
							Action: systemEmailInboxSyncAction,
						},
					},
				},
				{
					Name:   "activation",
					Usage:  "Show the organization activation funnel (super-admin)",
					Action: systemActivationAction,
				},
				{
					Name:   "lane-load",
					Usage:  "Show each worker's offered check load per lane (super-admin)",
					Action: systemLaneLoadAction,
				},
			},
		},
		{
			Name:    "discovery",
			Aliases: []string{"discover"},
			Usage:   "Network discovery scans and suggested checks",
			Flags:   GetGlobalFlags(),
			Commands: []*cli.Command{
				{
					Name:   "types",
					Usage:  "List registered discovery types",
					Action: discoveryTypesAction,
				},
				{
					Name:  "scans",
					Usage: "Manage discovery scans",
					Commands: []*cli.Command{
						{
							Name:   flagList,
							Usage:  "List discovery scans",
							Action: discoveryScansListAction,
						},
						{
							Name:  cmdCreate,
							Usage: "Start a new discovery scan",
							Flags: []cli.Flag{
								&cli.StringFlag{
									Name:     flagType,
									Usage:    "Discovery type (see `sp discovery types`)",
									Required: true,
								},
								&cli.StringFlag{
									Name:  "params",
									Usage: "Type-specific parameters as a JSON object",
								},
							},
							Action: discoveryScansCreateAction,
						},
						{
							Name:      flagGet,
							Usage:     "Get a discovery scan by UID",
							ArgsUsage: argUID,
							Action:    discoveryScansGetAction,
						},
						{
							Name:      cmdCancel,
							Usage:     "Cancel a running discovery scan",
							ArgsUsage: argUID,
							Action:    discoveryScansCancelAction,
						},
					},
				},
				{
					Name:  flagCheck,
					Usage: "Manage suggested checks from scans",
					Commands: []*cli.Command{
						{
							Name:  flagList,
							Usage: "List discovered (suggested) checks",
							Flags: []cli.Flag{
								&cli.StringFlag{
									Name:  "job-uid",
									Usage: "Filter to one scan's checks",
								},
								&cli.StringFlag{
									Name:  "group",
									Usage: "Narrow to one group (groupKey)",
								},
								&cli.StringFlag{
									Name:  "source",
									Usage: "Comma-separated discovery sources (e.g., lan,freebox)",
								},
								&cli.BoolFlag{
									Name:  "promoted",
									Usage: "Filter by promotion state",
								},
							},
							Action: discoveryChecksListAction,
						},
						{
							Name:      "promote",
							Usage:     "Promote discovered checks into real checks",
							ArgsUsage: "<uid> [<uid>...]",
							Action:    discoveryChecksPromoteAction,
						},
						{
							Name:      "dismiss",
							Usage:     "Dismiss a discovered check",
							ArgsUsage: argUID,
							Action:    discoveryChecksDismissAction,
						},
					},
				},
			},
		},
		{
			Name:    "heartbeat",
			Aliases: []string{"hb"},
			Usage:   "Heartbeat ingestion for cron-style checks",
			Flags:   GetGlobalFlags(),
			Commands: []*cli.Command{
				{
					Name:      "send",
					Usage:     "Send a heartbeat ping for a check identifier",
					ArgsUsage: "<identifier>",
					Flags: []cli.Flag{
						&cli.StringFlag{
							Name:  flagToken,
							Usage: "Heartbeat token (required if the check has one configured)",
						},
						&cli.StringFlag{
							Name:  flagStatus,
							Usage: "Reported status: running, up, down, error",
						},
						&cli.StringFlag{
							Name:  "message",
							Usage: "Optional message to record with the heartbeat",
						},
					},
					Action: heartbeatSendAction,
				},
			},
		},
		{
			Name:    "status-pages",
			Aliases: []string{"status-page"},
			Usage:   "Manage public status pages",
			Flags:   GetGlobalFlags(),
			Commands: []*cli.Command{
				{
					Name:   flagList,
					Usage:  "List status pages",
					Action: statusPagesListAction,
				},
				{
					Name:  cmdCreate,
					Usage: "Create a status page",
					Flags: []cli.Flag{
						&cli.StringFlag{Name: flagName, Usage: usageHumanReadableName, Required: true},
						&cli.StringFlag{Name: keySlug, Usage: usageSlug, Required: true},
						&cli.StringFlag{Name: flagDescription, Usage: "Status page description"},
						&cli.StringFlag{Name: flagVisibility, Usage: "Visibility: public or private"},
						&cli.StringFlag{Name: flagLanguage, Usage: "Page language (e.g. en, fr)"},
						&cli.StringFlag{Name: flagCustomCSS, Usage: "Custom CSS for the public page (max 64 KB, no @import)"},
						&cli.StringFlag{Name: flagCustomCSSFile, Usage: "Read the custom CSS from a file"},
						&cli.StringFlag{Name: flagHistoryPeriod, Usage: "History window: 24h, 7d, 30d, 90d"},
						&cli.IntFlag{Name: flagHistoryDays, Usage: "History window in days (legacy)"},
						&cli.BoolFlag{Name: flagDefault, Usage: "Mark as the default status page"},
						&cli.BoolFlag{Name: flagShowAvailability, Usage: "Show the availability chart"},
						&cli.BoolFlag{Name: flagHideAvailability, Usage: "Hide the availability chart"},
						&cli.BoolFlag{Name: flagShowResponseTime, Usage: "Show the response-time chart"},
						&cli.BoolFlag{Name: flagHideResponseTime, Usage: "Hide the response-time chart"},
					},
					Action: statusPagesCreateAction,
				},
				{
					Name:      flagGet,
					Usage:     "Get a status page",
					ArgsUsage: argUIDSlug,
					Flags: []cli.Flag{
						&cli.StringFlag{Name: flagWith, Usage: "Include related data (e.g. sections)"},
					},
					Action: statusPagesGetAction,
				},
				{
					Name:      cmdUpdate,
					Usage:     "Update a status page",
					ArgsUsage: argUIDSlug,
					Flags: []cli.Flag{
						&cli.StringFlag{Name: flagName, Usage: usageHumanReadableName},
						&cli.StringFlag{Name: keySlug, Usage: usageSlug},
						&cli.StringFlag{Name: flagDescription, Usage: "Status page description"},
						&cli.StringFlag{Name: flagVisibility, Usage: "Visibility: public or private"},
						&cli.StringFlag{
							Name:  flagCustomCSS,
							Usage: "Custom CSS for the public page (max 64 KB, no @import; empty string clears it)",
						},
						&cli.StringFlag{Name: flagCustomCSSFile, Usage: "Read the custom CSS from a file"},
						&cli.StringFlag{Name: flagLanguage, Usage: "Page language (e.g. en, fr)"},
						&cli.StringFlag{Name: flagHistoryPeriod, Usage: "History window: 24h, 7d, 30d, 90d"},
						&cli.IntFlag{Name: flagHistoryDays, Usage: "History window in days (legacy)"},
						&cli.BoolFlag{Name: flagDefault, Usage: "Mark as the default status page"},
						&cli.BoolFlag{Name: flagNoDefault, Usage: "Unset the default status page"},
						&cli.BoolFlag{Name: flagEnabled, Usage: "Enable the status page"},
						&cli.BoolFlag{Name: flagDisabled, Usage: "Disable the status page"},
						&cli.BoolFlag{Name: flagShowAvailability, Usage: "Show the availability chart"},
						&cli.BoolFlag{Name: flagHideAvailability, Usage: "Hide the availability chart"},
						&cli.BoolFlag{Name: flagShowResponseTime, Usage: "Show the response-time chart"},
						&cli.BoolFlag{Name: flagHideResponseTime, Usage: "Hide the response-time chart"},
					},
					Action: statusPagesUpdateAction,
				},
				{
					Name:      flagRemove,
					Aliases:   []string{"rm", cmdDelete},
					Usage:     "Remove a status page",
					ArgsUsage: argUIDSlug,
					Action:    statusPagesRemoveAction,
				},
				{
					Name:  "sections",
					Usage: "Manage the sections of a status page",
					Commands: []*cli.Command{
						{
							Name:      flagList,
							Usage:     "List sections",
							ArgsUsage: argStatusPage,
							Action:    statusPagesSectionsListAction,
						},
						{
							Name:      cmdCreate,
							Usage:     "Create a section",
							ArgsUsage: argStatusPage,
							Flags: []cli.Flag{
								&cli.StringFlag{Name: flagName, Usage: usageHumanReadableName, Required: true},
								&cli.StringFlag{Name: keySlug, Usage: usageSlug, Required: true},
								&cli.IntFlag{Name: flagPosition, Usage: usagePosition},
							},
							Action: statusPagesSectionsCreateAction,
						},
						{
							Name:      flagGet,
							Usage:     "Get a section",
							ArgsUsage: argPageSection,
							Action:    statusPagesSectionsGetAction,
						},
						{
							Name:      cmdUpdate,
							Usage:     "Update a section",
							ArgsUsage: argPageSection,
							Flags: []cli.Flag{
								&cli.StringFlag{Name: flagName, Usage: usageHumanReadableName},
								&cli.StringFlag{Name: keySlug, Usage: usageSlug},
								&cli.IntFlag{Name: flagPosition, Usage: usagePosition},
							},
							Action: statusPagesSectionsUpdateAction,
						},
						{
							Name:      flagRemove,
							Aliases:   []string{"rm"},
							Usage:     "Remove a section",
							ArgsUsage: argPageSection,
							Action:    statusPagesSectionsRemoveAction,
						},
						{
							Name:      cmdReorder,
							Usage:     "Reorder sections (list every section uid exactly once)",
							ArgsUsage: "<status-page> <section-uid>...",
							Action:    statusPagesSectionsReorderAction,
						},
					},
				},
				{
					Name:  "resources",
					Usage: "Manage the resources (checks) of a section",
					Commands: []*cli.Command{
						{
							Name:      flagList,
							Usage:     "List resources of a section",
							ArgsUsage: argPageSection,
							Action:    statusPagesResourcesListAction,
						},
						{
							Name:      cmdCreate,
							Usage:     "Add a check, or a check group, as a resource",
							ArgsUsage: argPageSection,
							Flags: []cli.Flag{
								&cli.StringFlag{Name: flagCheck, Usage: "Check UID or slug to attach"},
								&cli.StringFlag{
									Name: flagCheckGroup,
									Usage: "Check group UID or slug to attach as one aggregated component " +
										"(mutually exclusive with --check)",
								},
								&cli.StringFlag{Name: flagPublicName, Usage: "Public display name"},
								&cli.StringFlag{Name: flagExplanation, Usage: "Explanation shown on the page"},
								&cli.IntFlag{Name: flagPosition, Usage: usagePosition},
							},
							Action: statusPagesResourcesCreateAction,
						},
						{
							Name:      cmdUpdate,
							Usage:     "Update a resource",
							ArgsUsage: argPageSectionResource,
							Flags: []cli.Flag{
								&cli.StringFlag{
									Name:  flagCheck,
									Usage: "Switch the resource to target this check (UID or slug)",
								},
								&cli.StringFlag{
									Name:  flagCheckGroup,
									Usage: "Switch the resource to target this check group (UID or slug)",
								},
								&cli.StringFlag{Name: flagPublicName, Usage: "Public display name"},
								&cli.StringFlag{Name: flagExplanation, Usage: "Explanation shown on the page"},
								&cli.IntFlag{Name: flagPosition, Usage: usagePosition},
							},
							Action: statusPagesResourcesUpdateAction,
						},
						{
							Name:      flagRemove,
							Aliases:   []string{"rm"},
							Usage:     "Remove a resource from a section",
							ArgsUsage: argPageSectionResource,
							Action:    statusPagesResourcesRemoveAction,
						},
						{
							Name:      cmdReorder,
							Usage:     "Reorder resources (list every resource uid exactly once)",
							ArgsUsage: "<status-page> <section> <resource-uid>...",
							Action:    statusPagesResourcesReorderAction,
						},
					},
				},
				{
					Name:  "subscribers",
					Usage: "Manage the subscribers of a status page",
					Commands: []*cli.Command{
						{
							Name:      flagList,
							Usage:     "List subscribers",
							ArgsUsage: argStatusPage,
							Action:    statusPagesSubscribersListAction,
						},
						{
							Name:      flagRemove,
							Aliases:   []string{"rm"},
							Usage:     "Remove a subscriber",
							ArgsUsage: "<status-page> <subscriber-uid>",
							Action:    statusPagesSubscribersRemoveAction,
						},
					},
				},
			},
		},
		{
			Name:    "status-updates",
			Aliases: []string{"status-update"},
			Usage:   "Manage status/incident updates posted to status pages",
			Flags:   GetGlobalFlags(),
			Commands: []*cli.Command{
				{
					Name:  flagList,
					Usage: "List status updates",
					Flags: []cli.Flag{
						&cli.StringFlag{Name: flagStatusPage, Usage: "Filter by status page UID"},
						&cli.StringFlag{Name: flagSection, Usage: "Filter by section UID"},
						&cli.StringFlag{Name: flagCheck, Usage: "Filter by check UID"},
						&cli.StringFlag{Name: flagIncident, Usage: "Filter by incident UID"},
						&cli.IntFlag{Name: flagLimit, Usage: usageResultsLimit},
						&cli.IntFlag{Name: flagOffset, Usage: usageFileOffset},
					},
					Action: statusUpdatesListAction,
				},
				{
					Name:  cmdCreate,
					Usage: "Create a status update",
					Flags: []cli.Flag{
						&cli.StringFlag{Name: flagStatusPage, Usage: "Status page UID", Required: true},
						&cli.StringFlag{Name: flagTitle, Usage: "Update title", Required: true},
						&cli.StringFlag{Name: flagBody, Usage: "Body (markdown)", Required: true},
						&cli.StringFlag{
							Name:     flagKind,
							Usage:    "Kind: investigating, identified, monitoring, resolved, maintenance, info",
							Required: true,
						},
						&cli.StringFlag{Name: flagSection, Usage: "Related section UID"},
						&cli.StringFlag{Name: flagCheck, Usage: "Related check UID"},
						&cli.StringFlag{Name: flagIncident, Usage: "Related incident UID"},
						&cli.StringFlag{Name: flagLink, Usage: "Optional link URL"},
					},
					Action: statusUpdatesCreateAction,
				},
				{
					Name:      flagGet,
					Usage:     "Get a status update",
					ArgsUsage: argUID,
					Action:    statusUpdatesGetAction,
				},
				{
					Name:      cmdUpdate,
					Usage:     "Update a status update",
					ArgsUsage: argUID,
					Flags: []cli.Flag{
						&cli.StringFlag{Name: flagTitle, Usage: "Update title"},
						&cli.StringFlag{Name: flagBody, Usage: "Body (markdown)"},
						&cli.StringFlag{
							Name:  flagKind,
							Usage: "Kind: investigating, identified, monitoring, resolved, maintenance, info",
						},
						&cli.StringFlag{Name: flagSection, Usage: "Related section UID"},
						&cli.StringFlag{Name: flagCheck, Usage: "Related check UID"},
						&cli.StringFlag{Name: flagIncident, Usage: "Related incident UID"},
						&cli.StringFlag{Name: flagLink, Usage: "Optional link URL"},
					},
					Action: statusUpdatesUpdateAction,
				},
				{
					Name:      flagRemove,
					Aliases:   []string{"rm", cmdDelete},
					Usage:     "Remove a status update",
					ArgsUsage: argUID,
					Action:    statusUpdatesRemoveAction,
				},
			},
		},
		maintenanceWindowsCommand(),
		checkGroupsCommand(),
		severitiesCommand(),
		labelsCommand(),
		regionsCommand(),
		checkTypesCommand(),
		oncallCommand(),
		escalationPoliciesCommand(),
		notificationsCommand(),
		notificationRoutesCommand(),
		notificationContactsCommand(),
		orgsCommand(),
		invitationsCommand(),
		membershipRequestsCommand(),
		entitlementsCommand(),
		filesCommand(),
		emailSuppressionsCommand(),
	}
}

// maintenanceWindowsCommand builds the "maintenance-windows" command group.
func maintenanceWindowsCommand() *cli.Command {
	return &cli.Command{
		Name:    "maintenance-windows",
		Aliases: []string{"maintenance-window", "mw"},
		Usage:   "Manage scheduled maintenance windows",
		Flags:   GetGlobalFlags(),
		Commands: []*cli.Command{
			{
				Name:  flagList,
				Usage: "List maintenance windows",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: flagStatus, Usage: "Filter by status (active, upcoming, past)"},
					&cli.IntFlag{Name: flagLimit, Usage: "Maximum results (1-100)"},
				},
				Action: maintenanceWindowsListAction,
			},
			{
				Name:  cmdCreate,
				Usage: "Create a maintenance window",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: flagTitle, Usage: "Window title", Required: true},
					&cli.StringFlag{Name: flagDescription, Usage: usageOptionalDescription},
					&cli.StringFlag{Name: flagStart, Usage: usageRFC3339, Required: true},
					&cli.StringFlag{Name: flagEnd, Usage: usageRFC3339, Required: true},
					&cli.StringFlag{Name: flagRecurrence, Usage: "Recurrence: none, daily, weekly, monthly"},
					&cli.StringFlag{Name: flagRecurrenceEnd, Usage: "Recurrence end (" + usageRFC3339 + ")"},
				},
				Action: maintenanceWindowsCreateAction,
			},
			{
				Name:      flagGet,
				Usage:     "Get a maintenance window",
				ArgsUsage: argUID,
				Action:    maintenanceWindowsGetAction,
			},
			{
				Name:      cmdUpdate,
				Usage:     "Update a maintenance window",
				ArgsUsage: argUID,
				Flags: []cli.Flag{
					&cli.StringFlag{Name: flagTitle, Usage: "Window title"},
					&cli.StringFlag{Name: flagDescription, Usage: usageOptionalDescription},
					&cli.StringFlag{Name: flagStart, Usage: usageRFC3339},
					&cli.StringFlag{Name: flagEnd, Usage: usageRFC3339},
					&cli.StringFlag{Name: flagRecurrence, Usage: "Recurrence: none, daily, weekly, monthly"},
					&cli.StringFlag{Name: flagRecurrenceEnd, Usage: "Recurrence end (" + usageRFC3339 + ")"},
				},
				Action: maintenanceWindowsUpdateAction,
			},
			{
				Name:      flagRemove,
				Aliases:   []string{"rm", cmdDelete},
				Usage:     "Remove a maintenance window",
				ArgsUsage: argUID,
				Action:    maintenanceWindowsRemoveAction,
			},
			{
				Name:  "checks",
				Usage: "Manage the checks covered by a maintenance window",
				Commands: []*cli.Command{
					{
						Name:      flagList,
						Usage:     "List covered checks",
						ArgsUsage: argUID,
						Action:    maintenanceWindowsChecksListAction,
					},
					{
						Name:      cmdSet,
						Usage:     "Set covered checks (pass check uids as args, groups via --group)",
						ArgsUsage: "<uid> <check-uid>...",
						Flags: []cli.Flag{
							&cli.StringSliceFlag{Name: flagGroup, Usage: "Check group UID (repeatable)"},
						},
						Action: maintenanceWindowsChecksSetAction,
					},
				},
			},
		},
	}
}

// checkGroupsCommand builds the "check-groups" command group.
func checkGroupsCommand() *cli.Command {
	return &cli.Command{
		Name:    "check-groups",
		Aliases: []string{"check-group"},
		Usage:   "Manage check groups",
		Flags:   GetGlobalFlags(),
		Commands: []*cli.Command{
			{
				Name:   flagList,
				Usage:  "List check groups",
				Action: checkGroupsListAction,
			},
			{
				Name:  cmdCreate,
				Usage: "Create a check group",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: flagName, Usage: usageHumanReadableName, Required: true},
					&cli.StringFlag{Name: keySlug, Usage: usageSlug},
					&cli.StringFlag{Name: flagDescription, Usage: usageOptionalDescription},
					&cli.IntFlag{Name: flagSortOrder, Usage: "Sort order"},
				},
				Action: checkGroupsCreateAction,
			},
			{
				Name:      flagGet,
				Usage:     "Get a check group",
				ArgsUsage: argUIDSlug,
				Action:    checkGroupsGetAction,
			},
			{
				Name:      cmdUpdate,
				Usage:     "Update a check group",
				ArgsUsage: argUIDSlug,
				Flags: []cli.Flag{
					&cli.StringFlag{Name: flagName, Usage: usageHumanReadableName},
					&cli.StringFlag{Name: keySlug, Usage: usageSlug},
					&cli.StringFlag{Name: flagDescription, Usage: usageOptionalDescription},
					&cli.IntFlag{Name: flagSortOrder, Usage: "Sort order"},
				},
				Action: checkGroupsUpdateAction,
			},
			{
				Name:      flagRemove,
				Aliases:   []string{"rm", cmdDelete},
				Usage:     "Remove a check group",
				ArgsUsage: argUIDSlug,
				Action:    checkGroupsRemoveAction,
			},
		},
	}
}

// severitiesCommand builds the "severities" command group.
func severitiesCommand() *cli.Command {
	return &cli.Command{
		Name:    "severities",
		Aliases: []string{"severity"},
		Usage:   "Manage severities (channel-set bundles)",
		Flags:   GetGlobalFlags(),
		Commands: []*cli.Command{
			{
				Name:   flagList,
				Usage:  "List severities",
				Action: severitiesListAction,
			},
			{
				Name:  cmdCreate,
				Usage: "Create a severity",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: flagName, Usage: usageHumanReadableName, Required: true},
					&cli.StringFlag{Name: keySlug, Usage: usageSlug},
					&cli.StringFlag{Name: flagDescription, Usage: usageOptionalDescription},
					&cli.StringSliceFlag{Name: flagChannels, Usage: "Channel type (repeatable)"},
					&cli.BoolFlag{Name: flagDefault, Usage: "Mark as the default severity"},
				},
				Action: severitiesCreateAction,
			},
			{
				Name:      flagGet,
				Usage:     "Get a severity",
				ArgsUsage: argUIDSlug,
				Action:    severitiesGetAction,
			},
			{
				Name:      cmdUpdate,
				Usage:     "Update a severity",
				ArgsUsage: argUIDSlug,
				Flags: []cli.Flag{
					&cli.StringFlag{Name: flagName, Usage: usageHumanReadableName},
					&cli.StringFlag{Name: keySlug, Usage: usageSlug},
					&cli.StringFlag{Name: flagDescription, Usage: usageOptionalDescription},
					&cli.StringSliceFlag{Name: flagChannels, Usage: "Channel type (repeatable)"},
					&cli.BoolFlag{Name: flagDefault, Usage: "Mark as the default severity"},
					&cli.BoolFlag{Name: flagNoDefault, Usage: "Unmark as the default severity"},
				},
				Action: severitiesUpdateAction,
			},
			{
				Name:      flagRemove,
				Aliases:   []string{"rm", cmdDelete},
				Usage:     "Remove a severity",
				ArgsUsage: argUIDSlug,
				Action:    severitiesRemoveAction,
			},
		},
	}
}

// labelsCommand builds the "labels" command group.
func labelsCommand() *cli.Command {
	return &cli.Command{
		Name:  "labels",
		Usage: "List organization label keys and values",
		Flags: GetGlobalFlags(),
		Commands: []*cli.Command{
			{
				Name:  flagList,
				Usage: "List label keys, or values for a given --key",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: flagKey, Usage: "List values for this key instead of keys"},
					&cli.StringFlag{Name: flagLabelQuery, Usage: "Prefix filter on keys/values"},
					&cli.IntFlag{Name: flagLimit, Usage: usageResultsLimit},
				},
				Action: labelsListAction,
			},
		},
	}
}

// regionsCommand builds the "regions" command group.
func regionsCommand() *cli.Command {
	return &cli.Command{
		Name:  "regions",
		Usage: "List available monitoring regions",
		Flags: GetGlobalFlags(),
		Commands: []*cli.Command{
			{
				Name:   flagList,
				Usage:  "List regions available to the organization",
				Action: regionsListAction,
			},
		},
	}
}

// checkTypesCommand builds the "check-types" command group.
func checkTypesCommand() *cli.Command {
	return &cli.Command{
		Name:    "check-types",
		Aliases: []string{"check-type"},
		Usage:   "Inspect the check type catalog and sample configurations",
		Flags:   GetGlobalFlags(),
		Commands: []*cli.Command{
			{
				Name:   flagList,
				Usage:  "List check types with activation status",
				Action: checkTypesListAction,
			},
			{
				Name:  "samples",
				Usage: "List sample configurations grouped by check type",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: flagType, Usage: "Filter samples to a single check type"},
				},
				Action: checkTypesSamplesAction,
			},
		},
	}
}

// oncallScheduleFlags returns the create/update flag set for a schedule. All
// flags are optional here; required-field enforcement lives in the actions so
// that update can leave any subset unchanged.
func oncallScheduleFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: flagName, Usage: usageHumanReadableName},
		&cli.StringFlag{Name: flagDescription, Usage: usageOptionalDescription},
		&cli.StringFlag{Name: flagTimezone, Usage: "IANA timezone (e.g. Europe/Paris)"},
		&cli.StringFlag{Name: flagRotationType, Usage: "Rotation type: daily or weekly"},
		&cli.StringFlag{Name: flagHandoffTime, Usage: "Handoff time of day, HH:MM (24h)"},
		&cli.IntFlag{Name: flagHandoffWeekday, Usage: "Handoff weekday 0 (Mon)..6 (Sun); weekly only"},
		&cli.StringFlag{Name: flagStart, Usage: "Rotation start (" + usageRFC3339 + ")"},
		&cli.StringSliceFlag{Name: flagUser, Usage: "Roster user UID (repeatable)"},
	}
}

// oncallCommand builds the "oncall" command group.
func oncallCommand() *cli.Command {
	return &cli.Command{
		Name:    "oncall",
		Aliases: []string{"on-call", "on-call-schedules"},
		Usage:   "Manage on-call schedules, overrides, and iCal feeds",
		Flags:   GetGlobalFlags(),
		Commands: []*cli.Command{
			{
				Name:   flagList,
				Usage:  "List on-call schedules",
				Action: oncallListAction,
			},
			{
				Name:   cmdCreate,
				Usage:  "Create an on-call schedule",
				Flags:  oncallScheduleFlags(),
				Action: oncallCreateAction,
			},
			{
				Name:      flagGet,
				Usage:     "Get an on-call schedule",
				ArgsUsage: argUID,
				Action:    oncallGetAction,
			},
			{
				Name:      cmdUpdate,
				Usage:     "Update an on-call schedule",
				ArgsUsage: argUID,
				Flags:     oncallScheduleFlags(),
				Action:    oncallUpdateAction,
			},
			{
				Name:      flagRemove,
				Aliases:   []string{"rm", cmdDelete},
				Usage:     "Remove an on-call schedule",
				ArgsUsage: argUID,
				Action:    oncallRemoveAction,
			},
			{
				Name:      "preview",
				Usage:     "Preview the rotation over a window",
				ArgsUsage: argUID,
				Flags: []cli.Flag{
					&cli.StringFlag{Name: flagFrom, Usage: "Window start (" + usageRFC3339 + ")"},
					&cli.IntFlag{Name: flagDays, Usage: "Window length in days (1-365, default 14)"},
				},
				Action: oncallPreviewAction,
			},
			oncallOverridesCommand(),
			oncallICalCommand(),
		},
	}
}

// oncallOverridesCommand builds the "oncall overrides" subcommand group.
func oncallOverridesCommand() *cli.Command {
	return &cli.Command{
		Name:  "overrides",
		Usage: "Manage overrides on an on-call schedule",
		Commands: []*cli.Command{
			{
				Name:      flagList,
				Usage:     "List overrides",
				ArgsUsage: argUID,
				Flags: []cli.Flag{
					&cli.StringFlag{Name: flagFrom, Usage: "Only overrides ending after (" + usageRFC3339 + ")"},
					&cli.StringFlag{Name: flagUntil, Usage: "Only overrides starting before (" + usageRFC3339 + ")"},
				},
				Action: oncallOverridesListAction,
			},
			{
				Name:      cmdCreate,
				Usage:     "Create an override",
				ArgsUsage: argUID,
				Flags: []cli.Flag{
					&cli.StringFlag{Name: flagUser, Usage: "User UID to page during the override", Required: true},
					&cli.StringFlag{Name: flagStart, Usage: usageRFC3339, Required: true},
					&cli.StringFlag{Name: flagEnd, Usage: usageRFC3339, Required: true},
					&cli.StringFlag{Name: flagReason, Usage: "Optional reason"},
				},
				Action: oncallOverridesCreateAction,
			},
			{
				Name:      flagRemove,
				Aliases:   []string{"rm", cmdDelete},
				Usage:     "Remove an override",
				ArgsUsage: "<schedule-uid> <override-uid>",
				Action:    oncallOverridesRemoveAction,
			},
		},
	}
}

// oncallICalCommand builds the "oncall ical" subcommand group.
func oncallICalCommand() *cli.Command {
	return &cli.Command{
		Name:  "ical",
		Usage: "Manage the iCal feed for an on-call schedule",
		Commands: []*cli.Command{
			{
				Name:      "enable",
				Usage:     "Enable the iCal feed",
				ArgsUsage: argUID,
				Action:    oncallICalEnableAction,
			},
			{
				Name:      "disable",
				Usage:     "Disable the iCal feed",
				ArgsUsage: argUID,
				Action:    oncallICalDisableAction,
			},
			{
				Name:      "rotate",
				Usage:     "Rotate the iCal feed secret",
				ArgsUsage: argUID,
				Action:    oncallICalRotateAction,
			},
			{
				Name:      "feed",
				Usage:     "Fetch the raw public iCal feed by its secret",
				ArgsUsage: "<secret>",
				Action:    oncallICalFeedAction,
			},
		},
	}
}

// notificationsCommand builds the "notifications" command group. It reads the
// notification-delivery audit at org, per-incident, per-user, and caller scopes.
func notificationsCommand() *cli.Command {
	return &cli.Command{
		Name:    "notifications",
		Aliases: []string{"notification", "notif"},
		Usage:   "Inspect notification deliveries (org, incident, user, or self scope)",
		Flags:   GetGlobalFlags(),
		Commands: []*cli.Command{
			{
				Name: flagList,
				Usage: "List notification deliveries. Defaults to the caller; " +
					"use --incident, --connection, or --user to change scope",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: flagIncident, Usage: "Scope to an incident UID"},
					&cli.StringFlag{Name: flagConnection, Usage: "Scope to a connection (channel) UID"},
					&cli.StringFlag{Name: flagUser, Usage: "Scope to a user UID (admin only)"},
					&cli.StringFlag{Name: flagStatus, Usage: "Filter by status (sent, failed, skipped, ...)"},
					&cli.IntFlag{Name: flagLimit, Usage: "Maximum rows to return"},
					&cli.StringFlag{Name: flagBefore, Usage: "Only rows before this " + usageRFC3339},
				},
				Action: notificationsListAction,
			},
			{
				Name:      flagGet,
				Usage:     "Get a notification delivery by UID",
				ArgsUsage: argUID,
				Flags: []cli.Flag{
					&cli.StringFlag{Name: flagIncident, Usage: "Scope the lookup to an incident UID"},
				},
				Action: notificationsGetAction,
			},
		},
	}
}

// notificationRoutesCommand builds the "notification-routes" command group for
// the caller's own delivery routes.
func notificationRoutesCommand() *cli.Command {
	return &cli.Command{
		Name:    "notification-routes",
		Aliases: []string{"notification-route"},
		Usage:   "Manage your own notification routes",
		Flags:   GetGlobalFlags(),
		Commands: []*cli.Command{
			{
				Name:   flagList,
				Usage:  "List your notification routes",
				Action: notificationRoutesListAction,
			},
			{
				Name:      cmdUpdate,
				Usage:     "Enable/disable a route or reorder the full route list",
				ArgsUsage: argUID,
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: flagEnabled, Usage: "Enable the route"},
					&cli.BoolFlag{Name: flagDisabled, Usage: "Disable the route"},
					&cli.StringFlag{Name: flagOrder, Usage: "Full ordered list of route UIDs (comma-separated)"},
				},
				Action: notificationRoutesUpdateAction,
			},
			{
				Name:      cmdTest,
				Usage:     "Send a test notification through a route",
				ArgsUsage: argUID,
				Action:    notificationRoutesTestAction,
			},
		},
	}
}

// notificationContactsCommand builds the "notification-contacts" command group
// for the caller's own delivery contacts.
func notificationContactsCommand() *cli.Command {
	return &cli.Command{
		Name:    "notification-contacts",
		Aliases: []string{"notification-contact"},
		Usage:   "Manage your own notification contacts",
		Flags:   GetGlobalFlags(),
		Commands: []*cli.Command{
			{
				Name:   flagList,
				Usage:  "List your notification contacts",
				Action: notificationContactsListAction,
			},
			{
				Name:  flagAdd,
				Usage: "Add a notification contact",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:     flagType,
						Usage:    "Contact type (email, phone, slack_user, web_push, ...)",
						Required: true,
					},
					&cli.StringFlag{Name: flagValue, Usage: "Contact value (address, number, ...)", Required: true},
					&cli.StringFlag{Name: flagLabel, Usage: "Optional label"},
				},
				Action: notificationContactsAddAction,
			},
			{
				Name:      flagRemove,
				Aliases:   []string{"rm", cmdDelete},
				Usage:     "Remove a notification contact",
				ArgsUsage: argUID,
				Action:    notificationContactsRemoveAction,
			},
		},
	}
}

// escalationPoliciesCommand builds the "escalation-policies" command group.
func escalationPoliciesCommand() *cli.Command {
	return &cli.Command{
		Name:    "escalation-policies",
		Aliases: []string{"escalation-policy", "escalation"},
		Usage:   "Manage escalation policies",
		Flags:   GetGlobalFlags(),
		Commands: []*cli.Command{
			{
				Name:   flagList,
				Usage:  "List escalation policies",
				Action: escalationPoliciesListAction,
			},
			{
				Name:  cmdCreate,
				Usage: "Create an escalation policy",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: flagName, Usage: usageHumanReadableName, Required: true},
					&cli.StringFlag{Name: flagDescription, Usage: usageOptionalDescription},
					&cli.IntFlag{Name: flagRepeatMax, Usage: "Times to repeat the whole policy (>=0)"},
					&cli.IntFlag{Name: flagRepeatAfter, Usage: "Seconds between repeats (required when repeat-max > 0)"},
					&cli.StringFlag{Name: flagStepsJSON, Usage: usageStepsJSON},
				},
				Action: escalationPoliciesCreateAction,
			},
			{
				Name:      flagGet,
				Usage:     "Get an escalation policy",
				ArgsUsage: argUID,
				Action:    escalationPoliciesGetAction,
			},
			{
				Name:      cmdUpdate,
				Usage:     "Update an escalation policy",
				ArgsUsage: argUID,
				Flags: []cli.Flag{
					&cli.StringFlag{Name: flagName, Usage: usageHumanReadableName},
					&cli.StringFlag{Name: flagDescription, Usage: usageOptionalDescription},
					&cli.IntFlag{Name: flagRepeatMax, Usage: "Times to repeat the whole policy (>=0)"},
					&cli.IntFlag{Name: flagRepeatAfter, Usage: "Seconds between repeats"},
					&cli.StringFlag{Name: flagStepsJSON, Usage: usageStepsJSON},
				},
				Action: escalationPoliciesUpdateAction,
			},
			{
				Name:      flagRemove,
				Aliases:   []string{"rm", cmdDelete},
				Usage:     "Remove an escalation policy",
				ArgsUsage: argUID,
				Action:    escalationPoliciesRemoveAction,
			},
		},
	}
}
