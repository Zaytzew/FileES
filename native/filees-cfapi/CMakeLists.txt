cmake_minimum_required(VERSION 3.18)
project(filees_cfapi LANGUAGES C)

# Windows only, on purpose: the Cloud Files API is the whole point of this
# helper (implementation notes (not distributed) §6 rules Linux out).
if(NOT WIN32)
  message(FATAL_ERROR "filees-cfapi builds on Windows only")
endif()

add_executable(filees-cfapi src/main.c src/common.c src/sync_root.c src/placeholders.c src/connect.c)
set_target_properties(filees-cfapi PROPERTIES C_STANDARD 99 C_STANDARD_REQUIRED YES)
# cfapi.h needs Windows 10 1709 (0x0A00 plus NTDDI); older targets compile the
# declarations away and fail at link with no useful message.
target_compile_definitions(filees-cfapi PRIVATE _WIN32_WINNT=0x0A00 NTDDI_VERSION=0x0A000004 UNICODE _UNICODE)
target_link_libraries(filees-cfapi PRIVATE CldApi)
if(MSVC)
  target_compile_options(filees-cfapi PRIVATE /W4 /utf-8)
endif()
