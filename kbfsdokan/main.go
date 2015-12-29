// Copyright 2015 Keybase Inc. All rights reserved.
// Use of this source code is governed by a BSD
// license that can be found in the LICENSE file.

// +build windows

// Keybase file system

package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/keybase/client/go/logger"
	"github.com/keybase/kbfs/libdokan"
	"github.com/keybase/kbfs/libkbfs"
)

var runtimeDir = flag.String("runtime-dir", os.Getenv("KEYBASE_RUNTIME_DIR"), "runtime directory")
var label = flag.String("label", os.Getenv("KEYBASE_LABEL"), "label to help identify if running as a service")
var mountType = flag.String("mount-type", defaultMountType, "mount type: default, force")
var debug = flag.Bool("debug", false, "Print debug messages")
var version = flag.Bool("version", false, "Print version")

const usageFormatStr = `Usage:
  kbfsdokan -version

  kbfsdokan [-debug] [-cpuprofile=path/to/dir] [-memprofile=path/to/dir]
    [-bserver=%s] [-mdserver=%s]
    [-runtime-dir=path/to/dir] [-label=label] [-mount-type=force]
    /path/to/mountpoint

  kbfsdokan [-debug] [-cpuprofile=path/to/dir] [-memprofile=path/to/dir]
    [-server-in-memory|-server-root=path/to/dir] [-localuser=<user>]
    [-runtime-dir=path/to/dir] [-label=label] [-mount-type=force]
    /path/to/mountpoint

`

func getUsageStr() string {
	defaultBServer := libkbfs.GetDefaultBServer()
	if len(defaultBServer) == 0 {
		defaultBServer = "host:port"
	}
	defaultMDServer := libkbfs.GetDefaultMDServer()
	if len(defaultMDServer) == 0 {
		defaultMDServer = "host:port"
	}
	return fmt.Sprintf(usageFormatStr, defaultBServer, defaultMDServer)
}

func start() *libdokan.Error {
	kbfsParams := libkbfs.AddFlags(flag.CommandLine)

	flag.Parse()

	if *version {
		fmt.Printf("%s-%s\n", libkbfs.Version, libkbfs.Build)
		return nil
	}

	if len(flag.Args()) < 1 {
		fmt.Print(getUsageStr())
		return libdokan.InitError("no mount specified")
	}

	if kbfsParams.Debug {
		log := logger.NewWithCallDepth("DOKAN", 1, os.Stderr)
		log.Configure("", true, "")
	}

	mountpoint := flag.Arg(0)
	var mounter libdokan.Mounter
	if *mountType == "force" {
		mounter = libdokan.NewForceMounter(mountpoint)
	} else {
		mounter = libdokan.NewDefaultMounter(mountpoint)
	}

	options := libdokan.StartOptions{
		KbfsParams: *kbfsParams,
		RuntimeDir: *runtimeDir,
		Label:      *label,
	}

	return libdokan.Start(mounter, options)
}

func main() {
	err := start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "kbfsdokan error: (%d) %s\n", err.Code, err.Message)

		os.Exit(err.Code)
	}
	os.Exit(0)
}
