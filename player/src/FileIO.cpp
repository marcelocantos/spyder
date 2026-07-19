#include <player/FileIO.h>
#include <player/Resource.h>
#include <SDL3/SDL_iostream.h>
#include <spdlog/spdlog.h>
#include <streambuf>
#include <vector>

namespace spyder {
namespace {

// std::streambuf backed by an SDL_IOStream.
// Provides buffered reading + seeking so that std::istream works transparently
// over SDL's I/O abstraction (which handles Android APK assets, etc.).
class SdlStreambuf : public std::streambuf {
public:
    explicit SdlStreambuf(SDL_IOStream* io) : io_(io), buf_(kBufSize) {}

    ~SdlStreambuf() override {
        if (io_) SDL_CloseIO(io_);
    }

    SdlStreambuf(const SdlStreambuf&) = delete;
    SdlStreambuf& operator=(const SdlStreambuf&) = delete;

protected:
    int_type underflow() override {
        if (!io_) return traits_type::eof();
        size_t n = SDL_ReadIO(io_, buf_.data(), buf_.size());
        if (n == 0) return traits_type::eof();
        setg(reinterpret_cast<char*>(buf_.data()),
             reinterpret_cast<char*>(buf_.data()),
             reinterpret_cast<char*>(buf_.data() + n));
        return traits_type::to_int_type(*gptr());
    }

    pos_type seekoff(off_type off, std::ios_base::seekdir dir,
                     std::ios_base::openmode /*which*/) override {
        if (!io_) return pos_type(off_type(-1));

        // Adjust for unread buffered data when seeking relative to current position
        if (gptr() && gptr() < egptr() && dir == std::ios_base::cur) {
            off -= (egptr() - gptr());
        }

        SDL_IOWhence whence = SDL_IO_SEEK_SET;
        if (dir == std::ios_base::cur) whence = SDL_IO_SEEK_CUR;
        else if (dir == std::ios_base::end) whence = SDL_IO_SEEK_END;

        Sint64 result = SDL_SeekIO(io_, off, whence);
        if (result < 0) return pos_type(off_type(-1));

        // Invalidate read buffer after seek
        setg(nullptr, nullptr, nullptr);
        return pos_type(result);
    }

    pos_type seekpos(pos_type pos, std::ios_base::openmode which) override {
        return seekoff(off_type(pos), std::ios_base::beg, which);
    }

private:
    static constexpr size_t kBufSize = 8192;
    SDL_IOStream* io_;
    std::vector<uint8_t> buf_;
};

// std::istream subclass that owns its SdlStreambuf.
class SdlIStream : public std::istream {
public:
    explicit SdlIStream(SDL_IOStream* io)
        : std::istream(nullptr), buf_(io) {
        rdbuf(&buf_);
    }

private:
    SdlStreambuf buf_;
};

} // namespace

std::unique_ptr<std::istream> openFile(const std::string& path, bool binary) {
    auto resolved = spyder::resource(path);
    const char* mode = binary ? "rb" : "r";
    SDL_IOStream* io = SDL_IOFromFile(resolved.c_str(), mode);
    if (!io) {
        SPDLOG_ERROR("Failed to open file: {} ({})", path, SDL_GetError());
        auto stream = std::make_unique<std::istream>(nullptr);
        stream->setstate(std::ios::failbit);
        return stream;
    }

    return std::make_unique<SdlIStream>(io);
}

} // namespace spyder
