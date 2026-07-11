package cli

import (
	"context"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/fclairamb/solidping/server/pkg/cli/output"
	"github.com/fclairamb/solidping/server/pkg/client"
)

// heartbeatSendAction implements `sp heartbeat send <identifier>`.
func heartbeatSendAction(ctx context.Context, cmd *cli.Command) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	if cmd.Args().Len() < 1 {
		return cli.Exit("Error: check identifier is required", 5)
	}

	identifier := cmd.Args().Get(0)

	apiClient, err := cliCtx.APIHelper.GetClient(ctx)
	if err != nil {
		return cliCtx.HandleAuthError(err)
	}

	opts := client.HeartbeatOptions{
		Token:   cmd.String("token"),
		Status:  cmd.String(flagStatus),
		Message: cmd.String("message"),
	}

	if err := apiClient.SendHeartbeat(ctx, cliCtx.GetOrg(), identifier, opts); err != nil {
		return cliCtx.HandleError("Failed to send heartbeat", err)
	}

	if !cliCtx.IsText() {
		return cliCtx.Outputter.Print(map[string]string{keySuccess: "ok", "identifier": identifier})
	}

	output.PrintSuccess(os.Stdout, "Heartbeat sent for "+identifier)

	return nil
}
