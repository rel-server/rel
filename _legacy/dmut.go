package main

import (
	"os"

	"github.com/ceymard/dmut/mutations"
	mut2 "github.com/ceymard/dmut/v2/mutations"
	"sales-way.com/server/sw"
)

// fileExists checks if a file exists and is not a directory before we
// try using it to prevent further errors.
func fileExists(filename string) bool {
	info, err := os.Stat(filename)
	if os.IsNotExist(err) {
		return false
	}
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// In this package, we will try to use dmut mutations as soon as we find them.
// We will try several files, but they all need index.
func tryRunDmut(srv *sw.SwServer, pguri string) error {
	// Try dmut v2 first
	var dmut2_fname = "/dmut2"
	info, err := os.Stat(dmut2_fname)
	if err == nil && info.IsDir() {
		srv.LogInfo("/dmut2 found, using dmut v2")
		err := mut2.ReadAndRunMutations(pguri, []string{dmut2_fname}, mut2.MutationRunnerOptions{
			Commit: true,
		})
		if err != nil {
			srv.DmutStatus = err.Error()
		}
		return err
	}

	// dmut
	var fname = "/dmut/index.dmut"
	if fileExists(fname) {
		err := mutations.ParseAndRunMutations(pguri, fname)
		if err != nil {
			srv.DmutStatus = err.Error()
			return err
		} else {
			srv.DmutStatus = "ok"
		}
	} else {
		srv.LogInfo("no dmut found, skipping mutations")
		srv.DmutStatus = "skipped"
	}
	return nil
}
