@echo off
REM Runs go vet always, and golangci-lint if it's installed (see .golangci.yml).
REM Usage: scripts\lint.bat
cd /d "%~dp0.."

echo Running go vet...
go vet ./...
if errorlevel 1 exit /b %errorlevel%

where golangci-lint >nul 2>nul
if errorlevel 1 (
    echo golangci-lint not found - skipping. Install with:
    echo   go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
    exit /b 0
)

echo Running golangci-lint...
golangci-lint run ./...