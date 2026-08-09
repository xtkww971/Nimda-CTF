@echo off
REM Starts an isolated Redis container for local worker dev/testing on port 6380.
REM Usage: scripts\redis-up.bat
cd /d "%~dp0.."
docker compose -f docker-compose.test.yml up -d
echo Redis ready at localhost:6380 - set REDIS_ADDR=localhost:6380 (see .env.example)