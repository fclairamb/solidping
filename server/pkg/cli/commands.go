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
					Name:  "login",
					Usage: "Login and obtain session token",
					Flags: []cli.Flag{
						&cli.StringFlag{
							Name:    "email",
							Aliases: []string{"e"},
							Usage:   "Email for authentication",
							Sources: cli.EnvVars("SOLIDPING_EMAIL"),
						},
						&cli.StringFlag{
							Name:    "password",
							Aliases: []string{"p"},
							Usage:   "Password for authentication",
							Sources: cli.EnvVars("SOLIDPING_PASSWORD"),
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
					Name:      "add",
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
					Name:      "remove",
					Aliases:   []string{"rm", "delete"},
					Usage:     "Remove a check",
					ArgsUsage: argUIDSlug,
					Action:    checksRemoveAction,
				},
			},
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
	}
}
