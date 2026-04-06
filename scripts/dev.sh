#!/usr/bin/env bash
# Development mode: run Go backend + SvelteKit dev server with hot reload.
# Ctrl+C cleanly kills both processes.
set -e

# Kill any leftover processes from previous runs.
pkill -f "hop-server" 2>/dev/null || true
sleep 0.5

echo "==> Building Go binaries..."
make build

echo "==> Starting backend + frontend dev servers..."

# Start backend.
./hop-server &
SERVER_PID=$!

# Start frontend dev server.
cd frontend && npm run dev -- --port 5173 &
FRONTEND_PID=$!
cd ..

echo ""
echo "  Backend:  http://localhost:9473"
echo "  Frontend: http://localhost:5173 (hot reload, proxies /api to :9473)"
echo "  Press Ctrl+C to stop."
echo ""

# Cleanup on exit.
cleanup() {
    echo ""
    echo "==> Stopping..."
    kill $SERVER_PID 2>/dev/null || true
    kill $FRONTEND_PID 2>/dev/null || true
    wait $SERVER_PID 2>/dev/null || true
    wait $FRONTEND_PID 2>/dev/null || true
    echo "==> Stopped."
}
trap cleanup EXIT INT TERM

# Wait for either to exit.
wait
