//go:build windows

package main

import (
	"log"
	"net/http"
	"os"
	"time"
	"unsafe"

	"github.com/gorilla/websocket"
	"golang.org/x/sys/windows"
)

func handleShell(w http.ResponseWriter, r *http.Request) {
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[shell] WebSocket upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	var (
		ptyIn, pipeOut windows.Handle
		pipeIn, ptyOut windows.Handle
		hpc            windows.Handle
	)

	closeHandle := func(h *windows.Handle) {
		if *h != 0 && *h != windows.InvalidHandle {
			windows.CloseHandle(*h)
			*h = windows.InvalidHandle
		}
	}

	cleanup := func() {
		if hpc != 0 && hpc != windows.InvalidHandle {
			windows.ClosePseudoConsole(hpc)
			hpc = windows.InvalidHandle
		}
		closeHandle(&ptyIn)
		closeHandle(&pipeOut)
		closeHandle(&pipeIn)
		closeHandle(&ptyOut)
	}

	// Create pipes for ConPTY I/O.
	// Pipe 1: pipeOut (we write) -> ptyIn (ConPTY reads) = process stdin
	// Pipe 2: ptyOut (ConPTY writes) -> pipeIn (we read) = process stdout
	if err := windows.CreatePipe(&ptyIn, &pipeOut, nil, 0); err != nil {
		log.Printf("[shell] CreatePipe (input) failed: %v", err)
		conn.WriteMessage(websocket.TextMessage, []byte("Error: "+err.Error()))
		return
	}
	if err := windows.CreatePipe(&pipeIn, &ptyOut, nil, 0); err != nil {
		log.Printf("[shell] CreatePipe (output) failed: %v", err)
		cleanup()
		conn.WriteMessage(websocket.TextMessage, []byte("Error: "+err.Error()))
		return
	}

	// Create pseudo console (ConPTY).
	size := windows.Coord{X: 80, Y: 24}
	if err := windows.CreatePseudoConsole(size, ptyIn, ptyOut, 0, &hpc); err != nil {
		log.Printf("[shell] CreatePseudoConsole failed: %v", err)
		cleanup()
		conn.WriteMessage(websocket.TextMessage, []byte("Error: "+err.Error()))
		return
	}

	// Set up proc thread attribute list with the pseudo console.
	attrList, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		log.Printf("[shell] NewProcThreadAttributeList failed: %v", err)
		cleanup()
		conn.WriteMessage(websocket.TextMessage, []byte("Error: "+err.Error()))
		return
	}
	defer attrList.Delete()

	if err := attrList.Update(
		windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE,
		unsafe.Pointer(&hpc),
		unsafe.Sizeof(hpc),
	); err != nil {
		log.Printf("[shell] UpdateProcThreadAttribute failed: %v", err)
		cleanup()
		conn.WriteMessage(websocket.TextMessage, []byte("Error: "+err.Error()))
		return
	}

	// Find shell executable.
	shell := os.Getenv("COMSPEC")
	if shell == "" {
		shell = `C:\Windows\System32\cmd.exe`
	}
	if ps, err := os.Stat(`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`); err == nil && !ps.IsDir() {
		shell = `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`
	}

	cmdLine, err := windows.UTF16PtrFromString(shell)
	if err != nil {
		log.Printf("[shell] UTF16PtrFromString failed: %v", err)
		cleanup()
		conn.WriteMessage(websocket.TextMessage, []byte("Error: "+err.Error()))
		return
	}

	si := &windows.StartupInfoEx{
		StartupInfo:             windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfoEx{}))},
		ProcThreadAttributeList: attrList.List(),
	}
	var pi windows.ProcessInformation

	err = windows.CreateProcess(
		nil, cmdLine, nil, nil, false,
		windows.EXTENDED_STARTUPINFO_PRESENT,
		nil, nil, &si.StartupInfo, &pi,
	)
	if err != nil {
		log.Printf("[shell] CreateProcess failed: %v", err)
		cleanup()
		conn.WriteMessage(websocket.TextMessage, []byte("Error: "+err.Error()))
		return
	}
	defer windows.CloseHandle(pi.Thread)
	defer windows.CloseHandle(pi.Process)

	log.Printf("[shell] started %s (PID %d)", shell, pi.ProcessId)

	// Close only the PTY-side read end of the input pipe.
	// Keep ptyOut open — closing it would break the output pipe before
	// ConPTY has written anything (ConPTY may not duplicate handles).
	closeHandle(&ptyIn)

	done := make(chan struct{})

	// ConPTY output -> WebSocket
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			var n uint32
			err := windows.ReadFile(pipeIn, buf, &n, nil)
			if err != nil {
				log.Printf("[shell] ReadFile ended: %v (n=%d)", err, n)
				return
			}
			if n == 0 {
				continue
			}
			if werr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
				log.Printf("[shell] WebSocket write failed: %v", werr)
				return
			}
		}
	}()

	// WebSocket -> ConPTY input
	go func() {
		for {
			msgType, msg, err := conn.ReadMessage()
			if err != nil {
				log.Printf("[shell] WebSocket read ended: %v", err)
				closeHandle(&pipeOut)
				return
			}

			if msgType == websocket.BinaryMessage && len(msg) > 0 && msg[0] == shellResizePrefix {
				if len(msg) >= 5 {
					rows := int16(msg[1])<<8 | int16(msg[2])
					cols := int16(msg[3])<<8 | int16(msg[4])
					_ = windows.ResizePseudoConsole(hpc, windows.Coord{X: cols, Y: rows})
					log.Printf("[shell] resized to %dx%d", cols, rows)
				}
				continue
			}

			var written uint32
			if err := windows.WriteFile(pipeOut, msg, &written, nil); err != nil {
				log.Printf("[shell] WriteFile to ConPTY failed: %v", err)
				return
			}
		}
	}()

	<-done

	// Clean up process.
	windows.ClosePseudoConsole(hpc)
	hpc = windows.InvalidHandle

	result, _ := windows.WaitForSingleObject(pi.Process, 3000)
	if result == uint32(windows.WAIT_TIMEOUT) {
		windows.TerminateProcess(pi.Process, 1)
		waitDone := make(chan struct{})
		go func() {
			windows.WaitForSingleObject(pi.Process, windows.INFINITE)
			close(waitDone)
		}()
		select {
		case <-waitDone:
		case <-time.After(5 * time.Second):
			log.Printf("[shell] process %d did not exit after terminate, abandoning", pi.ProcessId)
		}
	}

	log.Printf("[shell] session ended (PID %d)", pi.ProcessId)
	closeHandle(&pipeIn)
	closeHandle(&pipeOut)
	closeHandle(&ptyOut)
}
