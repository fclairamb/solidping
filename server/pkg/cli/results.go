package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/urfave/cli/v3"

	"github.com/fclairamb/solidping/server/pkg/cli/output"
	"github.com/fclairamb/solidping/server/pkg/client"
)

// resultsListAction handles listing check results using the new organization-wide endpoint
//
//nolint:funlen,gocognit,cyclop // CLI parameter parsing and output formatting
func resultsListAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	// Get API client
	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	// Check if auto mode is enabled
	autoMode := cmd.Bool("auto")

	// Collect all results (for auto mode)
	var allResults []client.OrgResult
	currentCursor := cmd.String("cursor")
	pageCount := 0

	// If auto mode is enabled, ignore the initial cursor
	if autoMode {
		currentCursor = ""
	}

	for {
		// Build query parameters
		params := &client.ListOrgResultsParams{}

		if check := cmd.String(flagCheck); check != "" {
			params.CheckUid = &check
		}
		if checkType := cmd.String("check-type"); checkType != "" {
			params.CheckType = &checkType
		}
		if status := cmd.String(flagStatus); status != "" {
			params.Status = &status
		}
		if region := cmd.String("region"); region != "" {
			params.Region = &region
		}
		if periodType := cmd.String("period-type"); periodType != "" {
			params.PeriodType = &periodType
		}
		if currentCursor != "" {
			params.Cursor = &currentCursor
		}
		if size := cmd.Int(flagSize); size > 0 {
			params.Size = &size
		}
		if with := cmd.String("with"); with != "" {
			params.With = &with
		}

		// Call API
		resp, err := apiClient.ListOrgResultsWithResponse(ctx, cliCtx.GetOrg(), params)
		if err != nil {
			return cliCtx.HandleError("Failed to list results", err)
		}

		if resp.StatusCode() != 200 || resp.JSON200 == nil {
			return cliCtx.HandleStatusError("Failed to list results", resp.StatusCode())
		}

		// Add results to collection
		if resp.JSON200.Data != nil && len(*resp.JSON200.Data) > 0 {
			allResults = append(allResults, *resp.JSON200.Data...)
			pageCount++

			if autoMode {
				// Show progress in auto mode
				if cliCtx.IsText() {
					fmt.Fprintf(os.Stderr, "\rFetching page %d... (%d results)", pageCount, len(allResults))
				}
			}
		}

		// Check if there are more pages
		hasMore := resp.JSON200.Pagination != nil &&
			resp.JSON200.Pagination.Cursor != nil &&
			*resp.JSON200.Pagination.Cursor != ""

		// If not in auto mode, or no more pages, break
		if !autoMode || !hasMore {
			if autoMode && cliCtx.IsText() {
				fmt.Fprintf(os.Stderr, "\n")
			}
			break
		}

		// Update cursor for next iteration
		currentCursor = *resp.JSON200.Pagination.Cursor
	}

	// Non-text output
	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(map[string]interface{}{
			"data": allResults,
			"pagination": map[string]interface{}{
				"total": len(allResults),
				"pages": pageCount,
			},
		})
	}

	// Table output
	if len(allResults) == 0 {
		output.PrintMessage(os.Stdout, "No results found")
		return nil
	}

	// Determine which columns to show based on --with flag
	withParam := cmd.String("with")
	showDuration := strings.Contains(withParam, "durationMs")
	showRegion := strings.Contains(withParam, "region")
	showCheckSlug := strings.Contains(withParam, "checkSlug")

	// Build table header dynamically
	headers := []table.Row{{colTimestamp, colStatus}}
	if showCheckSlug {
		headers[0] = append(headers[0], "CHECK")
	} else {
		headers[0] = append(headers[0], "CHECK UID")
	}
	if showDuration {
		headers[0] = append(headers[0], "DURATION (ms)")
	}
	if showRegion {
		headers[0] = append(headers[0], "REGION")
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(headers[0])

	for i := range allResults {
		result := &allResults[i]
		timestamp := ""
		if result.PeriodStart != nil {
			timestamp = result.PeriodStart.Format(time.RFC3339)
		}

		status := "unknown"
		if result.Status != nil {
			status = string(*result.Status)
		}

		row := table.Row{timestamp, status}

		// Check column
		switch {
		case showCheckSlug && result.CheckSlug != nil:
			row = append(row, *result.CheckSlug)
		case result.CheckUid != nil:
			row = append(row, result.CheckUid.String())
		default:
			row = append(row, "")
		}

		// Duration column
		if showDuration {
			if result.DurationMs != nil {
				row = append(row, fmt.Sprintf("%.2f", *result.DurationMs))
			} else {
				row = append(row, "")
			}
		}

		// Region column
		if showRegion {
			if result.Region != nil {
				row = append(row, *result.Region)
			} else {
				row = append(row, "")
			}
		}

		tbl.AppendRow(row)
	}

	tbl.Render()

	// Print summary
	if autoMode {
		msg := fmt.Sprintf("\nTotal: %d results across %d page(s)", len(allResults), pageCount)
		output.PrintMessage(os.Stdout, msg)
	} else {
		msg := fmt.Sprintf("\nShowing %d results", len(allResults))
		output.PrintMessage(os.Stdout, msg)
	}

	return nil
}

// resultsGetAction fetches a single result by check + result UID.
//
//nolint:cyclop // CLI parameter parsing and field-by-field output
func resultsGetAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	if cmd.Args().Len() < 2 {
		return cli.Exit("Error: <check> <result-uid> are required", 5)
	}

	check := cmd.Args().Get(0)

	resultUID, err := uuid.Parse(cmd.Args().Get(1))
	if err != nil {
		return cli.Exit("Error: invalid result UID: "+err.Error(), 5)
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	params := &client.GetOrgResultParams{}
	if region := cmd.String("region"); region != "" {
		params.Region = &region
	}

	resp, err := apiClient.GetOrgResultWithResponse(ctx, cliCtx.GetOrg(), check, resultUID, params)
	if err != nil {
		return cliCtx.HandleError("Failed to get result", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to get result", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	result := resp.JSON200

	if result.Uid != nil {
		output.PrintMessage(os.Stdout, "UID:          "+result.Uid.String())
	}
	if result.Status != nil {
		output.PrintMessage(os.Stdout, "Status:       "+string(*result.Status))
	}
	if result.CheckSlug != nil {
		output.PrintMessage(os.Stdout, "Check:        "+*result.CheckSlug)
	} else if result.CheckUid != nil {
		output.PrintMessage(os.Stdout, "Check UID:    "+result.CheckUid.String())
	}
	if result.PeriodType != nil {
		output.PrintMessage(os.Stdout, "Period Type:  "+*result.PeriodType)
	}
	if result.PeriodStart != nil {
		output.PrintMessage(os.Stdout, "Period Start: "+result.PeriodStart.Format(time.RFC3339))
	}
	if result.Region != nil {
		output.PrintMessage(os.Stdout, "Region:       "+*result.Region)
	}
	if result.DurationMs != nil {
		output.PrintMessage(os.Stdout, fmt.Sprintf("Duration:     %.2f ms", *result.DurationMs))
	}
	if result.Fallback != nil {
		output.PrintMessage(os.Stdout, "(served from an aggregated fallback row)")
	}

	return nil
}
