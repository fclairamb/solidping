package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/urfave/cli/v3"

	"github.com/fclairamb/solidping/server/pkg/cli/output"
	"github.com/fclairamb/solidping/server/pkg/client"
)

var (
	// ErrCheckNotFound is returned when a check is not found.
	ErrCheckNotFound = errors.New("check not found")
	// ErrFailedToListChecks is returned when listing checks fails.
	ErrFailedToListChecks = errors.New("failed to list checks")
	// ErrFailedToCreateCheck is returned when creating a check fails.
	ErrFailedToCreateCheck = errors.New("failed to create check")
	// ErrFailedToRemoveCheck is returned when removing a check fails.
	ErrFailedToRemoveCheck = errors.New("failed to remove check")
	// ErrAPIError is returned when an API call fails.
	ErrAPIError = errors.New("API error")
)

// formatDuration formats a duration in a human-readable short form like "3d", "2h", "5m".
func formatDuration(duration time.Duration) string {
	if duration < 0 {
		duration = -duration
	}

	days := int(duration.Hours() / 24)
	hours := int(duration.Hours()) % 24
	minutes := int(duration.Minutes()) % 60

	if days > 0 {
		return fmt.Sprintf("%dd", days)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh", hours)
	}
	if minutes > 0 {
		return fmt.Sprintf("%dm", minutes)
	}
	return "< 1m"
}

// formatAPIError formats an API error response with detailed information.
func formatAPIError(err *client.Error, statusCode int) string {
	if err == nil {
		return fmt.Sprintf("Request failed (status: %d)", statusCode)
	}

	msg := err.Title

	// Add error code if available
	if err.Code != "" {
		msg = fmt.Sprintf("%s [%s]", msg, err.Code)
	}

	// Add detail if available
	if err.Detail != nil && *err.Detail != "" {
		msg = fmt.Sprintf("%s\n  Detail: %s", msg, *err.Detail)
	}

	// Add field-level validation errors if present
	if err.Fields != nil && len(*err.Fields) > 0 {
		msg += "\n  Validation errors:"
		for i := range *err.Fields {
			msg = fmt.Sprintf("%s\n    - %s: %s", msg, (*err.Fields)[i].Name, (*err.Fields)[i].Message)
		}
	}

	return msg
}

// checksListAction handles listing all checks
//
//nolint:gocognit,cyclop,funlen // CLI output formatting requires conditional logic
func checksListAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	// Get API client
	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		if !cliCtx.IsText() {
			return cliCtx.Outputter.PrintError(err)
		}
		output.PrintError(os.Stdout, err.Error())
		return cli.Exit("", 3)
	}

	// List checks
	params := &client.ListChecksParams{}

	// Add with parameters
	withParams := []string{"last_status_change"}
	if cmd.Bool("with-last-result") {
		withParams = append(withParams, "last_result")
	}
	withParam := strings.Join(withParams, ",")
	params.With = &withParam

	// Set internal filter
	if cmd.Bool("all") {
		internalParam := "all"
		params.Internal = &internalParam
	} else if cmd.Bool("internal") {
		internalParam := "true"
		params.Internal = &internalParam
	}

	resp, err := apiClient.ListChecksWithResponse(ctx, cliCtx.GetOrg(), params)
	if err != nil {
		if !cliCtx.IsText() {
			return cliCtx.Outputter.PrintError(err)
		}
		output.PrintError(os.Stdout, fmt.Sprintf("Failed to list checks: %v", err))
		return cli.Exit("", 1)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		if !cliCtx.IsText() {
			return cliCtx.Outputter.PrintError(fmt.Errorf("%w (status: %d)", ErrFailedToListChecks, resp.StatusCode()))
		}
		output.PrintError(os.Stdout, fmt.Sprintf("Failed to list checks (status: %d)", resp.StatusCode()))
		return cli.Exit("", 1)
	}

	// Output checks
	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	// Print as table
	if resp.JSON200.Data == nil || len(*resp.JSON200.Data) == 0 {
		output.PrintMessage(os.Stdout, "No checks found")
		return nil
	}

	tbl := output.NewTable(os.Stdout)

	// Add columns based on whether last result is included
	if cmd.Bool("with-last-result") {
		tbl.AppendHeader(table.Row{"SLUG", colName, colType, "PERIOD", "ENABLED", "LAST_STATUS", "LAST_CHECKED"})
	} else {
		tbl.AppendHeader(table.Row{"SLUG", colName, colType, "PERIOD", "ENABLED", colStatus})
	}

	data := *resp.JSON200.Data
	for i := range data {
		check := &data[i]
		enabled := boolYes
		if check.Enabled != nil && !*check.Enabled {
			enabled = boolNo
		}

		// Format status with duration if available
		status := ""
		if check.LastStatusChange != nil && check.LastStatusChange.Status != nil && check.LastStatusChange.Time != nil {
			statusStr := string(*check.LastStatusChange.Status)
			duration := time.Since(*check.LastStatusChange.Time)
			status = fmt.Sprintf("%s (%s)", statusStr, formatDuration(duration))
		} else if check.LastResult != nil && check.LastResult.Status != nil {
			status = string(*check.LastResult.Status)
		}

		slug := ""
		if check.Slug != nil {
			slug = *check.Slug
		}

		name := ""
		if check.Name != nil {
			name = *check.Name
		}

		checkType := ""
		if check.Type != nil {
			checkType = string(*check.Type)
		}

		period := ""
		if check.Period != nil {
			period = *check.Period
		}

		if cmd.Bool("with-last-result") { //nolint:nestif // Conditional field inclusion
			lastStatus := ""
			lastChecked := ""
			if check.LastResult != nil {
				if check.LastResult.Status != nil {
					lastStatus = string(*check.LastResult.Status)
				}
				if check.LastResult.Timestamp != nil {
					lastChecked = check.LastResult.Timestamp.Format("2006-01-02 15:04:05")
				}
			}
			tbl.AppendRow(table.Row{slug, name, checkType, period, enabled, lastStatus, lastChecked})
		} else {
			tbl.AppendRow(table.Row{slug, name, checkType, period, enabled, status})
		}
	}

	tbl.Render()
	return nil
}

// bulkCheckResult represents the result of creating a single check in bulk operation.
type bulkCheckResult struct {
	UID    string `json:"uid,omitempty"`
	Slug   string `json:"slug,omitempty"`
	Name   string `json:"name,omitempty"`
	Status string `json:"status"` // "created" or "failed"
	Error  string `json:"error,omitempty"`
}

// bulkCheckResponse represents the complete result of a bulk check creation operation.
type bulkCheckResponse struct {
	Total   int               `json:"total"`
	Created int               `json:"created"`
	Failed  int               `json:"failed"`
	Checks  []bulkCheckResult `json:"checks"`
}

// generateCheckSlug generates a unique slug for a check at the given index.
func generateCheckSlug(baseSlug string, index int, total int) string {
	if total == 1 {
		return baseSlug
	}
	return fmt.Sprintf("%s-%d", baseSlug, index)
}

// generateCheckName generates a unique name for a check at the given index.
func generateCheckName(baseName string, index int, total int) string {
	if total == 1 {
		return baseName
	}
	return fmt.Sprintf("%s #%d", baseName, index)
}

// checksAddAction handles adding a new check or multiple checks in bulk
//
//nolint:cyclop,funlen,gocognit // CLI parameter handling and bulk creation logic
func checksAddAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	// Get URL from args
	if cmd.Args().Len() < 1 {
		return cli.Exit("Error: URL is required", 5)
	}
	url := cmd.Args().Get(0)

	// Get number of checks to create
	numChecks := cmd.Int("number")
	if numChecks < 1 || numChecks > 10000 {
		return cli.Exit("Error: Number of checks must be between 1 and 10,000", 5)
	}

	// Get API client
	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		if !cliCtx.IsText() {
			return cliCtx.Outputter.PrintError(err)
		}
		output.PrintError(os.Stdout, err.Error())
		return cli.Exit("", 3)
	}

	// Prepare common check parameters
	checkType := cmd.String("type")
	if checkType == "" {
		checkType = "http"
	}

	baseName := cmd.String("name")
	if baseName == "" {
		baseName = url
	}

	baseSlug := cmd.String("slug")

	period := cmd.String("interval")
	if period != "" {
		// Try parsing as Go duration (e.g., "30s", "5m", "1h")
		if d, err := time.ParseDuration(period); err == nil {
			// Convert to HH:MM:SS format
			hours := int(d.Hours())
			minutes := int(d.Minutes()) % 60
			seconds := int(d.Seconds()) % 60
			period = fmt.Sprintf("%02d:%02d:%02d", hours, minutes, seconds)
		}
		// If parsing fails, assume it's already in HH:MM:SS format and pass as-is
	}

	// Track results for bulk operations
	results := bulkCheckResponse{
		Total:  numChecks,
		Checks: make([]bulkCheckResult, 0, numChecks),
	}

	// Show progress indicator for large operations
	showProgress := numChecks > 10 && cliCtx.IsText()

	// Create checks sequentially
	for i := 1; i <= numChecks; i++ {
		// Generate unique slug and name for this check
		checkSlug := generateCheckSlug(baseSlug, i, numChecks)
		checkName := generateCheckName(baseName, i, numChecks)

		// Replace {nb} pattern in URL with current check number
		checkURL := strings.ReplaceAll(url, "{nb}", strconv.Itoa(i))

		// Create config map with URL for this specific check
		checkConfig := map[string]interface{}{
			"url": checkURL,
		}

		// Add timeout if provided
		if timeout := cmd.String("timeout"); timeout != "" {
			checkConfig["timeout"] = timeout
		}

		// Create check request
		enabled := true
		checkTypePtr := client.CreateCheckRequestType(checkType)
		req := client.CreateCheckRequest{
			Type:    &checkTypePtr,
			Name:    &checkName,
			Period:  &period,
			Enabled: &enabled,
			Config:  checkConfig,
		}
		// Only set slug if user provided one
		if checkSlug != "" {
			req.Slug = &checkSlug
		}

		// Show progress
		if showProgress {
			_, _ = fmt.Fprintf(os.Stdout, "\rCreating checks: %d/%d", i, numChecks)
		}

		// Create check
		resp, err := apiClient.CreateCheckWithResponse(ctx, cliCtx.GetOrg(), req)
		if err != nil {
			results.Failed++
			results.Checks = append(results.Checks, bulkCheckResult{
				Slug:   checkSlug,
				Name:   checkName,
				Status: keyFailed,
				Error:  err.Error(),
			})
			continue
		}

		if resp.StatusCode() != 201 || resp.JSON201 == nil {
			// Try to get detailed error from response
			var apiError *client.Error
			if resp.JSON422 != nil {
				apiError = resp.JSON422
			} else if len(resp.Body) > 0 {
				// Try to parse error from body for other status codes
				var errResp client.Error
				if err := json.Unmarshal(resp.Body, &errResp); err == nil {
					apiError = &errResp
				}
			}

			errorMsg := formatAPIError(apiError, resp.StatusCode())
			results.Failed++
			results.Checks = append(results.Checks, bulkCheckResult{
				Slug:   checkSlug,
				Name:   checkName,
				Status: keyFailed,
				Error:  errorMsg,
			})
			continue
		}

		// Record success
		results.Created++
		result := bulkCheckResult{
			Status: "created",
		}
		if resp.JSON201.Uid != nil {
			result.UID = resp.JSON201.Uid.String()
		}
		if resp.JSON201.Slug != nil {
			result.Slug = *resp.JSON201.Slug
		}
		if resp.JSON201.Name != nil {
			result.Name = *resp.JSON201.Name
		}
		results.Checks = append(results.Checks, result)
	}

	// Clear progress line
	if showProgress {
		_, _ = fmt.Fprintf(os.Stdout, "\r%s\r", strings.Repeat(" ", 50))
	}

	// Output results
	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(results)
	}

	// For single check, use original output format
	if numChecks == 1 {
		if results.Failed > 0 {
			output.PrintError(os.Stdout, results.Checks[0].Error)
			return cli.Exit("", 1)
		}
		output.PrintSuccess(os.Stdout, "Check created successfully:")
		output.PrintMessage(os.Stdout, "  UID: "+results.Checks[0].UID)
		output.PrintMessage(os.Stdout, "  Slug: "+results.Checks[0].Slug)
		output.PrintMessage(os.Stdout, "  Name: "+results.Checks[0].Name)
		return nil
	}

	// For bulk operations, show summary
	msg := fmt.Sprintf("Bulk check creation completed: %d/%d created", results.Created, results.Total)
	output.PrintSuccess(os.Stdout, msg)
	if results.Failed > 0 {
		output.PrintError(os.Stdout, fmt.Sprintf("%d checks failed to create", results.Failed))
		// List failed checks
		for i := range results.Checks {
			if results.Checks[i].Status == keyFailed {
				output.PrintMessage(os.Stdout, fmt.Sprintf("  - %s: %s", results.Checks[i].Slug, results.Checks[i].Error))
			}
		}
		return cli.Exit("", 1)
	}

	return nil
}

// checksRemoveAction handles removing a check.
func checksRemoveAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	// Get UID or slug from args
	if cmd.Args().Len() < 1 {
		return cli.Exit("Error: check UID or slug is required", 5)
	}
	identifier := cmd.Args().Get(0)

	// Get API client
	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		if !cliCtx.IsText() {
			return cliCtx.Outputter.PrintError(err)
		}
		output.PrintError(os.Stdout, err.Error())
		return cli.Exit("", 3)
	}

	// Delete check directly using UID or slug (API supports both)
	resp, err := apiClient.DeleteCheckWithResponse(ctx, cliCtx.GetOrg(), identifier)
	if err != nil {
		if !cliCtx.IsText() {
			return cliCtx.Outputter.PrintError(err)
		}
		output.PrintError(os.Stdout, fmt.Sprintf("Failed to remove check: %v", err))
		return cli.Exit("", 1)
	}

	if resp.StatusCode() != 200 && resp.StatusCode() != 204 {
		if !cliCtx.IsText() {
			return cliCtx.Outputter.PrintError(fmt.Errorf("%w (status: %d)", ErrFailedToRemoveCheck, resp.StatusCode()))
		}
		output.PrintError(os.Stdout, fmt.Sprintf("Failed to remove check (status: %d)", resp.StatusCode()))
		return cli.Exit("", 1)
	}

	// Output success
	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(map[string]interface{}{
			keyMessage: "Check removed successfully",
			"id":       identifier,
		})
	}

	output.PrintSuccess(os.Stdout, "Check removed successfully: "+identifier)
	return nil
}
