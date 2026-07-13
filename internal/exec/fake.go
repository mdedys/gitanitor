package exec

import "strings"

// Invocation records one call to a Runner.
type Invocation struct {
	Name string
	Args []string
}

// Line renders the invocation as a space-joined command string.
func (i Invocation) Line() string {
	return i.Name + " " + strings.Join(i.Args, " ")
}

// Fake is a Runner that returns canned Results and records every invocation.
// The responder receives the command and returns the Result to hand back.
type Fake struct {
	Responder func(name string, args ...string) Result
	Calls     []Invocation
}

func (f *Fake) Run(name string, args ...string) Result {
	f.Calls = append(f.Calls, Invocation{Name: name, Args: append([]string(nil), args...)})
	if f.Responder == nil {
		return Result{}
	}
	return f.Responder(name, args...)
}

// Hybrid runs git commands through a real Runner while serving gh commands
// from a fake responder. It records every invocation so tests can assert on
// the exact command strings issued.
type Hybrid struct {
	Git   Runner
	GH    func(args ...string) Result
	Calls []Invocation
}

func (h *Hybrid) Run(name string, args ...string) Result {
	h.Calls = append(h.Calls, Invocation{Name: name, Args: append([]string(nil), args...)})
	if name == "gh" {
		if h.GH == nil {
			return Result{}
		}
		return h.GH(args...)
	}
	return h.Git.Run(name, args...)
}
