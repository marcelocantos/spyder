# Spyder player — self-contained stream glass (desktop).
#
# Speaks the GE stream wire (H.264 / SP2I / SP2S / SP2T) via spyder's stream
# relay. No link against the ge engine; no GE_ROOT. Protocol compatibility is
# the only contract with ge game servers.
#
#   make -C player
#   make player   # from spyder repo root

PLAYER_ROOT := $(abspath $(CURDIR))
SPYDER_ROOT := $(abspath $(CURDIR)/..)
BUILD_DIR   := $(PLAYER_ROOT)/build

CXX    ?= clang++
CC     ?= clang
PLAYER := $(SPYDER_ROOT)/bin/player

INCLUDES = \
	-I$(PLAYER_ROOT)/include \
	-I$(PLAYER_ROOT)/src \
	-I$(PLAYER_ROOT) \
	-I$(PLAYER_ROOT)/vendor/include \
	-I$(PLAYER_ROOT)/vendor/spdlog/include \
	-I$(PLAYER_ROOT)/vendor/asio/include \
	-I$(PLAYER_ROOT)/vendor/SDL/include \
	-I$(PLAYER_ROOT)/vendor/sdl3/include \
	-I$(PLAYER_ROOT)/vendor/freetype/include \
	-I$(PLAYER_ROOT)/vendor/deepparser \
	-I$(PLAYER_ROOT)/vendor/lunasvg/include \
	-I$(PLAYER_ROOT)/vendor/lunasvg/plutovg/include

DEFS = \
	-DSQLITE_ENABLE_SESSION -DSQLITE_ENABLE_PREUPDATE_HOOK -DSQLITE_ENABLE_DESERIALIZE \
	-DLUNASVG_BUILD_STATIC \
	-DASIO_STANDALONE \
	-DSPYDER_DESKTOP \
	-DSPYDER_PLAYER_NO_SOKOL

CXXFLAGS ?= -std=c++20 -O2 -fobjc-arc
CFLAGS   ?= -std=c11 -O2

SDL_LIBS = \
	$(PLAYER_ROOT)/vendor/sdl3/lib/macos-arm64/libSDL3.a \
	$(PLAYER_ROOT)/vendor/sdl3/lib/macos-arm64/libSDL3_image.a \
	$(PLAYER_ROOT)/vendor/sdl3/lib/macos-arm64/libSDL3_ttf.a \
	$(PLAYER_ROOT)/vendor/sdl3/lib/macos-arm64/libfreetype.a \
	$(PLAYER_ROOT)/vendor/sdl3/lib/macos-arm64/libharfbuzz.a \
	$(PLAYER_ROOT)/vendor/sdl3/lib/macos-arm64/libplutosvg.a \
	$(PLAYER_ROOT)/vendor/sdl3/lib/macos-arm64/libplutovg.a

FRAMEWORKS = \
	-framework Metal -framework MetalKit -framework QuartzCore \
	-framework Cocoa -framework IOKit -framework CoreFoundation \
	-framework Carbon -framework CoreAudio -framework AudioToolbox \
	-framework CoreHaptics -framework GameController -framework CoreVideo \
	-framework ForceFeedback -framework AVFoundation -framework CoreMedia \
	-framework UniformTypeIdentifiers -framework CoreGraphics \
	-framework VideoToolbox -framework CoreMotion -framework StoreKit \
	-framework Security

CXX_SRC = \
	$(PLAYER_ROOT)/main.cpp \
	$(PLAYER_ROOT)/player_core.cpp \
	$(PLAYER_ROOT)/src/PlayerRender.cpp \
	$(PLAYER_ROOT)/src/PlayerWireBridge.cpp \
	$(PLAYER_ROOT)/src/WebSocketClient.cpp \
	$(PLAYER_ROOT)/src/CmdStream.cpp \
	$(PLAYER_ROOT)/src/Signal.cpp \
	$(PLAYER_ROOT)/src/Resource.cpp \
	$(PLAYER_ROOT)/src/FileIO.cpp \
	$(PLAYER_ROOT)/src/svg.cpp \
	$(PLAYER_ROOT)/src/text.cpp \
	$(PLAYER_ROOT)/src/Immersive_stub.cpp \
	$(PLAYER_ROOT)/src/CutoutInsets_stub.cpp \
	$(PLAYER_ROOT)/src/player_orientation_stub.cpp \
	$(PLAYER_ROOT)/src/player_stubs.cpp \
	$(PLAYER_ROOT)/vendor/src/sqlpipe.cpp \
	$(wildcard $(PLAYER_ROOT)/vendor/lunasvg/source/*.cpp)

MM_SRC = \
	$(PLAYER_ROOT)/src/VideoDecoder_apple.mm \
	$(PLAYER_ROOT)/src/FontLoader_apple.mm

C_SRC = \
	$(PLAYER_ROOT)/vendor/src/sqlite3.c \
	$(PLAYER_ROOT)/vendor/src/lz4.c \
	$(PLAYER_ROOT)/vendor/deepparser/arena.c \
	$(PLAYER_ROOT)/vendor/deepparser/liteparser.c \
	$(PLAYER_ROOT)/vendor/deepparser/lp_tokenize.c \
	$(PLAYER_ROOT)/vendor/deepparser/lp_unparse.c \
	$(PLAYER_ROOT)/vendor/deepparser/parse.c \
	$(wildcard $(PLAYER_ROOT)/vendor/lunasvg/plutovg/source/*.c)

CXX_OBJ = $(patsubst $(PLAYER_ROOT)/%.cpp,$(BUILD_DIR)/%.o,$(filter %.cpp,$(CXX_SRC)))
MM_OBJ  = $(patsubst $(PLAYER_ROOT)/%.mm,$(BUILD_DIR)/%.o,$(MM_SRC))
C_OBJ   = $(patsubst $(PLAYER_ROOT)/%.c,$(BUILD_DIR)/%.o,$(C_SRC))
OBJS    = $(CXX_OBJ) $(MM_OBJ) $(C_OBJ)

.PHONY: all player clean check-wire web check-wire-web

all: player

player: $(PLAYER)

# ── Browser player (🎯T101): same C++ tree compiled to wasm ──────────────
#
# Web-specific TUs swap in for the platform ones: browser WebSocket API
# replaces asio, no H.264 decoder (cmdstream-only v1), no system fonts.
# SDL3 + freetype are prebuilt for wasm under vendor/sdl3/lib/web-wasm/
# (built from the vendored sources via emcmake; see README).
WEB_DIR      := $(PLAYER_ROOT)/web
WEB_DIST     := $(WEB_DIR)/dist
WEB_BUILD    := $(BUILD_DIR)/web
EMCC         ?= em++
EMCC_C       ?= emcc

WEB_CXX_SRC = \
	$(PLAYER_ROOT)/main.cpp \
	$(PLAYER_ROOT)/player_core.cpp \
	$(PLAYER_ROOT)/src/PlayerRender.cpp \
	$(PLAYER_ROOT)/src/PlayerWireBridge.cpp \
	$(PLAYER_ROOT)/src/WebSocketClient_web.cpp \
	$(PLAYER_ROOT)/src/VideoDecoder_stub.cpp \
	$(PLAYER_ROOT)/src/FontLoader_web.cpp \
	$(PLAYER_ROOT)/src/CmdStream.cpp \
	$(PLAYER_ROOT)/src/Signal.cpp \
	$(PLAYER_ROOT)/src/Resource.cpp \
	$(PLAYER_ROOT)/src/FileIO.cpp \
	$(PLAYER_ROOT)/src/svg.cpp \
	$(PLAYER_ROOT)/src/text.cpp \
	$(PLAYER_ROOT)/src/Immersive_stub.cpp \
	$(PLAYER_ROOT)/src/CutoutInsets_stub.cpp \
	$(PLAYER_ROOT)/src/player_orientation_stub.cpp \
	$(PLAYER_ROOT)/src/player_stubs.cpp \
	$(PLAYER_ROOT)/vendor/src/sqlpipe.cpp \
	$(wildcard $(PLAYER_ROOT)/vendor/lunasvg/source/*.cpp)

WEB_DEFS = \
	-DSQLITE_ENABLE_SESSION -DSQLITE_ENABLE_PREUPDATE_HOOK -DSQLITE_ENABLE_DESERIALIZE \
	-DSQLITE_OMIT_LOAD_EXTENSION -DSQLITE_THREADSAFE=0 \
	-DLUNASVG_BUILD_STATIC \
	-DSPYDER_DESKTOP \
	-DSPYDER_PLAYER_NO_SOKOL

# Classic (JS) exception catching: the player relies on try/catch (sqlpipe,
# FontLoader) and ASYNCIFY does not compose with -fwasm-exceptions.
WEB_COMMON_FLAGS = -O2 -sDISABLE_EXCEPTION_CATCHING=0
WEB_CXXFLAGS = -std=c++20 $(WEB_COMMON_FLAGS)
# gnu11, not c11: strict C11 hides POSIX strcasecmp (deepparser) on emcc.
WEB_CFLAGS   = -std=gnu11 -O2

WEB_LINK_FLAGS = \
	-sASYNCIFY -sALLOW_MEMORY_GROWTH \
	-sMIN_WEBGL_VERSION=2 -sMAX_WEBGL_VERSION=2 \
	-sENVIRONMENT=web -sEXIT_RUNTIME=1 \
	-sSTACK_SIZE=1048576 \
	-lwebsocket.js \
	-sDISABLE_EXCEPTION_CATCHING=0 \
	-sEXCEPTION_STACK_TRACES=1

WEB_SDL_LIBS = \
	$(PLAYER_ROOT)/vendor/sdl3/lib/web-wasm/libSDL3_image.a \
	$(PLAYER_ROOT)/vendor/sdl3/lib/web-wasm/libSDL3.a \
	$(PLAYER_ROOT)/vendor/sdl3/lib/web-wasm/libfreetype.a

WEB_CXX_OBJ = $(patsubst $(PLAYER_ROOT)/%.cpp,$(WEB_BUILD)/%.o,$(WEB_CXX_SRC))
WEB_C_OBJ   = $(patsubst $(PLAYER_ROOT)/%.c,$(WEB_BUILD)/%.o,$(C_SRC))
WEB_OBJS    = $(WEB_CXX_OBJ) $(WEB_C_OBJ)

web: $(WEB_DIST)/player.js

$(WEB_DIST)/player.js: $(WEB_OBJS) $(WEB_DIR)/index.html
	@mkdir -p $(WEB_DIST)
	$(EMCC) $(WEB_CXXFLAGS) $(WEB_OBJS) $(WEB_SDL_LIBS) $(WEB_LINK_FLAGS) \
	    -o $@
	cp $(WEB_DIR)/index.html $(WEB_DIST)/index.html
	@echo "built $(WEB_DIST)/{player.js,player.wasm,index.html} (browser glass)"

$(WEB_BUILD)/%.o: $(PLAYER_ROOT)/%.cpp
	@mkdir -p $(dir $@)
	$(EMCC) $(WEB_CXXFLAGS) $(INCLUDES) $(WEB_DEFS) -c $< -o $@

$(WEB_BUILD)/%.o: $(PLAYER_ROOT)/%.c
	@mkdir -p $(dir $@)
	$(EMCC_C) $(WEB_CFLAGS) $(INCLUDES) $(WEB_DEFS) -c $< -o $@

# Wire layout oracle under wasm (🎯T101.1): same struct sizes as native.
check-wire-web: tools/check-wire-layout.cpp include/wire/Protocol.h
	@mkdir -p $(WEB_BUILD)
	$(EMCC) -std=c++20 -O1 $(INCLUDES) $(WEB_DEFS) -sENVIRONMENT=node \
	    tools/check-wire-layout.cpp -o $(WEB_BUILD)/check-wire-layout.js
	node $(WEB_BUILD)/check-wire-layout.js

# Wire layout oracle (magics + struct sizes). No ge checkout required.
check-wire: tools/check-wire-layout.cpp include/wire/Protocol.h
	$(CXX) $(CXXFLAGS) $(INCLUDES) $(DEFS) tools/check-wire-layout.cpp -o $(BUILD_DIR)/check-wire-layout
	$(BUILD_DIR)/check-wire-layout

$(PLAYER): $(OBJS)
	@mkdir -p $(dir $@)
	@test -f $(PLAYER_ROOT)/vendor/sdl3/lib/macos-arm64/libSDL3.a || { \
	  echo "error: missing vendored SDL under player/vendor/sdl3"; exit 1; }
	$(CXX) $(CXXFLAGS) $^ $(SDL_LIBS) $(FRAMEWORKS) -o $@
	@echo "built $@ (spyder player — protocol-only to ge servers)"

$(BUILD_DIR)/%.o: $(PLAYER_ROOT)/%.cpp
	@mkdir -p $(dir $@)
	$(CXX) $(CXXFLAGS) $(INCLUDES) $(DEFS) -c $< -o $@

$(BUILD_DIR)/%.o: $(PLAYER_ROOT)/%.mm
	@mkdir -p $(dir $@)
	$(CXX) $(CXXFLAGS) $(INCLUDES) $(DEFS) -c $< -o $@

$(BUILD_DIR)/%.o: $(PLAYER_ROOT)/%.c
	@mkdir -p $(dir $@)
	$(CC) $(CFLAGS) $(INCLUDES) $(DEFS) -c $< -o $@

clean:
	rm -rf $(BUILD_DIR) $(PLAYER)
