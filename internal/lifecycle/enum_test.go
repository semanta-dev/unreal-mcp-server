package lifecycle

import "testing"

func TestExtractToken(t *testing.T) {
	cases := []struct{ cmd, want string }{
		{`C:/UE/UnrealEditor.exe X.uproject -MCPInstanceToken=mcp-abc123 -nosplash`, "mcp-abc123"},
		{`ue.exe -MCPInstanceToken=tok-at-end`, "tok-at-end"},
		{`ue.exe "X.uproject" -MCPInstanceToken=quoted"`, "quoted"},
		{`ue.exe -other=1 -nosplash`, ""},      // absent
		{`ue.exe -MCPInstanceTokenX=nope`, ""}, // must be exactly flag=
	}
	for _, c := range cases {
		if got := extractToken(c.cmd, "-MCPInstanceToken"); got != c.want {
			t.Errorf("extractToken(%q) = %q, want %q", c.cmd, got, c.want)
		}
	}
}

func TestNamesProject(t *testing.T) {
	cmd := `"D:\Unreal\Engine\Binaries\Win64\UnrealEditor.exe" "C:\Games\Aesir\Aesir.uproject" -AutoDeclinePackageRecovery`
	if !namesProject(cmd, "c:/games/aesir/Aesir.uproject") {
		t.Fatal("same project with other slashes/case should match")
	}
	if namesProject(cmd, "C:/Games/Other/Other.uproject") || namesProject(cmd, "") {
		t.Fatal("another project (or none) must not match")
	}
}
