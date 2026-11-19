// Package engine runs an external online-schema-change tool (gh-ost or pt-online-schema-change)
// under a watchdog. zdm orchestrates and verifies; the engine does the row copy.
package engine

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/siddik7181/zdm/internal/plan"
)

// Conn says where the engine connects. For gh-ost this is normally a replica.
type Conn struct {
	Host     string
	Port     int
	User     string
	Password string // never placed on the command line; written to a 0600 file
	// PrimaryHost is the real primary when Host is a replica (gh-ost --assume-master-host).
	PrimaryHost string
	// BehindProxy skips gh-ost's port check, needed with Docker port mapping or proxies.
	BehindProxy bool
}

// Spec is a runnable command plus files to delete afterwards.
type Spec struct {
	Bin   string
	Args  []string
	Files []string
}

// Cleanup removes credential files.
func (s Spec) Cleanup() {
	for _, f := range s.Files {
		_ = os.Remove(f)
	}
}

// credFile writes a 0600 [client] defaults file so the password stays out of `ps`.
func credFile(c Conn) (string, error) {
	f, err := os.CreateTemp("", "zdm-cred-*.cnf") // CreateTemp uses mode 0600
	if err != nil {
		return "", err
	}
	_, err = fmt.Fprintf(f, "[client]\nuser=%s\npassword=%s\n", c.User, c.Password)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// Build returns the command for a plan. With execute false the engine only does a dry run.
func Build(p plan.Plan, c Conn, execute bool) (Spec, error) {
	if err := p.Validate(); err != nil {
		return Spec{}, err
	}
	if c.Host == "" || c.Port == 0 || c.User == "" {
		return Spec{}, errors.New("engine: host, port and user are required")
	}
	cred, err := credFile(c)
	if err != nil {
		return Spec{}, err
	}
	switch p.Engine {
	case plan.GhOst:
		args := []string{
			"--conf=" + cred,
			"--host=" + c.Host, "--port=" + strconv.Itoa(c.Port),
			"--database=" + p.Schema, "--table=" + p.Table, "--alter=" + p.Alter,
			"--cut-over-lock-timeout-seconds=" + strconv.Itoa(p.Cutover.LockTimeoutSeconds),
			"--default-retries=" + strconv.Itoa(p.Cutover.MaxAttempts),
			"--max-lag-millis=" + strconv.Itoa(p.Cutover.MaxLagSeconds*1000),
			"--switch-to-rbr", "--verbose",
		}
		if c.PrimaryHost != "" {
			args = append(args, "--assume-master-host="+c.PrimaryHost)
		}
		if c.BehindProxy {
			args = append(args, "--skip-port-validation")
		}
		if !p.KeepOldTable {
			args = append(args, "--ok-to-drop-table")
		}
		if execute {
			args = append(args, "--execute")
		}
		return Spec{Bin: "gh-ost", Args: args, Files: []string{cred}}, nil
	default: // plan.PtOsc, guaranteed by Validate
		args := []string{
			"--alter=" + p.Alter,
			"--defaults-file=" + cred,
			fmt.Sprintf("h=%s,P=%d,D=%s,t=%s", c.Host, c.Port, p.Schema, p.Table),
			"--max-lag=" + strconv.Itoa(p.Cutover.MaxLagSeconds),
			"--lock-wait-timeout=" + strconv.Itoa(p.Cutover.LockTimeoutSeconds),
			"--progress=time,10",
		}
		if p.KeepOldTable {
			args = append(args, "--no-drop-old-table")
		}
		if execute {
			args = append(args, "--execute")
		} else {
			args = append(args, "--dry-run")
		}
		return Spec{Bin: "pt-online-schema-change", Args: args, Files: []string{cred}}, nil
	}
}

// ErrStalled means the engine printed nothing for longer than the stall timeout.
var ErrStalled = errors.New("engine stalled: no output within the stall timeout")

// Run starts the command, forwards each output line to onLine, and kills it if it goes silent.
func Run(ctx context.Context, s Spec, stall time.Duration, onLine func(string)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.Bin, s.Args...) //nolint:gosec // binary is fixed by Build; args are not shell-interpreted
	cmd.WaitDelay = 5 * time.Second
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", s.Bin, err)
	}

	var mu sync.Mutex
	last := time.Now()
	stalled := false
	done := make(chan struct{})
	go func() { // watchdog
		t := time.NewTicker(stall / 10)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				mu.Lock()
				if time.Since(last) > stall {
					stalled = true
					cancel()
				}
				mu.Unlock()
			}
		}
	}()
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			mu.Lock()
			last = time.Now()
			mu.Unlock()
			onLine(sc.Text())
		}
		_, _ = io.Copy(io.Discard, pr) // never block the child on an oversized line
	}()

	err := cmd.Wait()
	_ = pw.Close()
	<-readDone // no onLine call after Run returns
	close(done)
	mu.Lock()
	defer mu.Unlock()
	if stalled {
		return ErrStalled
	}
	return err
}
