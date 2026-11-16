@echo off
setlocal
cd /d "%~dp0"

if not "%~1"=="" (
  filediver.exe %*
  echo.
  pause
  exit /b
)

echo Filediver CLI is a command-line tool. The help page is below.
echo Type a filediver command at the prompt to run it; close the prompt when done.
echo.
filediver.exe --help --no-pager
echo.
cmd /k
