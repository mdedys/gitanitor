package exec

import (
	"bytes"
	"os/exec"
)

// Result captures the outcome of running an external command.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Runner executes git and gh commands. It is the single seam through which
// all external process access flows, so tests can substitute canned behavior.
type Runner interface {
	Run(name string, args ...string) Result
}

// System runs commands against the real operating system.
type System struct{}

func (System) Run(name string, args ...string) Result {
	cmd := exec.Command(name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	code := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			code = 1
			if stderr.Len() == 0 {
				stderr.WriteString(err.Error())
			}
		}
	}

	return Result{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: code,
	}
}

// LookPath reports whether an executable exists on PATH.
func LookPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
