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

	// Track handles for cleanup. Don't use defer with CloseHandle for handles
	// that get closed or transferred mid-function — defer captures the value
	// at registration time and would double-close.
	var (
		ptyIn, pipeOut windows.Handle
		pipeIn, ptyOut windows.Handle
		hpc            windows.Handle
	)

	cleanup := func() {
		if hpc != 0 && hpc != windows.InvalidHandle {
			windows.ClosePseudoConsole(hpc)
			hpc = windows.InvalidHandle
		}
		for _, h := range []*windows.Handle{&ptyIn, &pipeOut, &pipeIn, &ptyOut} {
			if *h != 0 && *h != windows.InvalidHandle {
				windows.CloseHandle(*h)
				*h = windows.InvalidHandle
			}
		}
	}

	// Create pipes for ConPTY I/O.
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

	// Find shell executable. Prefer PowerShell, fall back to cmd.exe.
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
		nil,     // appName
		cmdLine, // commandLine
		nil,     // process security
		nil,     // thread security
		false,   // inherit handles
		windows.EXTENDED_STARTUPINFO_PRESENT, // creation flags
		nil, // environment (inherit)
		nil, // current directory (inherit)
		&si.StartupInfo,
		&pi,
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

	// Close the PTY-side pipe ends. The ConPTY holds its own references.
	// This ensures our read pipe gets EOF when the ConPTY closes.
	windows.CloseHandle(ptyIn)
	ptyIn = windows.InvalidHandle
	windows.CloseHandle(ptyOut)
	ptyOut = windows.InvalidHandle

	done := make(chan struct{})

	// ConPTY output -> WebSocket.
	// Use raw windows.ReadFile to avoid os.File overlapped I/O issues
	// with synchronous pipe handles on Windows.
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			var n uint32
			err := windows.ReadFile(pipeIn, buf, &n, nil)
			if err != nil || n == 0 {
				return
			}
			if werr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
				return
			}
		}
	}()

	// WebSocket -> ConPTY input.
	// Use raw windows.WriteFile for the same reason.
	go func() {
		for {
			msgType, msg, err := conn.ReadMessage()
			if err != nil {
				// Close the write pipe to signal EOF to ConPTY.
				windows.CloseHandle(pipeOut)
				pipeOut = windows.InvalidHandle
				return
			}

			if msgType == websocket.BinaryMessage && len(msg) > 0 && msg[0] == shellResizePrefix {
				if len(msg) >= 5 {
					rows := int16(msg[1])<<8 | int16(msg[2])
					cols := int16(msg[3])<<8 | int16(msg[4])
					_ = windows.ResizePseudoConsole(hpc, windows.Coord{X: cols, Y: rows})
				}
				continue
			}

			var written uint32
			if err := windows.WriteFile(pipeOut, msg, &written, nil); err != nil {
				log.Printf("[shell] write to ConPTY failed: %v", err)
				return
			}
		}
	}()

	<-done

	// Clean up: close ConPTY (sends EOF to process), then wait.
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

	// Close remaining pipe handles.
	if pipeIn != windows.InvalidHandle {
		windows.CloseHandle(pipeIn)
		pipeIn = windows.InvalidHandle
	}
	if pipeOut != windows.InvalidHandle {
		windows.CloseHandle(pipeOut)
		pipeOut = windows.InvalidHandle
	}
}
