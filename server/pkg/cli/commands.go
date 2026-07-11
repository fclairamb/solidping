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
					Usage: "Login via browser (default), or with --email/--password " +
						"or a pasted --token",
					Flags: []cli.Flag{
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
							Name:    "token",
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
							Name:  "slug",
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
					Name:      "update",
					Usage:     "Update a check",
					ArgsUsage: argUIDSlug,
					Flags: []cli.Flag{
						&cli.StringFlag{
							Name:  flagName,
							Usage: usageHumanReadableName,
						},
						&cli.StringFlag{
							Name:  "slug",
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
					Name:  "validate",
					Usage: "Validate a check definition without persisting it",
					Flags: []cli.Flag{
						&cli.StringFlag{
							Name:    "file",
							Aliases: []string{"f"},
							Usage:   "Check definition file (JSON or YAML); reads stdin if omitted",
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
							Name:  "slug",
							Usage: "Slug for the cloned check (auto-generated if omitted)",
						},
						&cli.StringFlag{
							Name:  "description",
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
					Aliases:   []string{"rm", "delete"},
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
							ArgsUsage: "<child-slug> <parent-slug>",
							Flags: []cli.Flag{
								&cli.StringFlag{
									Name:  "kind",
									Usage: "Dependency kind: hard or soft",
									Value: depKindHard,
								},
								&cli.StringFlag{
									Name:  "description",
									Usage: "Optional description for the edge",
								},
							},
							Action: checksDepsAddAction,
						},
						{
							Name:      flagRemove,
							Aliases:   []string{"rm"},
							Usage:     "Drop a parent dependency",
							ArgsUsage: "<child-slug> <parent-slug>",
							Action:    checksDepsRemoveAction,
						},
						{
							Name:      "set",
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
							Name:      "update",
							Usage:     "Update a parent dependency edge (kind/description)",
							ArgsUsage: "<child-slug> <parent-slug>",
							Flags: []cli.Flag{
								&cli.StringFlag{
									Name:  "kind",
									Usage: "Dependency kind: hard or soft",
								},
								&cli.StringFlag{
									Name:  "description",
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
					Name:  "export",
					Usage: "Export all checks as a portable JSON document (admin-only)",
					Flags: []cli.Flag{
						&cli.StringFlag{
							Name:  "file",
							Usage: "Write the export to a file instead of stdout",
						},
					},
					Action: checksExportAction,
				},
				{
					Name:      "import",
					Usage:     "Import checks from an export document (idempotent upsert, admin-only)",
					ArgsUsage: "<file>",
					Flags: []cli.Flag{
						&cli.BoolFlag{
							Name:  "dry-run",
							Usage: "Preview created/updated counts without mutating",
						},
					},
					Action: checksImportAction,
				},
			},
		},
		{
			Name:      "apply",
			Usage:     "Reconcile checks against a declarative manifest (config-as-code, admin-only)",
			ArgsUsage: "<manifest>",
			Flags: append(GetGlobalFlags(),
				&cli.StringFlag{
					Name:    "file",
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
			Aliases: []string{"token"},
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
					Name:  "create",
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
					Name:      "update",
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
					Name:  "create",
					Usage: "Create a job",
					Flags: []cli.Flag{
						&cli.StringFlag{
							Name:     "type",
							Usage:    "Job type",
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
					Name:      "cancel",
					Usage:     "Cancel a job",
					ArgsUsage: argUID,
					Action:    jobsCancelAction,
				},
			},
		},
		{
			Name:    "system",
			Aliases: []string{"sys"},
			Usage:   "Manage system parameters",
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
					Name:      "set",
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
					Name:      "delete",
					Aliases:   []string{"rm"},
					Usage:     "Delete a system parameter",
					ArgsUsage: "<key>",
					Action:    systemDeleteAction,
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
							Name:  "create",
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
							Name:      "cancel",
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
							Name:  "token",
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
	}
}
