//go:build !windows

package main

import (
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

func handleShell(w http.ResponseWriter, r *http.Request) {
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[shell] WebSocket upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	// Determine shell: $SHELL env, then platform default, then fallbacks.
	shell := os.Getenv("SHELL")
	if shell == "" {
		// macOS default is zsh since Catalina; Linux typically has bash.
		if runtime.GOOS == "darwin" {
			shell = "/bin/zsh"
		} else {
			shell = "/bin/bash"
		}
	}
	if _, err := os.Stat(shell); err != nil {
		shell = "/bin/sh"
	}

	// Write shell config files that enable colors for common tools.
	// Works for both zsh (ZDOTDIR) and bash (--rcfile).
	hopShellDir := filepath.Join(os.TempDir(), "hop-shell")
	os.MkdirAll(hopShellDir, 0755)

	// Shared color config (sourced by both zsh and bash rc files).
	colorConf := `
export CLICOLOR=1
export CLICOLOR_FORCE=1
export LSCOLORS="GxFxCxDxBxegedabagaced"
export LS_COLORS="di=36:ln=35:so=32:pi=33:ex=31:bd=34;46:cd=34;43:su=30;41:sg=30;46:tw=30;42:ow=30;43"
alias grep='grep --color=auto'
alias fgrep='fgrep --color=auto'
alias egrep='egrep --color=auto'
alias diff='diff --color=auto'
`
	// Zsh: colored prompt with git branch
	zshRC := colorConf + `
autoload -U colors && colors 2>/dev/null
# Git branch in prompt
__hop_git_branch() {
  local b=$(git symbolic-ref --short HEAD 2>/dev/null)
  [ -n "$b" ] && echo " %F{yellow}($b)%f"
}
setopt PROMPT_SUBST
PS1='%F{green}%n@%m%f %F{cyan}%~%f$(__hop_git_branch) %F{white}%#%f '
`
	// Bash: colored prompt with git branch
	bashRC := colorConf + `
__hop_git_branch() {
  local b=$(git symbolic-ref --short HEAD 2>/dev/null)
  [ -n "$b" ] && echo " \[\033[33m\]($b)\[\033[0m\]"
}
PS1='\[\033[32m\]\u@\h\[\033[0m\] \[\033[36m\]\w\[\033[0m\]$(__hop_git_branch) \[\033[37m\]\$\[\033[0m\] '
`
	os.WriteFile(filepath.Join(hopShellDir, ".zshrc"), []byte(zshRC), 0644)
	os.WriteFile(filepath.Join(hopShellDir, ".zshenv"), []byte(colorConf), 0644)
	bashRCPath := filepath.Join(hopShellDir, ".bashrc")
	os.WriteFile(bashRCPath, []byte(bashRC), 0644)

	// Launch shell with color config.
	// ZDOTDIR for zsh, BASH_ENV for bash (sources our rc on every invocation).
	var cmd *exec.Cmd
	if strings.HasSuffix(shell, "bash") {
		cmd = exec.Command(shell, "--rcfile", bashRCPath)
	} else {
		cmd = exec.Command(shell, "-l")
	}
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		"CLICOLOR=1",
		"CLICOLOR_FORCE=1",
		"LSCOLORS=GxFxCxDxBxegedabagaced",
		`LS_COLORS=di=36:ln=35:so=32:pi=33:ex=31:bd=34;46:cd=34;43:su=30;41:sg=30;46:tw=30;42:ow=30;43`,
		"ZDOTDIR="+hopShellDir,
		"BASH_ENV="+bashRCPath, // bash sources this for child shells too
		"ENV="+bashRCPath,      // sh sources this
	)

	ptmx, err := pty.Start(cmd)
	if err != nil {
		log.Printf("[shell] PTY start failed: %v", err)
		conn.WriteMessage(websocket.TextMessage, []byte("Error: "+err.Error()))
		return
	}
	defer func() {
		ptmx.Close()
		cmd.Process.Kill()
		// Wait with timeout to avoid blocking on zombie processes.
		waitDone := make(chan struct{})
		go func() {
			cmd.Wait()
			close(waitDone)
		}()
		select {
		case <-waitDone:
		case <-time.After(5 * time.Second):
			log.Printf("[shell] process %d did not exit after kill, abandoning", cmd.Process.Pid)
		}
	}()

	_ = pty.Setsize(ptmx, &pty.Winsize{Rows: 24, Cols: 80})

	done := make(chan struct{})

	// PTY stdout -> WebSocket
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if err != nil {
				return
			}
			if err := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
				return
			}
		}
	}()

	// WebSocket -> PTY stdin
	go func() {
		for {
			msgType, msg, err := conn.ReadMessage()
			if err != nil {
				ptmx.Close()
				return
			}

			if msgType == websocket.BinaryMessage && len(msg) > 0 && msg[0] == shellResizePrefix {
				if len(msg) >= 5 {
					rows := uint16(msg[1])<<8 | uint16(msg[2])
					cols := uint16(msg[3])<<8 | uint16(msg[4])
					_ = pty.Setsize(ptmx, &pty.Winsize{Rows: rows, Cols: cols})
				}
				continue
			}

			ptmx.Write(msg)
		}
	}()

	<-done
}
