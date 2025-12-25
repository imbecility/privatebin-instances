@echo off
chcp 65001 >nul
cd /d "%~dp0"
setlocal EnableDelayedExpansion

if not exist "go.mod" (
    echo [ОШИБКА] файл go.mod не найден в текущей папке!
    pause
    exit /b 1
)

set "FULL_MOD_NAME="
for /f "tokens=2" %%a in ('findstr /R "^module" go.mod') do (
    set "FULL_MOD_NAME=%%a"
)

if "%FULL_MOD_NAME%"=="" (
    echo [ОШИБКА] не удалось найти строку "module" в go.mod
    echo сделан "go mod init ИМЯ/РЕПО"?
    pause
    exit /b 1
)

set "MOD_PATH_WIN=!FULL_MOD_NAME:/=\!"
for %%F in ("!MOD_PATH_WIN!") do set "APP_NAME=%%~nxF"

if "%APP_NAME%"=="" (
    echo [ОШИБКА] не удалось извлечь имя из "%FULL_MOD_NAME%"
    pause
    exit /b 1
)

echo [INFO] имя модуля: %FULL_MOD_NAME%
echo [INFO] имя приложения: %APP_NAME%
echo.

go version >nul 2>&1
if errorlevel 1 (
    echo [ОШИБКА] go не найден в PATH!
    echo установи: https://go.dev/doc/install
    pause
    exit /b 1
)

if exist "build" (
    rmdir /s /q "build"
)
mkdir "build\win"

call :Build "windows" "amd64" ".exe" "win"
if errorlevel 1 goto :Fail

mkdir "build\lin"
call :Build "linux" "amd64" "" "lin"
if errorlevel 1 goto :Fail

echo.
echo ==========================================
echo  ВСЕ СБОРКИ УСПЕШНО ЗАВЕРШЕНЫ
echo  путь: %~dp0build\
echo ==========================================
pause
explorer %~dp0build\
exit /b 0

:Build
set "TG_OS=%~1"
set "TG_ARCH=%~2"
set "TG_EXT=%~3"
set "TG_DIR=%~4"

echo ------------------------------------------
echo   СБОРКА [%TG_OS% / %TG_ARCH%]
echo ------------------------------------------

set "GOOS=%TG_OS%"
set "GOARCH=%TG_ARCH%"
set "CGO_ENABLED=0"

set "OUT_PATH=%~dp0\build\%TG_DIR%\%APP_NAME%%TG_EXT%"
cd /d "%~dp0\parser"
go build -o "%OUT_PATH%" -ldflags="-s -w -extldflags '-static'" -trimpath
if errorlevel 1 (
    echo [ОШИБКА] не удалось собрать для %TG_OS%/%TG_ARCH%
    exit /b 1
)
echo   ^> готово: %OUT_PATH%
exit /b 0

:Fail
echo.
echo [ОШИБКА] сборка прервана
pause
exit /b 1
