cmake_minimum_required(VERSION 3.22)
project(SpyderPlayer LANGUAGES C CXX)
set(CMAKE_CXX_STANDARD 23)

# Self-contained player tree (no GE_ROOT / no libge).
# cpp → main → src → app → android → player  (5 levels)
get_filename_component(PLAYER_ROOT "${CMAKE_CURRENT_LIST_DIR}/../../../../.." REALPATH)
message(STATUS "PLAYER_ROOT=${PLAYER_ROOT}")
if(NOT EXISTS "${PLAYER_ROOT}/player_core.cpp")
    message(FATAL_ERROR "PLAYER_ROOT=${PLAYER_ROOT} missing player_core.cpp")
endif()

# ── 16 KB page alignment (🎯T75) ────────────────────────────────────
string(APPEND CMAKE_SHARED_LINKER_FLAGS " -Wl,-z,max-page-size=16384")

# SDL3 — must be vendored under player/vendor (vendored in-tree).
set(SDL_SRC "${PLAYER_ROOT}/vendor/github.com/libsdl-org/SDL")
if(NOT EXISTS "${SDL_SRC}/CMakeLists.txt")
    message(FATAL_ERROR
        "Missing ${SDL_SRC}. Vendor SDL3 under player/vendor for Android builds "
        "(self-contained).")
endif()
add_subdirectory(${SDL_SRC} ${CMAKE_CURRENT_BINARY_DIR}/SDL3)

set(SDL_IMAGE_SRC "${PLAYER_ROOT}/vendor/github.com/libsdl-org/SDL_image")
if(EXISTS "${SDL_IMAGE_SRC}/CMakeLists.txt")
    set(SDL3IMAGE_VENDORED OFF CACHE BOOL "" FORCE)
    add_subdirectory(${SDL_IMAGE_SRC} ${CMAKE_CURRENT_BINARY_DIR}/SDL3_image)
endif()

set(SDL_TTF_SRC "${PLAYER_ROOT}/vendor/github.com/libsdl-org/SDL_ttf")
if(EXISTS "${SDL_TTF_SRC}/CMakeLists.txt")
    set(SDLTTF_VENDORED ON CACHE BOOL "" FORCE)
    add_subdirectory(${SDL_TTF_SRC} ${CMAKE_CURRENT_BINARY_DIR}/SDL3_ttf)
endif()

set(FFMPEG_DIR ${PLAYER_ROOT}/vendor/ffmpeg)
set(LUNASVG_DIR ${PLAYER_ROOT}/vendor/lunasvg)
file(GLOB LUNASVG_SRC ${LUNASVG_DIR}/source/*.cpp)
file(GLOB PLUTOVG_SRC ${LUNASVG_DIR}/plutovg/source/*.c)

# Prefer GLOB so a missing path fails clearly with an empty list.
set(DEEPPARSER_DIR "${PLAYER_ROOT}/vendor/deepparser")
file(GLOB DEEPPARSER_SRC
    "${DEEPPARSER_DIR}/arena.c"
    "${DEEPPARSER_DIR}/liteparser.c"
    "${DEEPPARSER_DIR}/lp_tokenize.c"
    "${DEEPPARSER_DIR}/lp_unparse.c"
    "${DEEPPARSER_DIR}/parse.c"
)
list(LENGTH DEEPPARSER_SRC _dp_n)
message(STATUS "deepparser (${_dp_n}): ${DEEPPARSER_SRC}")
if(_dp_n LESS 5)
    message(FATAL_ERROR
        "expected 5 deepparser .c files under ${DEEPPARSER_DIR}, got ${_dp_n}. "
        "Contents: "
    )
    file(GLOB _all "${DEEPPARSER_DIR}/*")
    message(FATAL_ERROR "dir listing: ${_all}")
endif()

add_library(main SHARED
    ${CMAKE_CURRENT_SOURCE_DIR}/main.cpp
    ${PLAYER_ROOT}/player_core.cpp
    ${PLAYER_ROOT}/src/player_orientation_stub.cpp
    ${PLAYER_ROOT}/src/PlayerWireBridge.cpp
    ${PLAYER_ROOT}/src/WebSocketClient.cpp
    ${PLAYER_ROOT}/src/PlayerRender.cpp
    ${PLAYER_ROOT}/src/CmdStream.cpp
    ${PLAYER_ROOT}/src/Signal.cpp
    ${PLAYER_ROOT}/src/Resource.cpp
    ${PLAYER_ROOT}/src/FileIO.cpp
    ${PLAYER_ROOT}/src/svg.cpp
    ${PLAYER_ROOT}/src/text.cpp
    ${PLAYER_ROOT}/src/FontLoader_android.cpp
    ${PLAYER_ROOT}/src/Immersive_android.cpp
    ${PLAYER_ROOT}/src/CutoutInsets_android.cpp
    ${PLAYER_ROOT}/src/player_stubs.cpp
    ${PLAYER_ROOT}/src/player_sprite_stub.cpp
    ${PLAYER_ROOT}/src/VideoDecoder_ffmpeg.cpp
    ${PLAYER_ROOT}/vendor/src/sqlite3.c
    ${PLAYER_ROOT}/vendor/src/sqlpipe.cpp
    ${PLAYER_ROOT}/vendor/src/lz4.c
    ${DEEPPARSER_SRC}
    ${LUNASVG_SRC}
    ${PLUTOVG_SRC}
)

target_compile_definitions(main PRIVATE
    SPYDER_ANDROID
    SPYDER_PLAYER_NO_SOKOL
    ASIO_STANDALONE
    SQLITE_ENABLE_SESSION
    SQLITE_ENABLE_PREUPDATE_HOOK
    SQLITE_ENABLE_DESERIALIZE
    LUNASVG_BUILD_STATIC
)

target_include_directories(main PRIVATE
    ${PLAYER_ROOT}/include
    ${PLAYER_ROOT}/src
    ${PLAYER_ROOT}
    ${PLAYER_ROOT}/vendor/include
    ${PLAYER_ROOT}/vendor/spdlog/include
    ${PLAYER_ROOT}/vendor/asio/include
    ${PLAYER_ROOT}/vendor/deepparser
    ${PLAYER_ROOT}/vendor/lunasvg/include
    ${PLAYER_ROOT}/vendor/lunasvg/plutovg/include
    ${PLAYER_ROOT}/vendor/freetype/include
    ${FFMPEG_DIR}/include
    # Full tree so #include <libavcodec/avcodec.h> resolves (same as spyder player path).
    ${PLAYER_ROOT}/vendor/github.com/FFmpeg/FFmpeg
)

target_link_libraries(main PRIVATE
    SDL3::SDL3
    android
    log
    z
)
if(TARGET SDL3_image::SDL3_image)
    target_link_libraries(main PRIVATE SDL3_image::SDL3_image)
endif()
if(TARGET SDL3_ttf::SDL3_ttf)
    target_link_libraries(main PRIVATE SDL3_ttf::SDL3_ttf freetype)
endif()
if(EXISTS "${FFMPEG_DIR}/lib/android-arm64/libavcodec.a")
    target_link_libraries(main PRIVATE
        ${FFMPEG_DIR}/lib/android-arm64/libavcodec.a
        ${FFMPEG_DIR}/lib/android-arm64/libavutil.a)
else()
    message(FATAL_ERROR "missing FFmpeg android-arm64 under ${FFMPEG_DIR}")
endif()
