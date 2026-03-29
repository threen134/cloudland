#!/bin/bash

# 启动 VitePress 文档站 (port 5174)
echo "Starting VitePress docs on :5174..."
(cd docs && npm run docs:dev -- --port 5174) &
DOCS_PID=$!

# 启动 Web UI (port 5173)
echo "Starting Web UI on :5173..."
(cd web && npm run dev) &
WEB_PID=$!

echo ""
echo "  Web UI  → http://localhost:5173"
echo "  Docs    → http://localhost:5173/docs"
echo ""
echo "Press Ctrl+C to stop all services"

trap "kill $DOCS_PID $WEB_PID 2>/dev/null; exit" INT TERM
wait
