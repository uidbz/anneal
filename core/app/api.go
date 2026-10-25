package app

import (
	"context"
	"os"
	"time"
)

// AgentAPI is the `agent` global exposed to Risor scripts. List endpoints go
// through jsonList so Risor never sees a nil array (dot-access/length on null
// is a hard script error).
func (a *App) AgentAPI() map[string]any {
	api := a.Bridge.API()
	api["provider"] = func() any { return toJSON(a.Monitor.Current()) }
	api["config"] = func() any { return toJSON(a.Cfg) }
	api["probe"] = func() any {
		go a.Monitor.ProbeNow() // never block the script/GUI thread
		return toJSON(a.Monitor.Current())
	}
	api["saveConfig"] = func(doc string) any {
		if err := a.Cfg.Apply([]byte(doc)); err != nil {
			return toJSON(map[string]any{"ok": false, "error": err.Error()})
		}
		a.Monitor.SetProvider(a.Cfg.Provider)
		a.Bridge.Emit(map[string]any{"type": "config.changed"})
		return toJSON(map[string]any{"ok": true})
	}
	api["send"] = func(input string) any {
		a.Runner.Send(input)
		return nil
	}
	api["interrupt"] = func() any {
		a.Runner.Interrupt()
		return nil
	}
	api["sessions"] = func() any { return a.Runner.SessionsJSON() }
	api["newSession"] = func() any {
		a.Runner.NewSession()
		return nil
	}
	api["openSession"] = func(id string) any {
		a.Runner.OpenSession(id)
		return nil
	}
	api["respondPerm"] = func(id, action string) any {
		a.Runner.RespondPerm(id, action)
		return nil
	}
	api["setMode"] = func(mode string) any {
		a.Runner.SetMode(mode)
		return nil
	}
	api["mode"] = func() any { return a.Runner.Mode() }
	api["tieState"] = func() any { return toJSON(a.Tie.State()) }
	api["memoryView"] = func() any {
		m, _ := a.Mem.Read("memory")
		u, _ := a.Mem.Read("user")
		return toJSON(map[string]any{
			"memory": m, "user": u,
			"memory_cap": a.Cfg.Memory.MemoryCharLimit,
			"user_cap":   a.Cfg.Memory.UserCharLimit,
		})
	}
	api["memoryPending"] = func() any {
		list, err := a.Mem.ListPending()
		if err != nil {
			return toJSON([]any{})
		}
		return jsonList(list)
	}
	api["learnNow"] = func() any { a.Review.LearnNow(); return nil }
	api["improveStart"] = func() any {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
			defer cancel()
			if _, err := a.Improve(ctx); err != nil {
				a.Bridge.Emit(map[string]any{"type": "improve.error", "error": err.Error()})
			}
		}()
		return nil
	}
	api["harnessInfo"] = func() any { return toJSON(a.Runner.HarnessInfo()) }
	api["harnessLibList"] = func() any { return jsonList(a.Libs.List()) }
	api["harnessFiles"] = func() any { return jsonList(a.Harness.Files()) }
	api["harnessGet"] = func(name string) any {
		b, err := a.Harness.GetFile(name)
		return toJSON(map[string]any{"name": name, "content": b, "error": errString(err)})
	}
	api["harnessSave"] = func(name, content string) any {
		err := a.Harness.SaveFile(name, content)
		warns := nzs(a.Harness.Validate(a.Cfg.Harness.CharLimit))
		a.Bridge.Emit(map[string]any{"type": "harness.warn", "warns": warns, "saved": name})
		return toJSON(map[string]any{"error": errString(err), "warns": warns,
			"restart_for_rules": true})
	}
	api["libExperience"] = func(id string) any {
		b, err := a.Libs.Experience(id)
		return toJSON(map[string]any{"id": id, "content": b, "error": errString(err)})
	}
	api["libPromote"] = func(id string) any {
		backup, err := a.Libs.Promote(id)
		warns := nzs(a.Harness.Validate(a.Cfg.Harness.CharLimit))
		a.Bridge.Emit(map[string]any{"type": "harness.promoted", "entry": id,
			"backup": backup, "error": errString(err), "warns": warns})
		a.Journey.Append("harness-promote", map[string]any{"entry": id, "backup": backup})
		return toJSON(map[string]any{"entry": id, "backup": backup,
			"error": errString(err), "warns": warns})
	}
	api["flagPeek"] = func(n int) any { return jsonList(a.Review.PendingFlags(n)) }
	api["journey"] = func(limit int) any { return jsonList(a.Journey.Tail(limit)) }
	api["skillsList"] = func() any { return jsonList(a.Skills.List()) }
	api["skillPending"] = func() any {
		list, err := a.Skills.ListPending()
		if err != nil {
			return toJSON([]any{})
		}
		return jsonList(list)
	}
	api["respondSkill"] = func(id, decision string) any {
		approve := decision == "approve"
		op, result, err := a.Skills.Resolve(id, approve)
		out := map[string]any{"type": "skill.resolved", "id": id,
			"approved": approve, "error": "", "result": result}
		if op != nil {
			out["name"] = op.Name
			out["action"] = op.Action
		}
		if err != nil {
			out["error"] = err.Error()
		}
		a.Bridge.Emit(out)
		return nil
	}
	api["respondMemory"] = func(id, decision string) any {
		approve := decision == "approve"
		op, content, err := a.Mem.Resolve(id, approve)
		out := map[string]any{"type": "memory.resolved", "id": id,
			"approved": approve, "error": ""}
		if op != nil {
			out["target"] = op.Target
			out["action"] = op.Action
		}
		if err != nil {
			out["error"] = err.Error()
		} else if approve {
			a.Bridge.Emit(map[string]any{"type": "memory.updated",
				"target": out["target"], "action": out["action"], "content": content})
		}
		a.Bridge.Emit(out)
		return nil
	}
	api["rebuildIndex"] = func() any {
		go func() {
			n, err := a.Runner.RebuildIndex(context.Background())
			msg := map[string]any{"type": "tie.rebuilt", "sessions": n, "error": ""}
			if err != nil {
				msg["error"] = err.Error()
			}
			a.Bridge.Emit(msg)
		}()
		return nil
	}
	api["quit"] = func() any {
		// Failsafe for host-owned windows that ignore Fyne quit; the normal
		// path returns from ShowAndRun and exits well before this fires.
		time.AfterFunc(2*time.Second, func() { os.Exit(0) })
		a.Bridge.Quit()
		return nil
	}
	return api
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// jsonList marshals a list endpoint, normalizing nil to [] (Risor scripts
// index/length the result and null is an error there).
func jsonList[T any](v []T) string {
	if v == nil {
		v = []T{}
	}
	return toJSON(v)
}

func nzs(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
