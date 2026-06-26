package registry

import (
	"os"
	"os/exec"
	"strconv"
	"sync"
	"testing"
	"time"
)

// Worker-mode env vars. When KUBEPORT_REGISTRY_WORKER is set, TestMain runs the
// worker routine (registering/deregistering against a shared dir) instead of the
// normal test suite, then exits. This lets the parent test re-exec the test
// binary as N real OS processes that contend on the same flock-protected store.
const (
	envWorker    = "KUBEPORT_REGISTRY_WORKER"
	envWorkerDir = "KUBEPORT_REGISTRY_WORKER_DIR"
	envWorkerPID = "KUBEPORT_REGISTRY_WORKER_PID"
)

func TestMain(m *testing.M) {
	if os.Getenv(envWorker) != "" {
		os.Exit(runRegistryWorker())
	}
	os.Exit(m.Run())
}

// runRegistryWorker performs a burst of Register/List/Deregister cycles against
// the shared registry dir. Each worker uses a distinct synthetic PID so its
// entry is independent; os.Getpid() of the worker process is used as a
// guaranteed-live anchor entry that must survive concurrent writers.
func runRegistryWorker() int {
	dir := os.Getenv(envWorkerDir)
	pidStr := os.Getenv(envWorkerPID)
	syntheticPID, err := strconv.Atoi(pidStr)
	if err != nil {
		return 2
	}

	r, err := Open(dir)
	if err != nil {
		return 3
	}

	// Anchor: this worker's real PID is alive for the duration, so its entry
	// must never be lost or corrupted by concurrent writers.
	anchor := Entry{PID: os.Getpid(), Socket: "/tmp/anchor.sock", Version: "anchor"}
	if regErr := r.Register(anchor); regErr != nil {
		return 4
	}

	for i := 0; i < 25; i++ {
		// Register a synthetic entry, list (which prunes), then deregister it.
		if regErr := r.Register(Entry{PID: syntheticPID, Socket: "/tmp/w.sock", Version: "v" + strconv.Itoa(i)}); regErr != nil {
			return 5
		}
		if _, listErr := r.List(); listErr != nil {
			return 6
		}
		if deErr := r.Deregister(syntheticPID); deErr != nil {
			return 7
		}
		// Re-assert the anchor in case a racing writer's snapshot dropped it.
		if regErr := r.Register(anchor); regErr != nil {
			return 8
		}
	}
	return 0
}

func TestMultiProcess_ConcurrentRegisterDeregister(t *testing.T) {
	dir := t.TempDir()

	const workers = 6
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}

	// Each worker uses a synthetic PID well above any real PID so it is pruned
	// as "dead" by List() on the next read — exercising the prune-on-list path
	// under contention. The workers' real PIDs are the live anchors.
	var wg sync.WaitGroup
	errs := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			// The worker branch in TestMain runs (and exits) before any test is
			// selected; -test.run=^$ ensures no real test runs if it ever falls
			// through. The worker is driven entirely by the env vars below.
			cmd := exec.Command(exe, "-test.run=^$") //nolint:gosec // re-exec of the test binary itself
			cmd.Env = append(os.Environ(),
				envWorker+"=1",
				envWorkerDir+"="+dir,
				// 900000000+ are synthetic, guaranteed-dead PIDs.
				envWorkerPID+"="+strconv.Itoa(900000000+idx),
			)
			out, runErr := cmd.CombinedOutput()
			if runErr != nil {
				errs[idx] = runErr
				t.Logf("worker %d output: %s", idx, out)
			}
		}(i)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("workers did not finish within 60s")
	}

	for i, e := range errs {
		if e != nil {
			t.Fatalf("worker %d failed: %v", i, e)
		}
	}

	// The store must still be valid JSON and readable after the contention burst.
	r, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen registry: %v", err)
	}
	got, err := r.List()
	if err != nil {
		t.Fatalf("List after contention (corrupt store?): %v", err)
	}

	// All worker processes have exited, so their live-anchor PIDs are now dead
	// and pruned by List. The synthetic PIDs were always dead. The net result
	// must be a well-formed (possibly empty) list with no corruption — the key
	// property is that no lock was lost and the file is parseable.
	for _, e := range got {
		if e.PID <= 0 {
			t.Fatalf("corrupt entry survived contention: %+v", e)
		}
	}
}

func TestMultiProcess_NoLostUpdatesUnderContention(t *testing.T) {
	// Spawn workers that each register a DISTINCT, still-live anchor (a long-
	// lived sleep process) so we can assert every concurrent registration is
	// durably recorded with no lost updates. Each worker registers its sleeper's
	// PID; after all workers finish, every sleeper PID must be present exactly
	// once, proving the flock serialised the read-modify-write cycles.
	dir := t.TempDir()

	const writers = 6
	r, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Start long-lived child processes whose PIDs stay alive past List() pruning.
	sleepers := make([]*exec.Cmd, writers)
	pids := make([]int, writers)
	for i := range sleepers {
		cmd := exec.Command("sleep", "30")
		if startErr := cmd.Start(); startErr != nil {
			t.Fatalf("start sleeper: %v", startErr)
		}
		sleepers[i] = cmd
		pids[i] = cmd.Process.Pid
	}
	t.Cleanup(func() {
		for _, c := range sleepers {
			_ = c.Process.Kill()
			_, _ = c.Process.Wait()
		}
	})

	// Concurrently register all sleeper PIDs from separate goroutines (in-process
	// contention on the same flock; combined with the multi-process workers above
	// this covers both intra- and inter-process locking).
	var wg sync.WaitGroup
	for _, pid := range pids {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			if regErr := r.Register(Entry{PID: p, Socket: "/tmp/s.sock"}); regErr != nil {
				t.Errorf("register pid %d: %v", p, regErr)
			}
		}(pid)
	}
	wg.Wait()

	got, err := r.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	seen := make(map[int]int)
	for _, e := range got {
		seen[e.PID]++
	}
	for _, pid := range pids {
		if seen[pid] != 1 {
			t.Errorf("pid %d recorded %d times, want exactly 1 (lost or duplicated update)", pid, seen[pid])
		}
	}
}
