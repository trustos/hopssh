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

	// Create pipes for ConPTY I/O.
	var ptyIn, pipeOut windows.Handle
	if err := windows.CreatePipe(&ptyIn, &pipeOut, nil, 0); err != nil {
		log.Printf("[shell] CreatePipe (input) failed: %v", err)
		conn.WriteMessage(websocket.TextMessage, []byte("Error: "+err.Error()))
		return
	}
	defer windows.CloseHandle(ptyIn)
	defer windows.CloseHandle(pipeOut)

	var pipeIn, ptyOut windows.Handle
	if err := windows.CreatePipe(&pipeIn, &ptyOut, nil, 0); err != nil {
		log.Printf("[shell] CreatePipe (output) failed: %v", err)
		conn.WriteMessage(websocket.TextMessage, []byte("Error: "+err.Error()))
		return
	}
	defer windows.CloseHandle(pipeIn)
	defer windows.CloseHandle(ptyOut)

	// Create pseudo console (ConPTY).
	size := windows.Coord{X: 80, Y: 24}
	var hpc windows.Handle
	if err := windows.CreatePseudoConsole(size, ptyIn, ptyOut, 0, &hpc); err != nil {
		log.Printf("[shell] CreatePseudoConsole failed: %v", err)
		conn.WriteMessage(websocket.TextMessage, []byte("Error: "+err.Error()))
		return
	}
	defer windows.ClosePseudoConsole(hpc)

	// Set up proc thread attribute list with the pseudo console.
	attrList, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		log.Printf("[shell] NewProcThreadAttributeList failed: %v", err)
		conn.WriteMessage(websocket.TextMessage, []byte("Error: "+err.Error()))
		return
	}
	defer attrList.Delete()

	// Pass the pseudo console handle to the attribute list.
	// The ConPTY API expects a raw handle value, not a pointer to a handle.
	if err := attrList.Update(
		windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE,
		unsafe.Pointer(&hpc),
		unsafe.Sizeof(hpc),
	); err != nil {
		log.Printf("[shell] UpdateProcThreadAttribute failed: %v", err)
		conn.WriteMessage(websocket.TextMessage, []byte("Error: "+err.Error()))
		return
	}

	// Find shell executable. Prefer PowerShell, fall back to cmd.exe.
	shell := os.Getenv("COMSPEC")
	if shell == "" {
		shell = `C:\Windows\System32\cmd.exe`
	}
	// Try PowerShell if available.
	if ps, err := os.Stat(`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`); err == nil && !ps.IsDir() {
		shell = `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`
	}

	cmdLine, err := windows.UTF16PtrFromString(shell)
	if err != nil {
		log.Printf("[shell] UTF16PtrFromString failed: %v", err)
		conn.WriteMessage(websocket.TextMessage, []byte("Error: "+err.Error()))
		return
	}

	si := &windows.StartupInfoEx{
		StartupInfo:            windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfoEx{}))},
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
		conn.WriteMessage(websocket.TextMessage, []byte("Error: "+err.Error()))
		return
	}
	defer windows.CloseHandle(pi.Thread)
	defer windows.CloseHandle(pi.Process)

	// Close the PTY-side handles now that the child process has them.
	// This ensures reads on pipeIn get EOF when the process exits.
	windows.CloseHandle(ptyIn)
	ptyIn = windows.InvalidHandle
	windows.CloseHandle(ptyOut)
	ptyOut = windows.InvalidHandle

	// Wrap pipe handles as os.File for Go I/O.
	pipeReader := os.NewFile(uintptr(pipeIn), "conpty-read")
	pipeWriter := os.NewFile(uintptr(pipeOut), "conpty-write")

	done := make(chan struct{})

	// ConPTY output -> WebSocket
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, err := pipeReader.Read(buf)
			if n > 0 {
				if werr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// WebSocket -> ConPTY input
	go func() {
		for {
			msgType, msg, err := conn.ReadMessage()
			if err != nil {
				pipeWriter.Close()
				return
			}

			if msgType == websocket.BinaryMessage && len(msg) > 0 && msg[0] == shellResizePrefix {
				if len(msg) >= 5 {
					rows := int16(msg[1])<<8 | int16(msg[2])
					cols := int16(msg[3])<<8 | int16(msg[4])
					newSize := windows.Coord{X: cols, Y: rows}
					_ = windows.ResizePseudoConsole(hpc, newSize)
				}
				continue
			}

			pipeWriter.Write(msg)
		}
	}()

	// Wait for output to finish (process exited and pipe drained).
	<-done

	// Clean up: terminate the process if still running.
	result, _ := windows.WaitForSingleObject(pi.Process, 0)
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

	// Prevent double-close of handles wrapped in os.File.
	pipeIn = windows.InvalidHandle
	pipeOut = windows.InvalidHandle

}
