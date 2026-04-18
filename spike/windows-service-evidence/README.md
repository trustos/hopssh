# Windows Service (SCM) integration evidence

Date: 2026-04-18
Host: Windows 11 Home 25H2 ARM64 (UTM VM), mesh IP 10.42.1.10

Files:
- `00-install.txt`        — `hop-agent install` output + `sc.exe qc` + `Get-Service`
- `01-running-state.txt`  — service Running, BinaryPath has `serve --config-dir`
- `02-graceful-stop.log`  — `hop-agent stop` + last 8 log lines
  (shows `Shutting down agent... [dns-proxy] stopped ... Goodbye`)
- `03-restart.txt`        — restart + mesh-ping recovery
- `04-uninstall.txt`      — uninstall + Get-Service gone
- `05-reinstall-ping.txt` — final state
- `06-harness-run.log`    — ConPTY/shell works under LocalSystem
