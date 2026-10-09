cmake_minimum_required(VERSION 3.18)
project(filees_cfapi LANGUAGES C CXX)

# Windows only, on purpose: the Cloud Files API is the whole point of this
# helper (implementation notes (not distributed) §6 rules Linux out).
if(NOT WIN32)
  message(FATAL_ERROR "filees-cfapi builds on Windows only")
endif()

set(FILEES_CFAPI_SOURCES src/main.c src/common.c src/sync_root.c src/placeholders.c src/connect.c src/bridge.c src/file_state.c src/shell_root.cpp)
add_executable(filees-cfapi ${FILEES_CFAPI_SOURCES})
# Disposable callback characterization, NEVER the shipped provider. Even an
# opted-in build keeps filees-cfapi.exe unchanged; only this extra executable
# can allow a remote deletion without the product's durable coordinator.
option(FILEES_CFAPI_BUILD_DELETE_PROBE "Build isolated deletion callback probe (not packaged)" OFF)
set(FILEES_CFAPI_TARGETS filees-cfapi)
if(FILEES_CFAPI_BUILD_DELETE_PROBE)
  add_executable(filees-cfapi-delete-probe ${FILEES_CFAPI_SOURCES})
  target_compile_definitions(filees-cfapi-delete-probe PRIVATE FILEES_CFAPI_DELETE_PROBE=1)
  list(APPEND FILEES_CFAPI_TARGETS filees-cfapi-delete-probe)
endif()
# cfapi.h needs Windows 10 1709 (0x0A00 plus NTDDI); older targets compile the
# declarations away and fail at link with no useful message.
# Version metadata comes from the same source release as the daemon.
set_property(DIRECTORY APPEND PROPERTY CMAKE_CONFIGURE_DEPENDS "${CMAKE_CURRENT_SOURCE_DIR}/../../VERSION")
file(STRINGS "${CMAKE_CURRENT_SOURCE_DIR}/../../VERSION" FILEES_VERSION LIMIT_COUNT 1)
string(STRIP "${FILEES_VERSION}" FILEES_VERSION)
if(NOT FILEES_VERSION MATCHES "^[0-9]+\\.[0-9]+\\.[0-9]+$")
  message(FATAL_ERROR "VERSION must be MAJOR.MINOR.PATCH, got '${FILEES_VERSION}'")
endif()
foreach(FILEES_CFAPI_TARGET IN LISTS FILEES_CFAPI_TARGETS)
set_target_properties(${FILEES_CFAPI_TARGET} PROPERTIES C_STANDARD 99 C_STANDARD_REQUIRED YES CXX_STANDARD 17 CXX_STANDARD_REQUIRED YES)
target_compile_definitions(${FILEES_CFAPI_TARGET} PRIVATE _WIN32_WINNT=0x0A00 NTDDI_VERSION=0x0A000004 UNICODE _UNICODE FILEES_CFAPI_VERSION="${FILEES_VERSION}")
target_link_libraries(${FILEES_CFAPI_TARGET} PRIVATE CldApi RuntimeObject Advapi32 Shell32 Ole32 OleAut32 User32)
if(MSVC)
  target_compile_options(${FILEES_CFAPI_TARGET} PRIVATE /W4 /utf-8)
  target_compile_options(${FILEES_CFAPI_TARGET} PRIVATE $<$<COMPILE_LANGUAGE:CXX>:/EHsc>)
  # Static C runtime: the helper ships next to filees.exe, where the native
  # runtime's DLLs are not (they are unpacked into a cache), so it may depend
  # on system libraries only (packaging/windows/build-cfapi-helper.ps1 checks).
  set_property(TARGET ${FILEES_CFAPI_TARGET} PROPERTY MSVC_RUNTIME_LIBRARY "MultiThreaded$<$<CONFIG:Debug>:Debug>")
endif()
endforeach()
