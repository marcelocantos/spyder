bin.install "bin/spyder" => "spyder"
bin.install "bin/spyder-killusbmuxd" => "spyder-killusbmuxd"
(libexec/"spyder").install "libexec/spyder/ios"
# Stream player artifacts (🎯T102): desktop glass always ships; the Android
# debug APK is best-effort (player/android's native build is not yet
# ported to sokol — see 🎯T34) and simply absent from the tarball when
# release-package.sh couldn't produce one. iOS is deliberately excluded —
# see formula_includes.rb caveats.
(libexec/"spyder/player/desktop").install "libexec/spyder/player/desktop/player" if
  File.exist?("libexec/spyder/player/desktop/player")
(libexec/"spyder/player/android").install "libexec/spyder/player/android/player.apk" if
  File.exist?("libexec/spyder/player/android/player.apk")
