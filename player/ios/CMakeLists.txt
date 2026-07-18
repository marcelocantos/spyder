# Spyder stream player — iOS (device + simulator).
# Self-contained: sources + vendor under player/; no GE_ROOT / libge.
#
# Three orientation packaging variants (🎯T154.3):
#   Player / PlayerLand / PlayerPort
#
#   cd player/ios && cmake -B build/xcode -G Xcode
#   cmake --build build/xcode --config Debug --target PlayerLand -- -sdk iphonesimulator
cmake_minimum_required(VERSION 3.28)
project(Player LANGUAGES C CXX OBJCXX)
set(CMAKE_CXX_STANDARD 23)
set(CMAKE_XCODE_LINK_BUILD_PHASE_MODE KNOWN_LOCATION)

set(PLAYER_ROOT ${CMAKE_CURRENT_SOURCE_DIR}/..)
if(NOT EXISTS "${PLAYER_ROOT}/player_core.cpp")
    message(FATAL_ERROR "PLAYER_ROOT=${PLAYER_ROOT} missing player_core.cpp")
endif()

set(LUNASVG_DIR ${PLAYER_ROOT}/vendor/lunasvg)
file(GLOB LUNASVG_SRC ${LUNASVG_DIR}/source/*.cpp)
file(GLOB PLUTOVG_SRC ${LUNASVG_DIR}/plutovg/source/*.c)

set(PLAYER_SOURCES
    ${CMAKE_CURRENT_SOURCE_DIR}/main.mm
    ${CMAKE_CURRENT_SOURCE_DIR}/KeychainStore.mm
    ${PLAYER_ROOT}/player_core.cpp
    ${PLAYER_ROOT}/src/player_orientation_ios.mm
    ${PLAYER_ROOT}/src/PlayerWireBridge.cpp
    ${PLAYER_ROOT}/src/WebSocketClient.cpp
    ${PLAYER_ROOT}/src/VideoDecoder_apple.mm
    ${PLAYER_ROOT}/src/PlayerRender.cpp
    ${PLAYER_ROOT}/src/CmdStream.cpp
    ${PLAYER_ROOT}/src/Signal.cpp
    ${PLAYER_ROOT}/src/Resource.cpp
    ${PLAYER_ROOT}/src/FileIO.cpp
    ${PLAYER_ROOT}/src/svg.cpp
    ${PLAYER_ROOT}/src/text.cpp
    ${PLAYER_ROOT}/src/FontLoader_apple.mm
    ${PLAYER_ROOT}/src/Immersive_apple.mm
    ${PLAYER_ROOT}/src/CutoutInsets_stub.cpp
    ${PLAYER_ROOT}/src/player_stubs.cpp
    ${PLAYER_ROOT}/src/player_sprite_stub.cpp
    ${PLAYER_ROOT}/vendor/src/sqlite3.c
    ${PLAYER_ROOT}/vendor/src/sqlpipe.cpp
    ${PLAYER_ROOT}/vendor/src/lz4.c
    ${PLAYER_ROOT}/vendor/deepparser/arena.c
    ${PLAYER_ROOT}/vendor/deepparser/liteparser.c
    ${PLAYER_ROOT}/vendor/deepparser/lp_tokenize.c
    ${PLAYER_ROOT}/vendor/deepparser/lp_unparse.c
    ${PLAYER_ROOT}/vendor/deepparser/parse.c
    ${LUNASVG_SRC}
    ${PLUTOVG_SRC}
)

set(SDL_DEVICE_DIR ${PLAYER_ROOT}/vendor/sdl3/lib/ios-arm64)
set(SDL_SIM_DIR ${PLAYER_ROOT}/vendor/sdl3/lib/ios-arm64-simulator)

function(add_player_variant target_name bundle_id info_plist xcassets_dir)
    add_executable(${target_name} MACOSX_BUNDLE ${PLAYER_SOURCES})

    target_compile_definitions(${target_name} PRIVATE
        ASIO_STANDALONE
        SPYDER_IOS
        SPYDER_PLAYER_NO_SOKOL
        SQLITE_ENABLE_SESSION
        SQLITE_ENABLE_PREUPDATE_HOOK
        SQLITE_ENABLE_DESERIALIZE
        LUNASVG_BUILD_STATIC
    )

    target_include_directories(${target_name} PRIVATE
        ${PLAYER_ROOT}/include
        ${PLAYER_ROOT}/src
        ${PLAYER_ROOT}
        ${PLAYER_ROOT}/vendor/include
        ${PLAYER_ROOT}/vendor/spdlog/include
        ${PLAYER_ROOT}/vendor/asio/include
        ${PLAYER_ROOT}/vendor/SDL/include
        ${PLAYER_ROOT}/vendor/sdl3/include
        ${PLAYER_ROOT}/vendor/freetype/include
        ${PLAYER_ROOT}/vendor/deepparser
        ${PLAYER_ROOT}/vendor/lunasvg/include
        ${PLAYER_ROOT}/vendor/lunasvg/plutovg/include
        ${CMAKE_CURRENT_SOURCE_DIR}
    )

    target_link_libraries(${target_name} PRIVATE
        ${SDL_DEVICE_DIR}/libSDL3.a
        ${SDL_DEVICE_DIR}/libSDL3_image.a
        ${SDL_DEVICE_DIR}/libSDL3_ttf.a
        ${SDL_DEVICE_DIR}/libfreetype.a
        ${SDL_DEVICE_DIR}/libharfbuzz.a
        ${SDL_DEVICE_DIR}/libplutosvg.a
        ${SDL_DEVICE_DIR}/libplutovg.a
    )

    target_link_libraries(${target_name} PRIVATE
        "-framework Foundation"
        "-framework UIKit"
        "-framework Metal"
        "-framework MetalKit"
        "-framework QuartzCore"
        "-framework CoreGraphics"
        "-framework CoreFoundation"
        "-framework CoreText"
        "-framework CoreMedia"
        "-framework CoreVideo"
        "-framework AudioToolbox"
        "-framework AVFoundation"
        "-framework GameController"
        "-framework CoreHaptics"
        "-framework CoreMotion"
        "-framework VideoToolbox"
        "-framework Security"
        "-framework ImageIO"
        "-framework OpenGLES"
        "-framework MobileCoreServices"
        "-framework CFNetwork"
        "-framework CoreBluetooth"
        "-lobjc"
    )

    # App icon catalog (distinct per variant — L / P badges on Land / Port).
    set(_assets "${CMAKE_CURRENT_SOURCE_DIR}/${xcassets_dir}")
    if(EXISTS "${_assets}")
        target_sources(${target_name} PRIVATE "${_assets}")
        set_source_files_properties("${_assets}" PROPERTIES
            MACOSX_PACKAGE_LOCATION Resources)
    endif()

    set_target_properties(${target_name} PROPERTIES
        MACOSX_BUNDLE_GUI_IDENTIFIER ${bundle_id}
        MACOSX_BUNDLE_BUNDLE_VERSION 1
        MACOSX_BUNDLE_SHORT_VERSION_STRING "1.0"
        MACOSX_BUNDLE_INFO_PLIST ${CMAKE_CURRENT_SOURCE_DIR}/${info_plist}
        XCODE_ATTRIBUTE_TARGETED_DEVICE_FAMILY "1,2"
        XCODE_ATTRIBUTE_SUPPORTED_PLATFORMS "iphoneos iphonesimulator"
        XCODE_ATTRIBUTE_PRODUCT_BUNDLE_IDENTIFIER ${bundle_id}
        XCODE_ATTRIBUTE_PRODUCT_NAME ${target_name}
        XCODE_ATTRIBUTE_ASSETCATALOG_COMPILER_APPICON_NAME AppIcon
        XCODE_ATTRIBUTE_DEVELOPMENT_TEAM "SWA3H3N7TW"
        XCODE_ATTRIBUTE_CODE_SIGN_STYLE "Automatic"
        XCODE_GENERATE_SCHEME TRUE
        XCODE_SCHEME_ENABLE_GPU_API_VALIDATION OFF
        XCODE_ATTRIBUTE_IPHONEOS_DEPLOYMENT_TARGET 16.3
        "XCODE_ATTRIBUTE_LIBRARY_SEARCH_PATHS[sdk=iphoneos*]" "${SDL_DEVICE_DIR}"
        "XCODE_ATTRIBUTE_LIBRARY_SEARCH_PATHS[sdk=iphonesimulator*]" "${SDL_SIM_DIR}"
    )
endfunction()

# Don't-care keeps the historic name "Player" / com.spyder.player.
add_player_variant(Player     com.spyder.player      Info.plist      Assets-Player.xcassets)
add_player_variant(PlayerLand com.spyder.player.land Info-Land.plist Assets-PlayerLand.xcassets)
add_player_variant(PlayerPort com.spyder.player.port Info-Port.plist Assets-PlayerPort.xcassets)
