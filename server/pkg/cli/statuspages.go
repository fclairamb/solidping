package cli

import (
	"context"
	"os"
	"strconv"

	"github.com/google/uuid"
	"github.com/jedib0t/go-pretty/v6/table"
	openapi_types "github.com/oapi-codegen/runtime/types"
	"github.com/urfave/cli/v3"

	"github.com/fclairamb/solidping/server/pkg/cli/output"
	"github.com/fclairamb/solidping/server/pkg/client"
)

// Status-page CLI shared strings, kept as constants to satisfy goconst and keep
// flag names / column headers consistent across the status-page commands.
const (
	colVisibility = "VISIBILITY"
	colPosition   = "POSITION"
	colScope      = "SCOPE"
	colConfirmed  = "CONFIRMED"
	colEmail      = "EMAIL"

	flagVisibility       = "visibility"
	flagLanguage         = "language"
	flagCustomCSS        = "custom-css"
	flagCustomCSSFile    = "custom-css-file"
	flagHistoryPeriod    = "history-period"
	flagHistoryDays      = "history-days"
	flagShowAvailability = "show-availability"
	flagHideAvailability = "hide-availability"
	flagShowResponseTime = "show-response-time"
	flagHideResponseTime = "hide-response-time"
	flagPosition         = "position"
	flagPublicName       = "public-name"
	flagExplanation      = "explanation"
	flagWith             = "with"

	msgStatusPageRequired = "Error: status page uid or slug is required"
	msgSectionRequired    = "Error: <status-page> <section> are required"
	msgResourceRequired   = "Error: <status-page> <section> <resource-uid> are required"

	cmdReorder = "reorder"

	argStatusPage          = "<status-page>"
	argPageSection         = "<status-page> <section>"
	argPageSectionResource = "<status-page> <section> <resource-uid>"

	usageSlug     = "Unique identifier slug"
	usagePosition = "Position within the list"
)

// optString returns a pointer to the flag value when the flag was set, else nil.
func optString(cmd *cli.Command, name string) *string {
	if !cmd.IsSet(name) {
		return nil
	}

	value := cmd.String(name)

	return &value
}

// optInt returns a pointer to the int flag value when the flag was set, else nil.
func optInt(cmd *cli.Command, name string) *int {
	if !cmd.IsSet(name) {
		return nil
	}

	value := cmd.Int(name)

	return &value
}

// optUUID parses the flag value as a UUID when the flag was set, else nil.
func optUUID(cmd *cli.Command, name string) (*openapi_types.UUID, error) {
	if !cmd.IsSet(name) {
		return nil, nil //nolint:nilnil // absent optional UID is represented by a nil pointer
	}

	parsed, err := uuid.Parse(cmd.String(name))
	if err != nil {
		return nil, cli.Exit("Error: invalid "+name+" uid: "+err.Error(), 5)
	}

	return &parsed, nil
}

// boolPair resolves a pair of mutually-exclusive on/off flags into a *bool. When
// neither flag is set it returns nil so the field is omitted from the request.
func boolPair(cmd *cli.Command, onFlag, offFlag string) *bool {
	switch {
	case cmd.Bool(onFlag):
		value := true

		return &value
	case cmd.Bool(offFlag):
		value := false

		return &value
	default:
		return nil
	}
}

// statusPageArg reads the status-page uid-or-slug from the first argument.
func statusPageArg(cmd *cli.Command) (string, error) {
	page := cmd.Args().First()
	if page == "" {
		return "", cli.Exit(msgStatusPageRequired, 5)
	}

	return page, nil
}

// pageSectionArgs reads the <status-page> <section> argument pair.
func pageSectionArgs(cmd *cli.Command) (string, string, error) {
	if cmd.Args().Len() < 2 {
		return "", "", cli.Exit(msgSectionRequired, 5)
	}

	return cmd.Args().Get(0), cmd.Args().Get(1), nil
}

// pageSectionResourceArgs reads <status-page> <section> <resource-uid>.
func pageSectionResourceArgs(cmd *cli.Command) (string, string, openapi_types.UUID, error) {
	if cmd.Args().Len() < 3 {
		return "", "", uuid.Nil, cli.Exit(msgResourceRequired, 5)
	}

	resourceUID, err := uuid.Parse(cmd.Args().Get(2))
	if err != nil {
		return "", "", uuid.Nil, cli.Exit("Error: invalid resource uid: "+err.Error(), 5)
	}

	return cmd.Args().Get(0), cmd.Args().Get(1), resourceUID, nil
}

// parseUIDList parses the remaining args (starting at start) as UUIDs.
func parseUIDList(cmd *cli.Command, start int) ([]openapi_types.UUID, error) {
	uids := make([]openapi_types.UUID, 0, cmd.Args().Len()-start)
	for i := start; i < cmd.Args().Len(); i++ {
		parsed, err := uuid.Parse(cmd.Args().Get(i))
		if err != nil {
			return nil, cli.Exit("Error: invalid uid "+cmd.Args().Get(i)+": "+err.Error(), 5)
		}

		uids = append(uids, parsed)
	}

	return uids, nil
}

// renderStatusPage prints a single status page in text mode.
func renderStatusPage(page *client.StatusPage) {
	output.PrintMessage(os.Stdout, "UID:         "+page.Uid.String())
	output.PrintMessage(os.Stdout, "Name:        "+page.Name)
	output.PrintMessage(os.Stdout, "Slug:        "+page.Slug)
	output.PrintMessage(os.Stdout, "Visibility:  "+page.Visibility)
	output.PrintMessage(os.Stdout, "Default:     "+strconv.FormatBool(page.IsDefault))
	output.PrintMessage(os.Stdout, "Enabled:     "+strconv.FormatBool(page.Enabled))
	output.PrintMessage(os.Stdout, "History:     "+page.HistoryPeriod)

	if page.CustomCss != nil && *page.CustomCss != "" {
		// The stylesheet can be up to 64 KB — print its size, not its body.
		output.PrintMessage(os.Stdout, "Custom CSS:  "+strconv.Itoa(len(*page.CustomCss))+" bytes")
	}
}

// customCSSFlag resolves the mutually-exclusive --custom-css / --custom-css-file
// pair into the request field. --custom-css-file reads the stylesheet from disk
// (the realistic authoring workflow); --custom-css takes it inline, and an
// explicit empty string clears the field on update. Neither flag set -> nil, so
// the field is omitted and the stored stylesheet is left untouched.
func customCSSFlag(cmd *cli.Command) (*string, error) {
	if cmd.IsSet(flagCustomCSSFile) {
		if cmd.IsSet(flagCustomCSS) {
			return nil, cli.Exit("Error: --custom-css and --custom-css-file are mutually exclusive", 5)
		}

		data, err := os.ReadFile(cmd.String(flagCustomCSSFile))
		if err != nil {
			return nil, cli.Exit("Error: cannot read custom CSS file: "+err.Error(), 5)
		}

		css := string(data)

		return &css, nil
	}

	return optString(cmd, flagCustomCSS), nil
}

// --- Status page CRUD ---

// statusPagesListAction lists all status pages for the org.
func statusPagesListAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.ListStatusPagesWithResponse(ctx, cliCtx.GetOrg())
	if err != nil {
		return cliCtx.HandleError("Failed to list status pages", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list status pages", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	if resp.JSON200.Data == nil || len(*resp.JSON200.Data) == 0 {
		output.PrintMessage(os.Stdout, "No status pages found")

		return nil
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{colUID, colName, colSlug, colVisibility, colEnabled, colDefault})

	for i := range *resp.JSON200.Data {
		page := &(*resp.JSON200.Data)[i]
		tbl.AppendRow(table.Row{
			page.Uid.String(),
			page.Name,
			page.Slug,
			page.Visibility,
			strconv.FormatBool(page.Enabled),
			strconv.FormatBool(page.IsDefault),
		})
	}

	tbl.Render()

	return nil
}

// statusPagesCreateAction creates a new status page.
func statusPagesCreateAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	name := cmd.String(flagName)
	slug := cmd.String(keySlug)
	if name == "" || slug == "" {
		return cli.Exit("Error: --name and --slug are required", 5)
	}

	customCSS, err := customCSSFlag(cmd)
	if err != nil {
		return err
	}

	body := client.CreateStatusPageJSONRequestBody{
		Name:             name,
		Slug:             &slug,
		Description:      optString(cmd, flagDescription),
		Visibility:       optString(cmd, flagVisibility),
		Language:         optString(cmd, flagLanguage),
		CustomCss:        customCSS,
		HistoryPeriod:    optString(cmd, flagHistoryPeriod),
		HistoryDays:      optInt(cmd, flagHistoryDays),
		ShowAvailability: boolPair(cmd, flagShowAvailability, flagHideAvailability),
		ShowResponseTime: boolPair(cmd, flagShowResponseTime, flagHideResponseTime),
	}

	if cmd.Bool(flagDefault) {
		isDefault := true
		body.IsDefault = &isDefault
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.CreateStatusPageWithResponse(ctx, cliCtx.GetOrg(), body)
	if err != nil {
		return cliCtx.HandleError("Failed to create status page", err)
	}

	if resp.StatusCode() != 201 || resp.JSON201 == nil {
		return cliCtx.HandleStatusError("Failed to create status page", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON201)
	}

	output.PrintSuccess(os.Stdout, "Created status page "+resp.JSON201.Uid.String())
	renderStatusPage(resp.JSON201)

	return nil
}

// statusPagesGetAction fetches a single status page by uid or slug.
func statusPagesGetAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	page, err := statusPageArg(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	params := &client.GetStatusPageParams{With: optString(cmd, flagWith)}

	resp, err := apiClient.GetStatusPageWithResponse(ctx, cliCtx.GetOrg(), page, params)
	if err != nil {
		return cliCtx.HandleError("Failed to get status page", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to get status page", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	renderStatusPage(resp.JSON200)

	return nil
}

// statusPagesUpdateAction patches a status page.
func statusPagesUpdateAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	page, err := statusPageArg(cmd)
	if err != nil {
		return err
	}

	customCSS, err := customCSSFlag(cmd)
	if err != nil {
		return err
	}

	body := client.UpdateStatusPageJSONRequestBody{
		Name:             optString(cmd, flagName),
		Slug:             optString(cmd, keySlug),
		Description:      optString(cmd, flagDescription),
		Visibility:       optString(cmd, flagVisibility),
		Language:         optString(cmd, flagLanguage),
		CustomCss:        customCSS,
		HistoryPeriod:    optString(cmd, flagHistoryPeriod),
		HistoryDays:      optInt(cmd, flagHistoryDays),
		IsDefault:        boolPair(cmd, flagDefault, flagNoDefault),
		Enabled:          boolPair(cmd, flagEnabled, flagDisabled),
		ShowAvailability: boolPair(cmd, flagShowAvailability, flagHideAvailability),
		ShowResponseTime: boolPair(cmd, flagShowResponseTime, flagHideResponseTime),
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.UpdateStatusPageWithResponse(ctx, cliCtx.GetOrg(), page, body)
	if err != nil {
		return cliCtx.HandleError("Failed to update status page", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to update status page", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	output.PrintSuccess(os.Stdout, "Updated status page "+resp.JSON200.Uid.String())
	renderStatusPage(resp.JSON200)

	return nil
}

// statusPagesRemoveAction deletes a status page by uid or slug.
func statusPagesRemoveAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	page, err := statusPageArg(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.DeleteStatusPageWithResponse(ctx, cliCtx.GetOrg(), page)
	if err != nil {
		return cliCtx.HandleError("Failed to remove status page", err)
	}

	if resp.StatusCode() != 204 {
		return cliCtx.HandleStatusError("Failed to remove status page", resp.StatusCode())
	}

	output.PrintSuccess(os.Stdout, "Removed status page "+page)

	return nil
}

// --- Sections ---

// renderSection prints a single section in text mode.
func renderSection(section *client.StatusPageSection) {
	output.PrintMessage(os.Stdout, "UID:       "+section.Uid.String())
	output.PrintMessage(os.Stdout, "Name:      "+section.Name)
	output.PrintMessage(os.Stdout, "Slug:      "+section.Slug)
	output.PrintMessage(os.Stdout, "Position:  "+strconv.Itoa(section.Position))
}

// statusPagesSectionsListAction lists the sections of a status page.
func statusPagesSectionsListAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	page, err := statusPageArg(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.ListStatusPageSectionsWithResponse(ctx, cliCtx.GetOrg(), page)
	if err != nil {
		return cliCtx.HandleError("Failed to list sections", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list sections", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	if resp.JSON200.Data == nil || len(*resp.JSON200.Data) == 0 {
		output.PrintMessage(os.Stdout, "No sections found")

		return nil
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{colUID, colName, colSlug, colPosition})

	for i := range *resp.JSON200.Data {
		section := &(*resp.JSON200.Data)[i]
		tbl.AppendRow(table.Row{
			section.Uid.String(),
			section.Name,
			section.Slug,
			strconv.Itoa(section.Position),
		})
	}

	tbl.Render()

	return nil
}

// statusPagesSectionsCreateAction creates a section on a status page.
func statusPagesSectionsCreateAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	page, err := statusPageArg(cmd)
	if err != nil {
		return err
	}

	name := cmd.String(flagName)
	slug := cmd.String(keySlug)
	if name == "" || slug == "" {
		return cli.Exit("Error: --name and --slug are required", 5)
	}

	body := client.CreateStatusPageSectionJSONRequestBody{
		Name:     name,
		Slug:     &slug,
		Position: optInt(cmd, flagPosition),
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.CreateStatusPageSectionWithResponse(ctx, cliCtx.GetOrg(), page, body)
	if err != nil {
		return cliCtx.HandleError("Failed to create section", err)
	}

	if resp.StatusCode() != 201 || resp.JSON201 == nil {
		return cliCtx.HandleStatusError("Failed to create section", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON201)
	}

	output.PrintSuccess(os.Stdout, "Created section "+resp.JSON201.Uid.String())
	renderSection(resp.JSON201)

	return nil
}

// statusPagesSectionsGetAction fetches a single section.
func statusPagesSectionsGetAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	page, section, err := pageSectionArgs(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.GetStatusPageSectionWithResponse(ctx, cliCtx.GetOrg(), page, section)
	if err != nil {
		return cliCtx.HandleError("Failed to get section", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to get section", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	renderSection(resp.JSON200)

	return nil
}

// statusPagesSectionsUpdateAction patches a section.
func statusPagesSectionsUpdateAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	page, section, err := pageSectionArgs(cmd)
	if err != nil {
		return err
	}

	body := client.UpdateStatusPageSectionJSONRequestBody{
		Name:     optString(cmd, flagName),
		Slug:     optString(cmd, keySlug),
		Position: optInt(cmd, flagPosition),
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.UpdateStatusPageSectionWithResponse(ctx, cliCtx.GetOrg(), page, section, body)
	if err != nil {
		return cliCtx.HandleError("Failed to update section", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to update section", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	output.PrintSuccess(os.Stdout, "Updated section "+resp.JSON200.Uid.String())
	renderSection(resp.JSON200)

	return nil
}

// statusPagesSectionsRemoveAction deletes a section.
func statusPagesSectionsRemoveAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	page, section, err := pageSectionArgs(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.DeleteStatusPageSectionWithResponse(ctx, cliCtx.GetOrg(), page, section)
	if err != nil {
		return cliCtx.HandleError("Failed to remove section", err)
	}

	if resp.StatusCode() != 204 {
		return cliCtx.HandleStatusError("Failed to remove section", resp.StatusCode())
	}

	output.PrintSuccess(os.Stdout, "Removed section "+section)

	return nil
}

// statusPagesSectionsReorderAction reorders the sections of a status page.
func statusPagesSectionsReorderAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	page, err := statusPageArg(cmd)
	if err != nil {
		return err
	}

	uids, err := parseUIDList(cmd, 1)
	if err != nil {
		return err
	}

	if len(uids) == 0 {
		return cli.Exit("Error: at least one section uid is required", 5)
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	body := client.ReorderStatusPageSectionsJSONRequestBody{Uids: uids}

	resp, err := apiClient.ReorderStatusPageSectionsWithResponse(ctx, cliCtx.GetOrg(), page, body)
	if err != nil {
		return cliCtx.HandleError("Failed to reorder sections", err)
	}

	if resp.StatusCode() != 204 {
		return cliCtx.HandleStatusError("Failed to reorder sections", resp.StatusCode())
	}

	output.PrintSuccess(os.Stdout, "Reordered sections")

	return nil
}

// --- Resources ---

// resourceTarget renders a resource's target for text output: the check UID, or
// the check group UID prefixed with "group:" when the resource aggregates a
// group (spec 2026-08-01-03). Exactly one of the two is ever set.
func resourceTarget(resource *client.StatusPageResource) string {
	switch {
	case resource.CheckUid != nil:
		return resource.CheckUid.String()
	case resource.CheckGroupUid != nil:
		return "group:" + resource.CheckGroupUid.String()
	default:
		return ""
	}
}

// renderResource prints a single resource in text mode.
func renderResource(resource *client.StatusPageResource) {
	output.PrintMessage(os.Stdout, "UID:       "+resource.Uid.String())
	output.PrintMessage(os.Stdout, "Target:    "+resourceTarget(resource))
	output.PrintMessage(os.Stdout, "Position:  "+strconv.Itoa(resource.Position))

	if resource.PublicName != nil {
		output.PrintMessage(os.Stdout, "Public:    "+*resource.PublicName)
	}
}

// statusPagesResourcesListAction lists the resources of a section.
func statusPagesResourcesListAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	page, section, err := pageSectionArgs(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.ListStatusPageResourcesWithResponse(ctx, cliCtx.GetOrg(), page, section)
	if err != nil {
		return cliCtx.HandleError("Failed to list resources", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list resources", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	if resp.JSON200.Data == nil || len(*resp.JSON200.Data) == 0 {
		output.PrintMessage(os.Stdout, "No resources found")

		return nil
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{colUID, colTarget, colPosition})

	for i := range *resp.JSON200.Data {
		resource := &(*resp.JSON200.Data)[i]
		tbl.AppendRow(table.Row{
			resource.Uid.String(),
			resourceTarget(resource),
			strconv.Itoa(resource.Position),
		})
	}

	tbl.Render()

	return nil
}

// statusPagesResourcesCreateAction adds a check as a resource to a section.
func statusPagesResourcesCreateAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	page, section, err := pageSectionArgs(cmd)
	if err != nil {
		return err
	}

	check := cmd.String(flagCheck)
	checkGroup := cmd.String(flagCheckGroup)

	// A resource targets exactly one of a check or a check group (spec
	// 2026-08-01-03) — the same XOR the API and the schema enforce.
	if (check == "") == (checkGroup == "") {
		return cli.Exit("Error: exactly one of --check or --check-group is required", 5)
	}

	body := client.CreateStatusPageResourceJSONRequestBody{
		PublicName:  optString(cmd, flagPublicName),
		Explanation: optString(cmd, flagExplanation),
		Position:    optInt(cmd, flagPosition),
	}

	if check != "" {
		body.CheckUid = &check
	} else {
		body.CheckGroupUid = &checkGroup
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.CreateStatusPageResourceWithResponse(ctx, cliCtx.GetOrg(), page, section, body)
	if err != nil {
		return cliCtx.HandleError("Failed to create resource", err)
	}

	if resp.StatusCode() != 201 || resp.JSON201 == nil {
		return cliCtx.HandleStatusError("Failed to create resource", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON201)
	}

	output.PrintSuccess(os.Stdout, "Created resource "+resp.JSON201.Uid.String())
	renderResource(resp.JSON201)

	return nil
}

// statusPagesResourcesUpdateAction patches a resource.
func statusPagesResourcesUpdateAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	page, section, resourceUID, err := pageSectionResourceArgs(cmd)
	if err != nil {
		return err
	}

	body := client.UpdateStatusPageResourceJSONRequestBody{
		PublicName:    optString(cmd, flagPublicName),
		Explanation:   optString(cmd, flagExplanation),
		Position:      optInt(cmd, flagPosition),
		CheckUid:      optString(cmd, flagCheck),
		CheckGroupUid: optString(cmd, flagCheckGroup),
	}

	if body.CheckUid != nil && body.CheckGroupUid != nil {
		return cli.Exit("Error: --check and --check-group are mutually exclusive", 5)
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.UpdateStatusPageResourceWithResponse(ctx, cliCtx.GetOrg(), page, section, resourceUID, body)
	if err != nil {
		return cliCtx.HandleError("Failed to update resource", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to update resource", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	output.PrintSuccess(os.Stdout, "Updated resource "+resp.JSON200.Uid.String())
	renderResource(resp.JSON200)

	return nil
}

// statusPagesResourcesRemoveAction removes a resource from a section.
func statusPagesResourcesRemoveAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	page, section, resourceUID, err := pageSectionResourceArgs(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.DeleteStatusPageResourceWithResponse(ctx, cliCtx.GetOrg(), page, section, resourceUID)
	if err != nil {
		return cliCtx.HandleError("Failed to remove resource", err)
	}

	if resp.StatusCode() != 204 {
		return cliCtx.HandleStatusError("Failed to remove resource", resp.StatusCode())
	}

	output.PrintSuccess(os.Stdout, "Removed resource "+resourceUID.String())

	return nil
}

// statusPagesResourcesReorderAction reorders the resources of a section.
func statusPagesResourcesReorderAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	page, section, err := pageSectionArgs(cmd)
	if err != nil {
		return err
	}

	uids, err := parseUIDList(cmd, 2)
	if err != nil {
		return err
	}

	if len(uids) == 0 {
		return cli.Exit("Error: at least one resource uid is required", 5)
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	body := client.ReorderStatusPageResourcesJSONRequestBody{Uids: uids}

	resp, err := apiClient.ReorderStatusPageResourcesWithResponse(ctx, cliCtx.GetOrg(), page, section, body)
	if err != nil {
		return cliCtx.HandleError("Failed to reorder resources", err)
	}

	if resp.StatusCode() != 204 {
		return cliCtx.HandleStatusError("Failed to reorder resources", resp.StatusCode())
	}

	output.PrintSuccess(os.Stdout, "Reordered resources")

	return nil
}

// --- Subscribers ---

// statusPagesSubscribersListAction lists the subscribers of a status page.
func statusPagesSubscribersListAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	page, err := statusPageArg(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.ListStatusPageSubscribersWithResponse(ctx, cliCtx.GetOrg(), page)
	if err != nil {
		return cliCtx.HandleError("Failed to list subscribers", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list subscribers", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	if resp.JSON200.Data == nil || len(*resp.JSON200.Data) == 0 {
		output.PrintMessage(os.Stdout, "No subscribers found")

		return nil
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{colUID, colEmail, colScope, colConfirmed})

	for i := range *resp.JSON200.Data {
		sub := &(*resp.JSON200.Data)[i]

		email := ""
		if sub.Email != nil {
			email = *sub.Email
		}

		tbl.AppendRow(table.Row{
			sub.Uid.String(),
			email,
			sub.Scope,
			strconv.FormatBool(sub.Confirmed),
		})
	}

	tbl.Render()

	return nil
}

// statusPagesSubscribersRemoveAction removes a subscriber from a status page.
func statusPagesSubscribersRemoveAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	page, err := statusPageArg(cmd)
	if err != nil {
		return err
	}

	if cmd.Args().Len() < 2 {
		return cli.Exit("Error: <status-page> <subscriber-uid> are required", 5)
	}

	subscriberUID, err := uuid.Parse(cmd.Args().Get(1))
	if err != nil {
		return cli.Exit("Error: invalid subscriber uid: "+err.Error(), 5)
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.RemoveStatusPageSubscriberWithResponse(ctx, cliCtx.GetOrg(), page, subscriberUID)
	if err != nil {
		return cliCtx.HandleError("Failed to remove subscriber", err)
	}

	if resp.StatusCode() != 204 {
		return cliCtx.HandleStatusError("Failed to remove subscriber", resp.StatusCode())
	}

	output.PrintSuccess(os.Stdout, "Removed subscriber "+subscriberUID.String())

	return nil
}
