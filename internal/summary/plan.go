package summary

// Plan is what a run will cost, worked out without sending anything.
//
// One accounting, two readers: -dry-run renders it in full, and an interactive run prints
// its totals before the first call. They have to agree - the call count is the number that
// decides whether the wait is worth starting, and a run that turns out to be 122 calls when
// the tool implied a handful is one the operator kills halfway through.
type Plan struct {
	// Chars is the total digest size across every thread, root and subagent alike.
	Chars int
	// Calls is how many model calls Run will make.
	Calls    int
	Sessions []PlanSession
}

// PlanSession is one selected session's share of the plan.
type PlanSession struct {
	Name      string
	Chars     int
	Passes    int
	Subagents []PlanUnit
}

// PlanUnit is one subagent thread within a session.
type PlanUnit struct {
	Name   string
	Chars  int
	Passes int
}

// Estimate accounts for every call Run will make.
//
// It has to mirror Run exactly, so it walks the same subtree in the same order: one call per
// chunk of every digest, root and subagent alike, plus a combining call wherever a digest
// chunked, plus the session pass wherever subagents contributed, plus the window roll-up.
func Estimate(src Source, sessions []Session, o Options) Plan {
	o.setDefaults()

	p := Plan{Sessions: make([]PlanSession, 0, len(sessions))}
	for _, s := range sessions {
		th := s.Thread

		root := Build(src.Turns(th.ThreadID), o)
		ps := PlanSession{Name: th.Name, Chars: root.Chars, Passes: root.Passes()}
		p.Chars += root.Chars
		p.Calls += callsFor(root)

		kids := descendants(th)
		for _, k := range kids {
			kd := Build(src.Turns(k.ThreadID), o)
			ps.Subagents = append(ps.Subagents, PlanUnit{Name: label(k), Chars: kd.Chars, Passes: kd.Passes()})
			p.Chars += kd.Chars
			p.Calls += callsFor(kd)
		}
		if len(kids) > 0 {
			p.Calls++ // the pass that folds the subagent summaries into the session
		}

		p.Sessions = append(p.Sessions, ps)
	}
	p.Calls++ // the window roll-up
	return p
}

// callsFor is one digest's own cost: a call per chunk, plus a combine when it chunked.
func callsFor(d Digest) int {
	if d.Passes() > 1 {
		return d.Passes() + 1
	}
	return d.Passes()
}
