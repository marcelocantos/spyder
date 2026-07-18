// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// Android impl of spyder::applyImmersive — JNI into the SDL activity to
// hide system bars (status + navigation/gesture). The activity must
// expose `public void applyImmersive(boolean)` (the spyder Android
// template's default Activity does); otherwise the call is a no-op.

#include "Immersive.h"

#include <SDL3/SDL_system.h>
#include <jni.h>
#include <spdlog/spdlog.h>

namespace spyder {

void applyImmersive(bool enabled) {
    JNIEnv* env = static_cast<JNIEnv*>(SDL_GetAndroidJNIEnv());
    if (!env) {
        SPDLOG_WARN("applyImmersive: SDL_GetAndroidJNIEnv returned null");
        return;
    }
    jobject activity = static_cast<jobject>(SDL_GetAndroidActivity());
    if (!activity) {
        SPDLOG_WARN("applyImmersive: SDL_GetAndroidActivity returned null");
        return;
    }
    jclass cls = env->GetObjectClass(activity);
    jmethodID m = env->GetMethodID(cls, "applyImmersive", "(Z)V");
    if (!m) {
        // Activity didn't override applyImmersive — silent no-op for
        // apps that don't include the helper.
        env->ExceptionClear();
        env->DeleteLocalRef(cls);
        env->DeleteLocalRef(activity);
        return;
    }
    env->CallVoidMethod(activity, m, enabled ? JNI_TRUE : JNI_FALSE);
    if (env->ExceptionCheck()) {
        env->ExceptionDescribe();
        env->ExceptionClear();
    }
    env->DeleteLocalRef(cls);
    env->DeleteLocalRef(activity);
}

} // namespace spyder
