package engine

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/siddik7181/zdm/internal/plan"
)

func conn() Conn { return Conn{Host: "127.0.0.1", Port: 3306, User: "u", Password: "s3cret"} }

func TestBuildKeepsPasswordOffCommandLine(t *testing.T) {
	for _, eng := range []string{plan.GhOst, plan.PtOsc} {
		spec, err := Build(plan.Default("app", "t", "ADD COLUMN c INT NULL", eng), conn(), false)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.Join(spec.Args, " "), "s3cret") {
			t.Errorf("%s: password leaked into args", eng)
		}
		b, err := os.ReadFile(spec.Files[0])
		if err != nil || !strings.Contains(string(b), "password=s3cret") {
			t.Errorf("%s: credential file missing password: %v", eng, err)
		}
		if fi, _ := os.Stat(spec.Files[0]); fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: credential file mode %v", eng, fi.Mode().Perm())
		}
		spec.Cleanup()
		if _, err := os.Stat(spec.Files[0]); err == nil {
			t.Errorf("%s: credential file not removed", eng)
		}
	}
}

func TestBuildExecuteAndOptions(t *testing.T) {
	p := plan.Default("app", "t", "ADD COLUMN c INT NULL", plan.GhOst)
	c := conn()
	c.PrimaryHost, c.BehindProxy = "primary", true
	dry, _ := Build(p, c, false)
	defer dry.Cleanup()
	if slices.Contains(dry.Args, "--execute") {
		t.Error("dry run must not pass --execute")
	}
	for _, want := range []string{"--assume-master-host=primary", "--skip-port-validation", "--cut-over-lock-timeout-seconds=2"} {
		if !slices.Contains(dry.Args, want) {
			t.Errorf("missing %s in %v", want, dry.Args)
		}
	}
	p.KeepOldTable = false
	run, _ := Build(p, c, true)
	defer run.Cleanup()
	if !slices.Contains(run.Args, "--execute") || !slices.Contains(run.Args, "--ok-to-drop-table") {
		t.Errorf("args: %v", run.Args)
	}
	pt, _ := Build(plan.Default("app", "t", "ADD COLUMN c INT NULL", plan.PtOsc), c, false)
	defer pt.Cleanup()
	if !slices.Contains(pt.Args, "--dry-run") || !slices.Contains(pt.Args, "h=127.0.0.1,P=3306,D=app,t=t") {
		t.Errorf("pt-osc args: %v", pt.Args)
	}
}

func TestBuildRejectsBadInput(t *testing.T) {
	if _, err := Build(plan.Plan{}, conn(), false); err == nil {
		t.Error("invalid plan accepted")
	}
	if _, err := Build(plan.Default("a", "t", "ADD COLUMN c INT", plan.GhOst), Conn{}, false); err == nil {
		t.Error("missing connection accepted")
	}
}

func TestRunForwardsOutputAndExitStatus(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	err := Run(context.Background(), Spec{Bin: "sh", Args: []string{"-c", "echo a; echo b >&2; exit 3"}}, time.Minute,
		func(l string) { mu.Lock(); lines = append(lines, l); mu.Unlock() })
	if err == nil {
		t.Fatal("non-zero exit must be an error")
	}
	mu.Lock()
	defer mu.Unlock()
	slices.Sort(lines)
	if !slices.Equal(lines, []string{"a", "b"}) {
		t.Fatalf("lines = %v", lines)
	}
}

func TestRunKillsStalledProcess(t *testing.T) {
	start := time.Now()
	err := Run(context.Background(), Spec{Bin: "sh", Args: []string{"-c", "echo hi; sleep 30"}}, 500*time.Millisecond, func(string) {})
	if !errors.Is(err, ErrStalled) {
		t.Fatalf("want ErrStalled, got %v", err)
	}
	if time.Since(start) > 15*time.Second {
		t.Fatal("watchdog too slow")
	}
}

func TestRunReportsMissingBinary(t *testing.T) {
	if err := Run(context.Background(), Spec{Bin: "zdm-no-such-binary"}, time.Minute, func(string) {}); err == nil {
		t.Fatal("want start error")
	}
}
