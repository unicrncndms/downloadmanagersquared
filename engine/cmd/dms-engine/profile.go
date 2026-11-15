package main

import (
	"log"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"

	"downloadmanagersquared/engine/internal/engine"
)

// startProfile, with DMS_PROFILE=1, writes cpu.pprof from now until the
// returned stop, and heap.pprof and mutex.pprof at stop, into dir. Files only:
// no listener, so no firewall prompt. Off unless the variable is set.
func startProfile(dir string) (stop func()) {
	if os.Getenv("DMS_PROFILE") != "1" {
		return func() {}
	}
	engine.TickTiming = true
	runtime.SetMutexProfileFraction(5)
	cpu, err := os.Create(filepath.Join(dir, "cpu.pprof"))
	if err == nil {
		err = pprof.StartCPUProfile(cpu)
	}
	if err != nil {
		log.Printf("profiling: %v", err)
	}
	log.Printf("profiling on: cpu.pprof, heap.pprof and mutex.pprof go to %s", dir)
	return func() {
		pprof.StopCPUProfile()
		if cpu != nil {
			cpu.Close()
		}
		for _, name := range []string{"heap", "mutex"} {
			f, err := os.Create(filepath.Join(dir, name+".pprof"))
			if err == nil {
				err = pprof.Lookup(name).WriteTo(f, 0)
				f.Close()
			}
			if err != nil {
				log.Printf("profiling %s: %v", name, err)
			}
		}
	}
}
