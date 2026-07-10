@echo off
title Nimda Container Stopper
cd /d "%~dp0"
docker-compose down -v
pause