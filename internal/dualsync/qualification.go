package dualsync

import "context"

func (e *Engine) startDiagnostics(run func(context.Context, string) Report) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.outputs) > 0 || e.diagnostics.Running {
		return fail("BUSY", 409, "stop streams and wait for existing diagnostics")
	}
	e.diagnostics.Running = true
	e.revision++
	go func() {
		report := run(e.ctx, e.Config.Assets)
		if e.ctx.Err() == nil {
			if err := e.SetDiagnostics(report); err != nil {
				e.mu.Lock()
				e.diagnostics.Running = false
				e.diagnostics.Warning = "Could not save encoding qualification: " + Redact(err.Error())
				e.revision++
				e.mu.Unlock()
			}
		}
	}()
	return nil
}
