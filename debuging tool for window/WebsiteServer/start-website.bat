@echo off
title Nimda Container Runner
docker info >nul 2>&1
if %errorlevel% neq 0 (
    echo Docker is not on... Starting Docker Desktop...
    start "" "C:\Program Files\Docker\Docker\Docker Desktop.exe"
    timeout /t 20 /nobreak
)
cd /d "%~dp0"
docker-compose up -d
pause