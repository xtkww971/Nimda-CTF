@echo off
REM Runs benchmarks (functions named BenchmarkXxx) with memory allocation stats.
REM Usage: scripts\bench.bat
cd /d "%~dp0.."
go test ./... -run=^^$ -bench=. -benchmem