// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// Android impl: JNI into the activity to read display-cutout insets.
// Activity exposes `int[] getDisplayCutoutInsets()` returning
// {left, right, top, bottom} populated from
// WindowInsets.Type.displayCutout() in an OnApplyWindowInsetsListener.
// If the activity doesn't define the method (apps that customize their
// activity helper), we return zeros.

#include "CutoutInsets.h"

#include <SDL3/SDL_system.h>
#include <jni.h>
#include <spdlog/spdlog.h>

namespace spyder {

SafeAreaInsets queryDisplayCutoutInsets() {
    SafeAreaInsets out{};
    JNIEnv* env = static_cast<JNIEnv*>(SDL_GetAndroidJNIEnv());
    if (!env) return out;
    jobject activity = static_cast<jobject>(SDL_GetAndroidActivity());
    if (!activity) return out;
    jclass cls = env->GetObjectClass(activity);
    jmethodID m = env->GetMethodID(cls, "getDisplayCutoutInsets", "()[I");
    if (!m) {
        env->ExceptionClear();
        env->DeleteLocalRef(cls);
        env->DeleteLocalRef(activity);
        return out;
    }
    jintArray arr = static_cast<jintArray>(env->CallObjectMethod(activity, m));
    if (env->ExceptionCheck()) {
        env->ExceptionDescribe();
        env->ExceptionClear();
        env->DeleteLocalRef(cls);
        env->DeleteLocalRef(activity);
        return out;
    }
    if (arr && env->GetArrayLength(arr) == 4) {
        jint vals[4] = {0, 0, 0, 0};
        env->GetIntArrayRegion(arr, 0, 4, vals);
        // JNI returns left/right/top/bottom; screen is y-down (SDL/sokol),
        // so top → y0 (smaller-y edge), bottom → y1 (larger-y edge).
        out.x0 = float(vals[0]);   // left
        out.x1 = float(vals[1]);   // right
        out.y0 = float(vals[2]);   // top in y-down
        out.y1 = float(vals[3]);   // bottom
    }
    if (arr) env->DeleteLocalRef(arr);
    env->DeleteLocalRef(cls);
    env->DeleteLocalRef(activity);
    return out;
}

} // namespace spyder
