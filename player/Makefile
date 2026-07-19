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

.PHONY: all player clean check-wire

all: player

player: $(PLAYER)

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
