package setup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckDir(t *testing.T) {
	w.folderName = "downloadmanagersquared"
	file := filepath.Join(t.TempDir(), "a-file")
	writeFile(t, file)
	for _, c := range []struct {
		in, want string
		ok       bool
	}{
		{`C:\Program Files\downloadmanagersquared`, `C:\Program Files\downloadmanagersquared`, true},
		{`  "D:\Apps\dms\"  `, `D:\Apps\dms`, true},
		{`downloadmanagersquared`, "", false},
		{`\Apps\dms`, "", false},
		{`D:\`, "", false},
		{``, "", false},
		{file, "", false},
	} {
		got, problem := checkDir(c.in)
		if (problem == "") != c.ok || got != c.want {
			t.Errorf("checkDir(%q) = %q, %q", c.in, got, problem)
		}
	}
}

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}
