package cli

import (
	"context"
	"fmt"
	"mime"
	"os"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/urfave/cli/v3"

	"github.com/fclairamb/solidping/server/pkg/cli/output"
	"github.com/fclairamb/solidping/server/pkg/client"
)

// Files CLI shared strings.
const (
	flagQuery      = "query"
	flagOutputFile = "output-file"

	colMime = "MIME"
	colSize = "SIZE"

	msgFileUIDRequired = "Error: file uid is required"

	usageFileQuery  = "Filter by filename substring"
	usageFileLimit  = "Maximum results (1-200, default 50)"
	usageFileOffset = "Result offset"
	usageOutputFile = "Write content to this path instead of stdout"
)

// filesCommand builds the "files" command group.
func filesCommand() *cli.Command {
	return &cli.Command{
		Name:    "files",
		Aliases: []string{"file"},
		Usage:   "Manage stored files",
		Flags:   GetGlobalFlags(),
		Commands: []*cli.Command{
			{
				Name:  flagList,
				Usage: "List stored files",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: flagQuery, Aliases: []string{"q"}, Usage: usageFileQuery},
					&cli.IntFlag{Name: flagLimit, Usage: usageFileLimit},
					&cli.IntFlag{Name: flagOffset, Usage: usageFileOffset},
				},
				Action: filesListAction,
			},
			{
				Name:      flagGet,
				Usage:     "Get file metadata",
				ArgsUsage: argUID,
				Action:    filesGetAction,
			},
			{
				Name:      "download",
				Usage:     "Download file content",
				ArgsUsage: argUID,
				Flags: []cli.Flag{
					&cli.StringFlag{Name: flagOutputFile, Aliases: []string{"f"}, Usage: usageOutputFile},
				},
				Action: filesDownloadAction,
			},
			{
				Name:      flagRemove,
				Aliases:   []string{"rm", cmdDelete},
				Usage:     "Remove a file",
				ArgsUsage: argUID,
				Action:    filesRemoveAction,
			},
		},
	}
}

// filesListAction lists files for the org.
func filesListAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	params := &client.ListFilesParams{
		Q:      optString(cmd, flagQuery),
		Limit:  optInt(cmd, flagLimit),
		Offset: optInt(cmd, flagOffset),
	}

	resp, err := apiClient.ListFilesWithResponse(ctx, cliCtx.GetOrg(), params)
	if err != nil {
		return cliCtx.HandleError("Failed to list files", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to list files", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	if len(resp.JSON200.Data) == 0 {
		output.PrintMessage(os.Stdout, "No files found")

		return nil
	}

	tbl := output.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{colUID, colName, colMime, colSize, colCreated})

	for i := range resp.JSON200.Data {
		file := &resp.JSON200.Data[i]
		tbl.AppendRow(table.Row{
			file.Uid.String(),
			file.Name,
			file.MimeType,
			strconv.FormatInt(file.Size, 10),
			file.CreatedAt.Format(time.RFC3339),
		})
	}

	tbl.Render()

	return nil
}

// filesGetAction shows a single file's metadata.
func filesGetAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	fileUID, err := fileUIDArg(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.GetFileWithResponse(ctx, cliCtx.GetOrg(), fileUID)
	if err != nil {
		return cliCtx.HandleError("Failed to get file", err)
	}

	if resp.StatusCode() != 200 || resp.JSON200 == nil {
		return cliCtx.HandleStatusError("Failed to get file", resp.StatusCode())
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(resp.JSON200)
	}

	file := resp.JSON200
	output.PrintMessage(os.Stdout, "UID:        "+file.Uid.String())
	output.PrintMessage(os.Stdout, "Name:       "+file.Name)
	output.PrintMessage(os.Stdout, "MIME type:  "+file.MimeType)
	output.PrintMessage(os.Stdout, "Size:       "+strconv.FormatInt(file.Size, 10))
	output.PrintMessage(os.Stdout, "Created by: "+derefStr(file.CreatedBy))
	output.PrintMessage(os.Stdout, "Created at: "+file.CreatedAt.Format(time.RFC3339))

	return nil
}

// filesDownloadAction downloads file content to a file or stdout.
func filesDownloadAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	fileUID, err := fileUIDArg(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.DownloadFileWithResponse(ctx, cliCtx.GetOrg(), fileUID)
	if err != nil {
		return cliCtx.HandleError("Failed to download file", err)
	}

	if resp.StatusCode() != 200 {
		return cliCtx.HandleStatusError("Failed to download file", resp.StatusCode())
	}

	outFile := cmd.String(flagOutputFile)
	if outFile == "" {
		_, _ = os.Stdout.Write(resp.Body)

		return nil
	}

	if writeErr := os.WriteFile(outFile, resp.Body, 0o600); writeErr != nil {
		return cli.Exit(fmt.Sprintf("Error: cannot write %s: %v", outFile, writeErr), 5)
	}

	name := downloadedName(resp.HTTPResponse.Header.Get("Content-Disposition"))
	msg := "Downloaded " + strconv.Itoa(len(resp.Body)) + " bytes to " + outFile + name
	output.PrintSuccess(os.Stdout, msg)

	return nil
}

// downloadedName extracts the original filename from a Content-Disposition
// header, returning a " (name)" suffix or an empty string when absent.
func downloadedName(contentDisposition string) string {
	if contentDisposition == "" {
		return ""
	}

	_, params, err := mime.ParseMediaType(contentDisposition)
	if err != nil {
		return ""
	}

	if name := params["filename"]; name != "" {
		return " (" + name + ")"
	}

	return ""
}

// filesRemoveAction deletes a file by UID.
func filesRemoveAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	fileUID, err := fileUIDArg(cmd)
	if err != nil {
		return err
	}

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	resp, err := apiClient.DeleteFileWithResponse(ctx, cliCtx.GetOrg(), fileUID)
	if err != nil {
		return cliCtx.HandleError("Failed to remove file", err)
	}

	if resp.StatusCode() != 204 {
		return cliCtx.HandleStatusError("Failed to remove file", resp.StatusCode())
	}

	output.PrintSuccess(os.Stdout, "File removed: "+fileUID.String())

	return nil
}

// fileUIDArg reads and validates the file UID from the first arg.
func fileUIDArg(cmd *cli.Command) (uuid.UUID, error) {
	raw := cmd.Args().First()
	if raw == "" {
		return uuid.Nil, cli.Exit(msgFileUIDRequired, 5)
	}

	parsed, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, cli.Exit("Error: invalid file uid: "+err.Error(), 5)
	}

	return parsed, nil
}
