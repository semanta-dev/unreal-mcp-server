package build

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// Result is the outcome of a compile.
type Result struct {
	Strategy    Strategy     `json:"strategy_used"`
	Success     bool         `json:"success"`
	Diagnostics []Diagnostic `json:"diagnostics"`
	RawTail     string       `json:"raw_tail,omitempty"`
	Reason      string       `json:"reason,omitempty"`
}

// ResolveStrategy picks a strategy. If requested is "auto", it classifies from
// the project's git working diff; otherwise it honors the request.
func ResolveStrategy(ctx context.Context, projectDir, requested string) (Strategy, string) {
	switch Strategy(requested) {
	case StrategyFull:
		return StrategyFull, "explicitly requested full rebuild"
	case StrategyLiveCoding:
		return StrategyLiveCoding, "explicitly requested Live Coding"
	}
	// auto
	if projectDir == "" || !IsRepo(ctx, projectDir) {
		return StrategyFull, "no git repo to diff; defaulting to full rebuild"
	}
	nameStatus, err := Run(ctx, projectDir, "diff", "--name-status", "HEAD")
	if err != nil {
		return StrategyFull, "git diff failed; defaulting to full rebuild"
	}
	diff, _ := Run(ctx, projectDir, "diff", "HEAD")
	return ClassifyStrategy(ParseNameStatus(nameStatus), diff)
}

// BuildBatArgs builds the Build.bat command line for a full editor rebuild.
func BuildBatArgs(engineDir, target, uproject string) (string, []string) {
	batPath := filepath.Join(engineDir, "Engine", "Build", "BatchFiles", "Build.bat")
	args := []string{target, "Win64", "Development", "-Project=" + uproject, "-WaitMutex"}
	return batPath, args
}

// RunFull runs Build.bat, streaming each line to progress, and returns parsed
// diagnostics. The editor must be closed first (caller's responsibility).
func RunFull(ctx context.Context, engineDir, target, uproject string, progress func(string)) (Result, error) {
	batPath, args := BuildBatArgs(engineDir, target, uproject)
	cmd := exec.CommandContext(ctx, "cmd.exe", append([]string{"/c", batPath}, args...)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, err
	}
	cmd.Stderr = cmd.Stdout // merge
	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("start Build.bat: %w", err)
	}

	var sb strings.Builder
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		sb.WriteString(line)
		sb.WriteByte('\n')
		if progress != nil {
			progress(line)
		}
	}
	runErr := cmd.Wait()

	output := sb.String()
	diags := ParseDiagnostics(output)
	res := Result{
		Strategy:    StrategyFull,
		Success:     runErr == nil && CountErrors(diags) == 0,
		Diagnostics: diags,
		RawTail:     tail(output, 4000),
	}
	// A nonzero exit with no parsed error is still a failure (surface the tail).
	if runErr != nil && res.Success {
		res.Success = false
	}
	return res, nil
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
