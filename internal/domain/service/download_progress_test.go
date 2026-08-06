package service

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/WWTLF/mycodeagent/internal/domain/entity"
)

// captureStdout runs fn and returns whatever it printed.
//
// It drains to EOF rather than taking a single Read: one Read returns only what
// happens to be buffered when the reader is scheduled, which is fine for the
// one-line reporter output below but silently truncates anything that prints in
// several calls — a whole deploy, for instance.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		r.Close()
		done <- buf.String()
	}()
	fn()
	w.Close()
	os.Stdout = orig
	select {
	case s := <-done:
		return s
	case <-time.After(time.Second):
		return ""
	}
}

// The reporter exists because llama.cpp prints nothing at all while fetching a
// GGUF: the log stops after the startup banner and the tunnel logs
// "connect_to localhost port 8000: failed" on repeat, which is indistinguishable
// from a hang. Deploys were aborted on that suspicion before anyone confirmed
// bytes were still arriving.
func TestDownloadReporterShowsProgressAndRate(t *testing.T) {
	r := newDownloadReporter(&entity.Model{DownloadGB: 25.6})

	// First sample only establishes a baseline — there is no rate to state yet.
	if out := captureStdout(t, func() { r.report([]byte("1000000000\n")) }); out != "" {
		t.Errorf("first sample should be silent, printed: %q", out)
	}

	r.lastAt = time.Now().Add(-10 * time.Second) // 10s since the baseline
	out := captureStdout(t, func() { r.report([]byte("6000000000")) })

	for _, want := range []string{"6.0", "25.6 GB", "23%", "MB/s"} {
		if !strings.Contains(out, want) {
			t.Errorf("progress line missing %q: %q", want, out)
		}
	}
}

// A stalled download must not print the same line over and over — silence here
// is the signal that nothing is moving.
func TestDownloadReporterStaysQuietWhenNothingArrives(t *testing.T) {
	r := newDownloadReporter(&entity.Model{DownloadGB: 10})
	r.report([]byte("1000000000")) // baseline

	if out := captureStdout(t, func() { r.report([]byte("1000000000")) }); out != "" {
		t.Errorf("unchanged byte count should print nothing, got: %q", out)
	}
}

// A model with no declared size still gets a useful line, just without a
// percentage — better than falsely claiming one.
func TestDownloadReporterWithoutADeclaredSize(t *testing.T) {
	r := newDownloadReporter(&entity.Model{})
	r.report([]byte("1000000000"))
	r.lastAt = time.Now().Add(-5 * time.Second)

	out := captureStdout(t, func() { r.report([]byte("3000000000")) })
	if !strings.Contains(out, "3.0 GB") {
		t.Errorf("expected a bytes-only line, got %q", out)
	}
	if strings.Contains(out, "%") {
		t.Errorf("must not invent a percentage without a total: %q", out)
	}
}

// The cache holds a little more than the GGUF itself, so the raw ratio can pass
// 100. Reporting "downloading 103%" would look like a bug.
func TestDownloadReporterClampsPercentage(t *testing.T) {
	r := newDownloadReporter(&entity.Model{DownloadGB: 1})
	r.report([]byte("1000000"))
	r.lastAt = time.Now().Add(-time.Second)

	out := captureStdout(t, func() { r.report([]byte("2000000000")) }) // 2x the total
	if !strings.Contains(out, "100%") {
		t.Errorf("expected the percentage clamped to 100%%, got %q", out)
	}
}

// Decimal GB, not GiB. Caught on a live instance: `du -sb` returned
// 25 636 485 460 bytes for the entry the catalog calls 25.6 GB — exactly 25.6e9.
// Interpreting DownloadGB as GiB made a finished download report 93%.
func TestDownloadReporterUsesDecimalGigabytes(t *testing.T) {
	r := newDownloadReporter(&entity.Model{DownloadGB: 25.6})
	r.report([]byte("1000000"))
	r.lastAt = time.Now().Add(-time.Second)

	out := captureStdout(t, func() { r.report([]byte("25636485460")) })
	if !strings.Contains(out, "25.6 / 25.6 GB (100%)") {
		t.Errorf("a completed download should read 25.6 / 25.6 GB (100%%), got %q", out)
	}
}

// Garbage from the remote (empty output, an error message, a zero) must not
// produce a nonsense line.
func TestDownloadReporterIgnoresUnusableSamples(t *testing.T) {
	r := newDownloadReporter(&entity.Model{DownloadGB: 10})
	for _, sample := range []string{"", "0", "du: cannot access", "\n"} {
		if out := captureStdout(t, func() { r.report([]byte(sample)) }); out != "" {
			t.Errorf("sample %q produced output: %q", sample, out)
		}
	}
}

// Every ssh call goes through CombinedOutput, so stderr rides along — and vast.ai
// prints a two-line login banner on every connection. The first implementation
// parsed the whole blob as a number, so it failed on every single sample and the
// reporter printed nothing for an entire 21 GB download. The unit tests missed it
// because they fed clean numbers; this one feeds what the wire actually carries.
func TestDownloadReporterParsesOutputCarryingTheSSHBanner(t *testing.T) {
	const banner = "Welcome to vast.ai. If authentication fails, try again after a few seconds, and double check your ssh key.\nHave fun!\n"

	r := newDownloadReporter(&entity.Model{DownloadGB: 21.2})
	r.report([]byte(banner + "1000000000\n"))
	r.lastAt = time.Now().Add(-10 * time.Second)

	out := captureStdout(t, func() { r.report([]byte(banner + "7000000000\n")) })
	if !strings.Contains(out, "7.0 / 21.2 GB") {
		t.Errorf("banner defeated the parse; got %q", out)
	}
	if !strings.Contains(out, "33%") {
		t.Errorf("expected 33%%, got %q", out)
	}
}
