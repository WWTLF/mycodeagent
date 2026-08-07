package engine

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

// solutionDoc is where the engine table lives. The path is relative to the
// package directory, which is where `go test` runs.
const solutionDoc = "../../../docs/Solution.md"

// firstCode returns the text inside the first pair of backticks, or the cell
// stripped of bold markers when it carries none. It exists because the cells are
// not uniform: the image and health path are code-quoted, the port is bare, and
// the engine type reads "`llamacpp` (default)".
func firstCode(cell string) string {
	if a := strings.Index(cell, "`"); a >= 0 {
		if b := strings.Index(cell[a+1:], "`"); b >= 0 {
			return cell[a+1 : a+1+b]
		}
	}
	return strings.TrimSpace(strings.ReplaceAll(cell, "*", ""))
}

// engineTableRows parses the Docker-image table out of Solution.md, keyed by the
// EngineType each row names.
func engineTableRows(t *testing.T) map[string][]string {
	t.Helper()

	data, err := os.ReadFile(solutionDoc)
	if err != nil {
		t.Fatalf("reading %s: %v", solutionDoc, err)
	}

	lines := strings.Split(string(data), "\n")
	header := -1
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "|") &&
			strings.Contains(line, "Docker Image") && strings.Contains(line, "Server Port") {
			header = i
			break
		}
	}
	if header < 0 {
		t.Fatalf("no engine table in %s: expected a row with the headings "+
			"'Docker Image' and 'Server Port'. If the table moved, point this test at it.",
			solutionDoc)
	}

	rows := map[string][]string{}
	for _, line := range lines[header+1:] {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			break // end of the table
		}
		if strings.HasPrefix(line, "|--") || strings.HasPrefix(line, "| --") {
			continue // the header separator
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) < 5 {
			continue
		}
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		rows[firstCode(cells[1])] = cells
	}
	return rows
}

// The engine table in Solution.md is the only place the images are written down
// for a reader, and it drifted twice without anyone noticing until a release:
// it named `vastai/pytorch:cuda12` — a tag that registry does not publish at all,
// so it described something that could never have run — and it still named the
// ai-dock ComfyUI image long after the code moved to vastai/comfy.
//
// Documentation drift is normally a matter of tidiness. Here it is not: these
// rows are what someone reads to decide whether a CUDA version covers their card,
// and the Jupyter row was wrong in exactly the way that question is asked. Bumping
// an image is also a one-line change, which is precisely the kind that ships
// without a second thought about the docs.
//
// Keyed on allEngines(), so a fourth engine with no row fails here too.
func TestSolutionDocEngineTableMatchesTheCode(t *testing.T) {
	rows := engineTableRows(t)
	engines := allEngines()

	for engineType, e := range engines {
		cells, ok := rows[engineType]
		if !ok {
			t.Errorf("engine %q has no row in %s — add one, the table is where a reader "+
				"looks up which image and port an engine uses", engineType, solutionDoc)
			continue
		}
		for _, check := range []struct {
			what string
			doc  string
			code string
		}{
			{"image", firstCode(cells[2]), e.DockerImage(nil)},
			{"server port", firstCode(cells[3]), strconv.Itoa(e.ServerPort(nil))},
			{"health path", firstCode(cells[4]), e.HealthPath(nil)},
		} {
			if check.doc != check.code {
				t.Errorf("%s: %s says %s %q, the code uses %q",
					engineType, solutionDoc, check.what, check.doc, check.code)
			}
		}
	}

	for engineType := range rows {
		if _, ok := engines[engineType]; !ok {
			t.Errorf("%s documents engine %q, which MultiEngine no longer dispatches to",
				solutionDoc, engineType)
		}
	}
}
