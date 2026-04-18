# windows-shell-test

Standalone WebSocket client for the agent's `/shell` endpoint. Built to
debug the Windows ConPTY output path without going browser → control plane
→ mesh → agent every iteration.

Works against any OS — the protocol is identical, the ConPTY-specific
bugs just happen to live on Windows.

## Protocol recap

- `GET /shell` upgrades to WebSocket, Bearer-auth'd by the agent token.
- First binary frame from the client: `[0x01, rows_hi, rows_lo, cols_hi, cols_lo]` (resize).
- All subsequent binary frames from the client: raw PTY input.
- All binary frames from the server: raw PTY output.

## Build

```
cd spike/windows-shell-test
GOOS=windows GOARCH=amd64 go build -trimpath -o shelltest.exe
GOOS=windows GOARCH=arm64 go build -trimpath -o shelltest-arm64.exe
go build -o shelltest            # native for local Unix testing
```

## Use

```
shelltest --token-file C:\Users\<user>\.config\hopssh\token \
          --addr localhost:41820 \
          --duration 4s \
          --input "echo HOP_TEST_OK\r\n"
```

Output: every WebSocket frame dumped with timestamp, length, ASCII
preview, hex. Summary line at the end.

## What it catches

- Zero frames after resize → agent didn't start child / ConPTY never fired.
- Only setup bytes (title, focus-mode, cursor) but no prompt → ConPTY
  captures some writes but child's stdout is still inherited from parent
  (the Windows bug we're chasing).
- Prompt present → ConPTY path is healthy; any display problem is frontend.
