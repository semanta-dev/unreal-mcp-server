package headless

import (
	"strings"
	"testing"
)

func TestArgsCommandlet(t *testing.T) {
	c := Cmd{Project: "P.uproject", Commandlet: "DataValidation", NullRHI: true, Unattended: true}
	got := strings.Join(c.Args(), " ")
	for _, want := range []string{"P.uproject", "-run=DataValidation", "-nullrhi", "-unattended", "-stdout", "-nop4"} {
		if !strings.Contains(got, want) {
			t.Errorf("args missing %q: %s", want, got)
		}
	}
}

func TestArgsRunTestsUsesTestExitNotQuit(t *testing.T) {
	c := Cmd{Project: "P.uproject", RunTests: "Project.Functional"}
	got := strings.Join(c.Args(), " ")
	if !strings.Contains(got, `-ExecCmds=Automation RunTests Project.Functional`) {
		t.Fatalf("runtests exec malformed: %s", got)
	}
	if !strings.Contains(got, `-TestExit=Automation Test Queue Empty`) {
		t.Fatalf("runtests must wait via -TestExit, not race Quit: %s", got)
	}
	if strings.Contains(got, ";Quit") {
		t.Fatalf("Quit must not be appended to the async RunTests batch: %s", got)
	}
}

func TestArgsExecCmdsJoined(t *testing.T) {
	c := Cmd{Project: "P.uproject", ExecCmds: []string{"stat unit", "Quit"}}
	got := strings.Join(c.Args(), " ")
	if !strings.Contains(got, `-ExecCmds=stat unit;Quit`) {
		t.Fatalf("execcmds join wrong: %s", got)
	}
}

func TestExecutableDefaults(t *testing.T) {
	if (Cmd{}).Executable() != EditorCmdName() {
		t.Error("empty editor should default to EditorCmdName")
	}
	if (Cmd{Editor: "X"}).Executable() != "X" {
		t.Error("explicit editor should win")
	}
}

func TestWindowsCmdLineValueOnlyQuoting(t *testing.T) {
	// The whole point: -ExecCmds / -TestExit values must be quoted immediately
	// after '=' (UE's FParse), NOT whole-token-quoted (Go's default EscapeArg).
	line := buildWindowsCmdLine(`C:/UE/UnrealEditor-Cmd.exe`, []string{
		`C:/My Game/P.uproject`,
		`-ExecCmds=Automation RunTests Project.Functional`,
		`-TestExit=Automation Test Queue Empty`,
		`-nullrhi`,
	})
	if !strings.Contains(line, `-ExecCmds="Automation RunTests Project.Functional"`) {
		t.Errorf("ExecCmds not value-quoted after '=': %s", line)
	}
	if !strings.Contains(line, `-TestExit="Automation Test Queue Empty"`) {
		t.Errorf("TestExit not value-quoted after '=': %s", line)
	}
	// A bare path with spaces is whole-quoted; a no-space flag is untouched.
	if !strings.Contains(line, `"C:/My Game/P.uproject"`) {
		t.Errorf("uproject path not whole-quoted: %s", line)
	}
	if strings.Contains(line, `"-ExecCmds=`) || strings.Contains(line, `"-TestExit=`) {
		t.Errorf("flag must NOT be inside the quotes (breaks UE FParse): %s", line)
	}
	if !strings.Contains(line, ` -nullrhi`) {
		t.Errorf("no-space flag should be untouched: %s", line)
	}
}

func TestEditorCmdFromEngineDir(t *testing.T) {
	p := EditorCmdFromEngineDir("D:/UE_5.7")
	if !strings.Contains(p, "Engine") || !strings.Contains(p, "Binaries") || !strings.HasSuffix(p, EditorCmdName()) {
		t.Fatalf("engine-dir path wrong: %s", p)
	}
}
