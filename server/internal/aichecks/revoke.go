package aichecks

import (
	"context"
	"fmt"

	"github.com/fclairamb/solidping/server/internal/checkers/checkjs"
	jsconfig "github.com/fclairamb/solidping/server/internal/checkers/checkjs/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/dbctx"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// revokeReason is the reason stored on the version that revokes `repair: auto`.
const revokeReason = "AI repair: a run reached a host outside the repair grant, repair set back to propose"

// RepairHostRefused reports a result whose repair-applied script reached a
// host outside its repair grant (spec 2026-10-24-01).
func RepairHostRefused(output map[string]any) bool {
	refused, _ := output[checkjs.OutputKeyRepairRefused].(bool)

	return refused
}

// RevokeAutoRepair sets a check whose repair-applied script reached a host
// outside its grant back to `repair: propose`, so the next repair needs a
// human, and records a check.ai_repair_revoked event. The applied version
// keeps its grant: it stays held, and keeps failing, until someone edits it.
// A check not in `repair: auto` is left alone. Reports whether it flipped.
func RevokeAutoRepair(ctx context.Context, dbSvc db.Service, orgUID, checkUID, refusal string) (bool, error) {
	check, err := dbSvc.GetCheck(ctx, orgUID, checkUID)
	if err != nil {
		return false, fmt.Errorf("loading the check: %w", err)
	}

	block, ok := check.Config["ai"].(map[string]any)
	if !ok {
		return false, nil
	}

	if mode, _ := block["repair"].(string); mode != string(jsconfig.RepairAuto) {
		return false, nil
	}

	revoked := make(map[string]any, len(block))
	for key, value := range block {
		revoked[key] = value
	}

	revoked["repair"] = string(jsconfig.RepairPropose)

	config := models.JSONMap{}
	for key, value := range check.Config {
		config[key] = value
	}

	config["ai"] = revoked

	changeCtx := dbctx.WithChange(ctx, &dbctx.ChangeSource{
		Origin: string(models.CheckVersionOriginSystem), Reason: revokeReason,
	})
	if err := dbSvc.UpdateCheck(changeCtx, check.UID, &models.CheckUpdate{Config: &config}); err != nil {
		return false, fmt.Errorf("setting repair back to propose: %w", err)
	}

	event := models.NewEvent(check.OrganizationUID, models.EventTypeCheckAIRepairRevoked, models.ActorTypeSystem)
	event.CheckUID = &check.UID
	event.Payload["reason"] = revokeReason
	event.Payload["error"] = refusal

	if err := dbSvc.CreateEvent(ctx, event); err != nil {
		return true, fmt.Errorf("recording the revocation: %w", err)
	}

	return true, nil
}
