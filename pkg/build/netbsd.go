// Copyright 2018 syzkaller project authors. All rights reserved.
// Use of this source code is governed by Apache 2 LICENSE that can be found in the LICENSE file.

package build

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"github.com/google/syzkaller/pkg/mgrconfig"
	"github.com/google/syzkaller/pkg/osutil"
	"github.com/google/syzkaller/vm"
)

type netbsd struct{}

func (ctx netbsd) build(targetArch, vmType, kernelDir, outputDir, compiler, userspaceDir,
	cmdlineFile, sysctlFile string, config []byte) error {
	const kernelName = "GENERIC_SYZKALLER"
	confDir := fmt.Sprintf("%v/sys/arch/%v/conf", kernelDir, targetArch)
	compileDir := fmt.Sprintf("%v/sys/arch/%v/compile/obj/%v", kernelDir, targetArch, kernelName)

	// Compile the kernel with KASAN
	conf := []byte(`
include "arch/amd64/conf/GENERIC"

makeoptions    KASAN=1
options    KASAN
no options SVS
`)

	if err := osutil.WriteFile(filepath.Join(confDir, kernelName), conf); err != nil {
		return err
	}
	// Build tools before building kernel
	if _, err := osutil.RunCmd(10*time.Minute, kernelDir, "./build.sh", "-m", targetArch,
		"-U", "-u", "-j"+strconv.Itoa(runtime.NumCPU()), "tools"); err != nil {
		return extractRootCause(err)
	}

	// Build kernel
	if _, err := osutil.RunCmd(10*time.Minute, kernelDir, "./build.sh", "-m", targetArch,
		"-U", "-u", "-j"+strconv.Itoa(runtime.NumCPU()), "kernel="+kernelName); err != nil {
		return extractRootCause(err)
	}
	for _, s := range []struct{ dir, obj string }{
		{compileDir, "netbsd"},
		{compileDir, "netbsd.gdb"},
		{userspaceDir, "image"},
		{userspaceDir, "key"},
	} {
		fullSrc := filepath.Join(s.dir, s.obj)
		fullDst := filepath.Join(outputDir, s.obj)
		if err := osutil.CopyFile(fullSrc, fullDst); err != nil {
			return fmt.Errorf("failed to copy %v -> %v: %v", fullSrc, fullDst, err)
		}
	}

	if vmType == "gce" {
		return CopyKernelToDisk(outputDir)
	}
	return nil
}

func (ctx netbsd) clean(kernelDir string) error {
	// Building clean is fast enough and incremental builds in face of
	// changing config files don't work. Instead of optimizing for the
	// case where humans have to think, let's bludgeon it with a
	// machine.
	return nil
}

// Copy the compiled kernel to the qemu disk image using ssh
func CopyKernelToDisk(outputDir string) error {
	temp := []byte(`
{
	"qemu": "qemu-system-x86_64",
	"cpu": 1,
	"count": 1,
	"mem": 1024
}	`)
	VMconfig := (*json.RawMessage)(&temp)
	// Create config for booting the disk image
	cfg := &mgrconfig.Config{
		Workdir:      outputDir,
		Image:        filepath.Join(outputDir, "image"),
		SSHKey:       filepath.Join(outputDir, "key"),
		SSHUser:      "root",
		TargetOS:     "netbsd",
		TargetVMArch: "amd64",
		Type:         "qemu",
		VM:           *VMconfig,
	}
	// Create a VM pool
	pool, err := vm.Create(cfg, true)
	if err != nil {
		return fmt.Errorf("Failed to create a VM Pool : %v", err)
	}
	// Create a VM instance (We need only one)
	inst, err := pool.Create(0)
	if err != nil {
		return fmt.Errorf("Failed to create the VM Instance : %v", err)
	}
	defer inst.Close()
	// Copy the kernel into the disk image and replace it
	kernel, err := inst.Copy(filepath.Join(outputDir,"netbsd"))
	if err != nil {
		return fmt.Errorf("Error Copying the kernel %v: %v", kernel, err)
	}
	outc, errc, err := inst.Run(time.Minute, nil, "sync")
	if err != nil {
		return fmt.Errorf("Error syncing the kernel %v : %v", outc, errc)
	}
	return err
}
