package engine

import "fmt"

// Process inspection shared by every engine.
//
// None of these may use procps. The llama.cpp image is a bare CUDA runtime with
// libgomp1/curl/ffmpeg and nothing else — no pgrep, no pkill, no ps — so process
// checks read /proc directly. The other images are fatter and would tolerate
// procps, but the rule is kept uniform: one way to look at processes means one
// place to get it wrong.
//
// Every pattern passed here MUST be self-match-safe, i.e. it must not match its
// own text. The command string lands in the argv of the shell that runs it, and
// /proc/<pid>/cmdline includes that shell, so a plain "jupyter" makes the probe
// report ALIVE with nothing running and makes the kill sweep signal its own SSH
// session. The convention is to bracket one character: "jupyte[r]" matches
// "jupyter" but not itself. TestProcessCommandsAreSelfMatchSafe enforces this
// across all engines by running the real commands.

// livenessProbe builds a command that prints ALIVE or DEAD.
func livenessProbe(pattern string) string {
	return fmt.Sprintf("grep -qs '%s' /proc/*/cmdline && echo ALIVE || echo DEAD", pattern)
}

// procKillPattern builds a /proc scan that signals every matching process.
func procKillPattern(pattern, signal string) string {
	sig := ""
	if signal != "" {
		sig = signal + " "
	}
	return fmt.Sprintf("for p in /proc/[0-9]*; do grep -qs '%s' $p/cmdline && kill %s${p#/proc/} 2>/dev/null; done",
		pattern, sig)
}
