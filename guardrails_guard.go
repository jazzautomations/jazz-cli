package main

import "context"

// sgGuard runs sgCheckCommand and shows its warnings inline (never a prompt,
// also in turbo) and in the audit log; it returns the block-level reason.
func (a *Agent) sgGuard(ctx context.Context, cmd string, allowed bool) string {
	why, warns := a.sgCheckCommand(ctx, cmd, allowed)
	if len(warns) > 0 && a.mode() != "yolo" && !a.apBatchCollecting() { // the dry pass of a merged confirmation stays silent
		for _, w := range warns {
			a.ui.Notice("⚠ pacote: " + oneLine(w, 200))
		}
		a.apRecordAuto("bash", cmd, "supply:warn")
	}
	return why
}
