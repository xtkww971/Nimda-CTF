@echo off
REM Stops and removes the local test Redis container started by redis-up.bat.
REM Usage: scripts\redis-down.bat
cd /d "%~dp0.."
docker compose -f docker-compose.test.yml down