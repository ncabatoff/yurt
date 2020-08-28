package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"

	"github.com/ncabatoff/yurt/binaries"
)

func main() {
	var (
		flagWorkDir = flag.String("workdir", "", "directory to store files")
		flagVersion = flag.String("version", "", "override default version")
		flagOS      = flag.String("os", runtime.GOOS, "override default OS")
		flagArch    = flag.String("arch", runtime.GOARCH, "override default arch")
	)
	flag.Parse()

	args := flag.Args()
	if len(args) != 1 {
		flag.Usage()
	}

	if *flagWorkDir == "" {
		*flagWorkDir = os.TempDir()
	}

	binmgr, err := binaries.NewDownloadManager(*flagWorkDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	path, err := binmgr.GetOSArch(args[0], *flagOS, *flagArch, *flagVersion)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(path)
}
