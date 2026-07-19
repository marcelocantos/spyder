#pragma once

#include <player/Linalg.h>
#include <string>

namespace spyder {

// Resolve a relative path to the app's resource directory.
// On iOS/Android, prepends the app bundle path (SDL_GetBasePath()).
// On desktop, prepends the binary's parent directory (project root).
std::string resource(const std::string& relativePath);

// Path component identifying the renderer-appropriate shader directory.
// Games construct shader paths as resource(shaderDir() + "/foo_vs.bin").
//   * Apple platforms (Metal)             → "build/shaders"
//   * Android (Vulkan, on emulator)       → "build/shaders-spirv"
//   * Android (OpenGL ES, real devices)   → "build/shaders-gles"
// Must be called AFTER the render backend is initialized (SokolContext constructed).
std::string shaderDir();

// Same as shaderDir() but for internal compose-pass shaders, which
// live under "build/spyder/shaders" (Apple) or "build/spyder/shaders-{spirv,gles}"
// (Android).
std::string renderShaderDir();

} // namespace spyder
