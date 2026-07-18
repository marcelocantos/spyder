#pragma once

#include <player/Linalg.h>

#include <istream>
#include <memory>
#include <string>

namespace spyder {

// Opens a file for reading. Uses SDL_IOFromFile internally, which transparently
// handles Android APK assets, iOS bundle paths, and normal filesystem paths.
// Returns a std::istream wrapping the SDL_IOStream.
std::unique_ptr<std::istream> openFile(const std::string& path, bool binary = false);

} // namespace spyder
