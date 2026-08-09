@echo off
REM Runs all unit tests with coverage. Skips -race by default: it requires
REM cgo (a C compiler), which most Windows dev machines don't have set up.
REM CI (.github/workflows/worker-ci.yml, runs on Linux) always runs -race.
REM If you have a C toolchain installed (e.g. MSYS2 mingw-w64) locally, run:
REM   go test ./... -race -cover
REM Usage: scripts\test.bat
cd /d "%~dp0.."
go test ./... -cover