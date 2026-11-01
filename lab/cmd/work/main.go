//go:build linux

// Command work is the calibration lab's reference service. Every request
// burns a fixed amount of CPU time (WORK_MS), so the cost per request is
// known exactly and goes into ArchSim as cpuMsPerReq.
package main

import (
	"log"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"syscall"
	"time"
	"unsafe"
)

func main() {
	workMs, err := strconv.ParseFloat(env("WORK_MS", "4"), 64)
	if err != nil || workMs <= 0 {
		log.Fatalf("WORK_MS must be a positive number, got %q", os.Getenv("WORK_MS"))
	}
	work := time.Duration(workMs * float64(time.Millisecond))

	http.HandleFunc("/work", func(w http.ResponseWriter, r *http.Request) {
		burn(work)
		w.WriteHeader(http.StatusNoContent)
	})
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	log.Printf("burning %v of CPU per request on :8080", work)
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

var sink uint64 // keeps the busy loop from being optimized away

// burn spins until the calling thread has used d of CPU time. Counting
// thread CPU time instead of wall time keeps the cost exact while the
// container is CFS-throttled or the goroutine is preempted.
func burn(d time.Duration) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	x := sink
	for start := threadCPU(); threadCPU()-start < d; {
		for range 1000 {
			x = x*6364136223846793005 + 1442695040888963407
		}
	}
	sink = x
}

const clockThreadCPUTimeID = 3 // CLOCK_THREAD_CPUTIME_ID

func threadCPU() time.Duration {
	var ts syscall.Timespec
	if _, _, errno := syscall.Syscall(syscall.SYS_CLOCK_GETTIME, clockThreadCPUTimeID, uintptr(unsafe.Pointer(&ts)), 0); errno != 0 {
		log.Fatalf("clock_gettime: %v", errno)
	}
	return time.Duration(ts.Nano())
}
