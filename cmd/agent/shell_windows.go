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

	// The ConPTY API expects the HPCON handle value as lpValue directly,
	// not a pointer to it. Convert uintptr→unsafe.Pointer via double-cast
	// to avoid go vet's uintptr-to-Pointer conversion warning.
	if err := attrList.Update(
		windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE,
		*(*unsafe.Pointer)(unsafe.Pointer(&hpc)),
		unsafe.Sizeof(hpc),
	); err != nil {
		log.Printf("[shell] UpdateProcThreadAttribute failed: %v", err)
		cleanup()
		conn.WriteMessage(websocket.TextMessage, []byte("Error: "+err.Error()))
		return
	}

	// Find shell executable.
	//
	// Shell preference:
	//  1. pwsh.exe (PowerShell 7) if installed — works reliably in ConPTY,
	//     defaults to UTF-8.
	//  2. cmd.exe — reliable in ConPTY, plus `chcp 65001` for UTF-8 codepage
	//     so non-ASCII output (e.g. box drawing, file-system non-ASCII names)
	//     renders correctly in xterm.js.
	//  3. Windows PowerShell 5.1 (powershell.exe) — AVOID for now. Debugging
	//     showed it emits only terminal-setup ANSI codes + title-set OSC
	//     sequences and NEVER emits its prompt string when launched inside
	//     ConPTY by a non-console parent process. Rather than shim around
	//     that (tried -NoExit -Command injection, made it worse), route
	//     users to cmd.exe unless PS7 is available. PowerShell 5.1 stays
	//     accessible — users can type `powershell` at the cmd prompt.
	shell := os.Getenv("COMSPEC")
	if shell == "" {
		shell = `C:\Windows\System32\cmd.exe`
	}
	cmdLineStr := `"` + shell + `" /K "chcp 65001 > nul"`
	if _, err := os.Stat(`C:\Program Files\PowerShell\7\pwsh.exe`); err == nil {
		shell = `C:\Program Files\PowerShell\7\pwsh.exe`
		cmdLineStr = `"` + shell + `" -NoLogo`
	}

	cmdLine, err := windows.UTF16PtrFromString(cmdLineStr)
	if err != nil {
		log.Printf("[shell] UTF16PtrFromString failed: %v", err)
		cleanup()
		conn.WriteMessage(websocket.TextMessage, []byte("Error: "+err.Error()))
		return
	}

	// STARTF_USESTDHANDLES is set with zero StdInput/StdOutput/StdError by
	// design — matches UserExistsError/conpty. Without this flag, Windows
	// falls back to handing the child our inherited console's stdio, and
	// cmd.exe's prompt ends up written to the agent's parent stream instead
	// of the pseudoconsole pipe. Setting the flag (even with zero handles)
	// forces Windows to use the PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE pipes
	// for the child's stdio instead. Verified empirically against the
	// UE-conpty library source.
	si := &windows.StartupInfoEx{
		StartupInfo: windows.StartupInfo{
			Cb:    uint32(unsafe.Sizeof(windows.StartupInfoEx{})),
			Flags: windows.STARTF_USESTDHANDLES,
		},
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

	// Close only the PTY-side read end of the input pipe. DON'T close
	// ptyOut (the child-side write end of the output pipe) here —
	// ConPTY may not duplicate that handle, and closing it before the
	// child writes anything cuts off the output path. We close ptyOut
	// later in cleanup when the session ends.
	closeHandle(&ptyIn)

	done := make(chan struct{})

	// ConPTY output -> WebSocket
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			var n uint32
			if err := windows.ReadFile(pipeIn, buf, &n, nil); err != nil {
				return
			}
			if n == 0 {
				continue
			}
			if werr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
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
