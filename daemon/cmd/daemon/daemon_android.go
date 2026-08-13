//go:build cgo && android

package main

/*
#include <stdlib.h>
*/
import "C"

import (
	"log"
	"os"
	"time"
)

// startDaemon resolves to Java_io_openyou_app_MainActivity_startDaemon via JNI naming.
//
//export Java_io_openyou_app_MainActivity_startDaemon
func Java_io_openyou_app_MainActivity_startDaemon() {
	// Run BLOCKING on the Kotlin daemon thread so thread.isAlive tracks the daemon;
	// Run returns only on fatal errors (e.g. transient port-bind failure), so retry with backoff.
	for {
		err := Run(androidDataDir())
		if err == nil {
			return // clean shutdown — do not restart
		}
		log.Printf("openyou daemon exited: %v; restarting in 3s", err)
		time.Sleep(3 * time.Second)
	}
}

// androidDataDir prefers the app-private path: shared storage exposes DB/master.key
// to any app and orphans data on later boot success (split-brain). Shared is last resort.
func androidDataDir() string {
	private := "/data/data/io.openyou.app/files/openyou"
	for i := 0; i < 5; i++ {
		if err := os.MkdirAll(private, 0755); err == nil {
			return private
		}
		log.Printf("openyou: private data dir attempt %d failed, retrying", i+1)
		time.Sleep(500 * time.Millisecond)
	}
	log.Printf("openyou: app-private data dir unavailable, falling back to shared storage")
	for _, dir := range []string{"/storage/emulated/0/openyou", "/data/local/tmp/openyou"} {
		if err := os.MkdirAll(dir, 0755); err == nil {
			return dir
		}
	}
	return "/data/local/tmp/openyou"
}
