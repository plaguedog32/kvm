//go:build windows

// jetkvm-guest is the Windows half of the paired USB clipboard integration.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/jetkvm/kvm/internal/guestbridge"
)

func main() {
	service := flag.Bool("service", false, "run as the installed Windows service")
	worker := flag.Bool("clipboard-worker", false, "internal per-user clipboard worker")
	pipe := flag.String("pipe", "", "internal named pipe")
	parent := flag.Uint("parent-pid", 0, "internal parent service process")
	session := flag.Uint("session-id", 0, "internal physical console session")
	importFile := flag.String("import-pairing", "", "import one-time pairing JSON; use - for standard input")
	install := flag.Bool("install-service", false, "install/start the Windows service after importing pairing")
	flag.Parse()
	var err error
	switch {
	case *worker:
		// This branch never reads pairing configuration or opens a HID device.
		if *parent > 0xffffffff || *session > 0xffffffff {
			err = guestbridge.ErrProtocol
			break
		}
		err = guestbridge.RunClipboardWorker(uint32(*parent), uint32(*session), *pipe)
	case *service:
		err = guestbridge.RunWindowsService()
	case *importFile != "":
		var input io.ReadCloser = os.Stdin
		if *importFile != "-" {
			input, err = os.Open(*importFile)
		}
		if err == nil {
			err = guestbridge.ImportWindowsPairing(input)
			input.Close()
		}
		if err == nil {
			fmt.Println("Pairing configuration protected for the Windows service.")
		}
	case *install:
		err = guestbridge.InstallWindowsService()
	default:
		flag.Usage()
		os.Exit(2)
	}
	if err != nil {
		// Worker failures are handled by its parent and must not create UI.
		if !*worker {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}
}
