cmake_minimum_required(VERSION 3.18)
project(filees_cfapi LANGUAGES C CXX)

# Windows only, on purpose: the Cloud Files API is the whole point of this
# helper (implementation notes (not distributed) §6 rules Linux out).
if(NOT WIN32)
  message(FATAL_ERROR "filees-cfapi builds on Windows only")
endif()

add_executable(filees-cfapi src/main.c src/common.c src/sync_root.c src/placeholders.c src/connect.c src/bridge.c src/shell_root.cpp)
set_target_properties(filees-cfapi PROPERTIES C_STANDARD 99 C_STANDARD_REQUIRED YES)
set_target_properties(filees-cfapi PROPERTIES CXX_STANDARD 17 CXX_STANDARD_REQUIRED YES)
# cfapi.h needs Windows 10 1709 (0x0A00 plus NTDDI); older targets compile the
# declarations away and fail at link with no useful message.
target_compile_definitions(filees-cfapi PRIVATE _WIN32_WINNT=0x0A00 NTDDI_VERSION=0x0A000004 UNICODE _UNICODE)
target_link_libraries(filees-cfapi PRIVATE CldApi RuntimeObject Advapi32 Shell32 Ole32 OleAut32 User32)
if(MSVC)
  target_compile_options(filees-cfapi PRIVATE /W4 /utf-8)
  target_compile_options(filees-cfapi PRIVATE $<$<COMPILE_LANGUAGE:CXX>:/EHsc>)
  # Static C runtime: the helper ships next to filees.exe, where the native
  # runtime's DLLs are not (they are unpacked into a cache), so it may depend
  # on system libraries only (packaging/windows/build-cfapi-helper.ps1 checks).
  set_property(TARGET filees-cfapi PROPERTY MSVC_RUNTIME_LIBRARY "MultiThreaded$<$<CONFIG:Debug>:Debug>")
endif()
