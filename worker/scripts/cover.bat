@echo off
REM Generates an HTML coverage report and opens it in the default browser.
REM Usage: scripts\cover.bat
cd /d "%~dp0.."
go test ./... -coverprofile=coverage.out
if errorlevel 1 exit /b %errorlevel%
go tool cover -html=coverage.out -o coverage.html
start coverage.html