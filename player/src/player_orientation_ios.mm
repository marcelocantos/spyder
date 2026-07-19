// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// iOS orientation force + lock (🎯T36 / iPadOS 26 letterbox fix).
//
// Two distinct knobs (must not collapse into one flag):
//
//   A. Supported mask — `supportedInterfaceOrientations` narrowed to the
//      requested class (landscape / portrait / …). This is what allows
//      UIKit to *rotate into* that class when the chassis is the other way.
//
//   B. Freeze lock — `prefersInterfaceOrientationLocked` (TN3192, iPadOS 26+)
//      returns YES only *after* the interface has adopted the requested
//      class. Freezing too early freezes *portrait* and blocks geometry
//      updates — the iPadOS 26 letterbox failure mode (M5 @ 26.4 vs M4 @ 18.6).
//
// Order of operations in playerForceOrientation:
//   1. Set supported mask, freeze = NO
//   2. setNeedsUpdate* + requestGeometryUpdate (rotate while unlocked)
//   3. After settle (interface matches / timeout), freeze = YES
//
// Critical: SDL's `SDL_uikitviewcontroller` *overrides*
// `supportedInterfaceOrientations` and does not call super. Swizzle
// UIViewController *and* SDL's class (by name).
//
// Also arm BEFORE SDL_CreateWindow when possible (runDirectHosted calls
// playerForceOrientation prior to DirectRenderHost) so CreateWindow measures
// a landscape glass, not a transient portrait one.

#include "player_orientation.h"

#import <UIKit/UIKit.h>
#import <objc/runtime.h>
#include <SDL3/SDL_video.h>
#include <spdlog/spdlog.h>

// Sentinel matching spyder::wire::kOrientationAnyLandscape (Protocol.h).
static constexpr uint8_t kLockAnyLandscape = 0xFE;

// Supported-class request (0 = no engine override). Drives supportedInterfaceOrientations.
static uint8_t g_supportedOrientation = 0;
// TN3192 freeze. Only true after we believe the interface adopted the mask.
static bool g_freezeOrientation = false;
static bool g_deviceOrientationActive = false;

static UIInterfaceOrientationMask spyderLockMask(uint8_t lock) {
    switch (lock) {
        case SDL_ORIENTATION_PORTRAIT:          return UIInterfaceOrientationMaskPortrait;
        case SDL_ORIENTATION_PORTRAIT_FLIPPED:  return UIInterfaceOrientationMaskPortraitUpsideDown;
        case SDL_ORIENTATION_LANDSCAPE:         return UIInterfaceOrientationMaskLandscapeRight;
        case SDL_ORIENTATION_LANDSCAPE_FLIPPED: return UIInterfaceOrientationMaskLandscapeLeft;
        case kLockAnyLandscape:                 return UIInterfaceOrientationMaskLandscape;
        default:                                return UIInterfaceOrientationMaskAll;
    }
}

static bool spyderInterfaceMatchesMask(UIInterfaceOrientationMask mask) {
    UIInterfaceOrientation io = UIInterfaceOrientationUnknown;
    for (UIScene *s in UIApplication.sharedApplication.connectedScenes) {
        if (![s isKindOfClass:[UIWindowScene class]]) continue;
        UIWindowScene *scene = (UIWindowScene *)s;
        io = scene.effectiveGeometry.interfaceOrientation;
        if (io != UIInterfaceOrientationUnknown) break;
    }
    if (io == UIInterfaceOrientationUnknown) {
        // Fallback (deprecated path) if no scene yet.
#pragma clang diagnostic push
#pragma clang diagnostic ignored "-Wdeprecated-declarations"
        io = UIApplication.sharedApplication.statusBarOrientation;
#pragma clang diagnostic pop
    }
    switch (io) {
    case UIInterfaceOrientationPortrait:
        return (mask & UIInterfaceOrientationMaskPortrait) != 0;
    case UIInterfaceOrientationPortraitUpsideDown:
        return (mask & UIInterfaceOrientationMaskPortraitUpsideDown) != 0;
    case UIInterfaceOrientationLandscapeLeft:
        return (mask & UIInterfaceOrientationMaskLandscapeLeft) != 0;
    case UIInterfaceOrientationLandscapeRight:
        return (mask & UIInterfaceOrientationMaskLandscapeRight) != 0;
    default:
        return false;
    }
}

static void spyderSwizzleOrientationMethods(Class cls) {
    if (!cls) return;

    static char kSwizzledKey;
    if (objc_getAssociatedObject((id)cls, &kSwizzledKey)) return;

    {
        SEL sel = @selector(prefersInterfaceOrientationLocked);
        Method orig = class_getInstanceMethod(cls, sel);
        const char *types = orig ? method_getTypeEncoding(orig) : "B@:";
        IMP blockImp = imp_implementationWithBlock(^BOOL(id self) {
            (void)self;
            // Freeze only after rotate — not merely because a mask is set.
            return g_freezeOrientation ? YES : NO;
        });
        if (orig) {
            class_replaceMethod(cls, sel, blockImp, types);
        } else {
            class_addMethod(cls, sel, blockImp, types);
        }
    }

    {
        SEL sel = @selector(supportedInterfaceOrientations);
        Method orig = class_getInstanceMethod(cls, sel);
        if (!orig) {
            class_addMethod(
                cls, sel,
                imp_implementationWithBlock(^UIInterfaceOrientationMask(id self) {
                    (void)self;
                    if (g_supportedOrientation != 0)
                        return spyderLockMask(g_supportedOrientation);
                    return UIInterfaceOrientationMaskAll;
                }),
                "I@:");
        } else {
            IMP originalImp = method_getImplementation(orig);
            IMP blockImp = imp_implementationWithBlock(
                ^UIInterfaceOrientationMask(id self) {
                    if (g_supportedOrientation != 0)
                        return spyderLockMask(g_supportedOrientation);
                    using OrigFn = UIInterfaceOrientationMask (*)(id, SEL);
                    return ((OrigFn)originalImp)(self, sel);
                });
            class_replaceMethod(cls, sel, blockImp, method_getTypeEncoding(orig));
        }
    }

    objc_setAssociatedObject((id)cls, &kSwizzledKey, @YES,
                             OBJC_ASSOCIATION_RETAIN_NONATOMIC);
    SPDLOG_INFO("playerForceOrientation: swizzled orientation hooks on {}",
                class_getName(cls));
}

static void geEnsureOrientationSwizzles() {
    spyderSwizzleOrientationMethods([UIViewController class]);
    spyderSwizzleOrientationMethods(NSClassFromString(@"SDL_uikitviewcontroller"));
}

@interface UIViewController (GeOrientationLock)
@end

@implementation UIViewController (GeOrientationLock)

+ (void)load {
    geEnsureOrientationSwizzles();
}

@end

int playerGetPhysicalOrientation() {
    if (!g_deviceOrientationActive) {
        [[UIDevice currentDevice] beginGeneratingDeviceOrientationNotifications];
        g_deviceOrientationActive = true;
    }
    UIDeviceOrientation dev = [UIDevice currentDevice].orientation;
    switch (dev) {
    case UIDeviceOrientationPortrait:           return SDL_ORIENTATION_PORTRAIT;
    case UIDeviceOrientationPortraitUpsideDown: return SDL_ORIENTATION_PORTRAIT_FLIPPED;
    case UIDeviceOrientationLandscapeLeft:      return SDL_ORIENTATION_LANDSCAPE;
    case UIDeviceOrientationLandscapeRight:     return SDL_ORIENTATION_LANDSCAPE_FLIPPED;
    default:                                    return SDL_ORIENTATION_PORTRAIT;
    }
}

static const char* spyderLockName(uint8_t lock) {
    switch (lock) {
        case SDL_ORIENTATION_PORTRAIT:          return "Portrait";
        case SDL_ORIENTATION_PORTRAIT_FLIPPED:  return "PortraitFlipped";
        case SDL_ORIENTATION_LANDSCAPE:         return "Landscape (LandscapeRight)";
        case SDL_ORIENTATION_LANDSCAPE_FLIPPED: return "LandscapeFlipped (LandscapeLeft)";
        case kLockAnyLandscape:                 return "AnyLandscape";
        default:                                return "Unknown";
    }
}

static void geRefreshViewControllers() {
    for (UIScene *s in UIApplication.sharedApplication.connectedScenes) {
        if (![s isKindOfClass:[UIWindowScene class]]) continue;
        UIWindowScene *scene = (UIWindowScene *)s;
        for (UIWindow *w in scene.windows) {
            UIViewController *vc = w.rootViewController;
            if (!vc) continue;
            spyderSwizzleOrientationMethods([vc class]);
            [vc setNeedsUpdateOfSupportedInterfaceOrientations];
            SEL updateSel = @selector(setNeedsUpdateOfPrefersInterfaceOrientationLocked);
            if ([vc respondsToSelector:updateSel]) {
#pragma clang diagnostic push
#pragma clang diagnostic ignored "-Warc-performSelector-leaks"
                [vc performSelector:updateSel];
#pragma clang diagnostic pop
            }
            for (UIViewController *child in vc.childViewControllers) {
                spyderSwizzleOrientationMethods([child class]);
                [child setNeedsUpdateOfSupportedInterfaceOrientations];
                if ([child respondsToSelector:updateSel]) {
#pragma clang diagnostic push
#pragma clang diagnostic ignored "-Warc-performSelector-leaks"
                    [child performSelector:updateSel];
#pragma clang diagnostic pop
                }
            }
        }
    }
}

// Geometry rotate while freeze is off. Safe to call repeatedly.
// Log presented glass vs screen. On iPadOS 26.4 sim with portrait chassis,
// landscape interface can letterbox (scale-to-fit) instead of rotating to
// full-bleed the way iOS 18.6 does — metrics reveal which case we are in.
static void geLogSurfaceMetrics(const char* tag) {
    CGRect sb = UIScreen.mainScreen.bounds;
    CGRect nb = UIScreen.mainScreen.nativeBounds;
    SPDLOG_INFO("orient-metrics[{}]: UIScreen.bounds={:.0f}x{:.0f} (pts) "
                "nativeBounds={:.0f}x{:.0f} scale={:.2f}",
                tag, sb.size.width, sb.size.height,
                nb.size.width, nb.size.height,
                UIScreen.mainScreen.scale);
    for (UIScene *s in UIApplication.sharedApplication.connectedScenes) {
        if (![s isKindOfClass:[UIWindowScene class]]) continue;
        UIWindowScene *scene = (UIWindowScene *)s;
        UIInterfaceOrientation io = UIInterfaceOrientationUnknown;
        if (@available(iOS 16.0, *)) {
            io = scene.effectiveGeometry.interfaceOrientation;
        }
        SPDLOG_INFO("orient-metrics[{}]: scene.interfaceOrientation={}",
                    tag, (int)io);
        for (UIWindow *w in scene.windows) {
            SPDLOG_INFO(
                "orient-metrics[{}]: window.bounds={:.0f}x{:.0f} frame={:.0f}x{:.0f} "
                "transform=[{:.2f} {:.2f}; {:.2f} {:.2f}]",
                tag,
                w.bounds.size.width, w.bounds.size.height,
                w.frame.size.width, w.frame.size.height,
                w.transform.a, w.transform.b, w.transform.c, w.transform.d);
            UIView *v = w.rootViewController.view;
            if (v) {
                SPDLOG_INFO(
                    "orient-metrics[{}]: rootView.bounds={:.0f}x{:.0f} "
                    "frame={:.0f},{:.0f} {:.0f}x{:.0f}",
                    tag,
                    v.bounds.size.width, v.bounds.size.height,
                    v.frame.origin.x, v.frame.origin.y,
                    v.frame.size.width, v.frame.size.height);
            }
        }
    }
}

// Push UIDevice.orientation toward a landscape value so the host presentation
// (Simulator glass / simctl composite) applies the same 90° full-bleed mapping
// iOS 18.6 used. UIKit already reports landscape interface + landscape
// UIScreen.bounds while the *physical* chassis stays portrait; without this
// kick, iPadOS 26.4 scale-to-fits the landscape framebuffer into the portrait
// device frame (black bars). KVC on UIDevice.orientation is the long-standing
// game-engine path to request that presentation; it does not rotate the
// Simulator chrome via Cmd+Arrow (chassis stays portrait).
static void gePushDeviceOrientationForMask(UIInterfaceOrientationMask requested) {
    const bool wantLandscape =
        (requested & UIInterfaceOrientationMaskLandscape) != 0 &&
        (requested & (UIInterfaceOrientationMaskPortrait |
                      UIInterfaceOrientationMaskPortraitUpsideDown)) == 0;
    if (!wantLandscape) return;

    UIDeviceOrientation target = UIDeviceOrientationLandscapeLeft;
    if ((requested & UIInterfaceOrientationMaskLandscapeRight) != 0 &&
        (requested & UIInterfaceOrientationMaskLandscapeLeft) == 0) {
        // LandscapeRight interface ↔ device LandscapeLeft (Apple's classic swap).
        target = UIDeviceOrientationLandscapeLeft;
    } else if ((requested & UIInterfaceOrientationMaskLandscapeLeft) != 0 &&
               (requested & UIInterfaceOrientationMaskLandscapeRight) == 0) {
        target = UIDeviceOrientationLandscapeRight;
    }

    UIDevice *dev = [UIDevice currentDevice];
    if (!g_deviceOrientationActive) {
        [dev beginGeneratingDeviceOrientationNotifications];
        g_deviceOrientationActive = true;
    }
    @try {
        [dev setValue:@(target) forKey:@"orientation"];
        SPDLOG_INFO("playerForceOrientation: UIDevice.orientation KVC → {}",
                    (int)target);
    } @catch (NSException *ex) {
        SPDLOG_WARN("playerForceOrientation: UIDevice.orientation KVC failed: {}",
                    ex.reason.UTF8String ?: "?");
    }
#if !TARGET_OS_TV
#pragma clang diagnostic push
#pragma clang diagnostic ignored "-Wdeprecated-declarations"
    [UIViewController attemptRotationToDeviceOrientation];
#pragma clang diagnostic pop
#endif
}

static void geCounterLetterboxIfNeeded(UIInterfaceOrientationMask requested) {
    gePushDeviceOrientationForMask(requested);
}

static void geRequestGeometry(UIInterfaceOrientationMask requested) {
    geEnsureOrientationSwizzles();
    geRefreshViewControllers();

    NSInteger scenes = 0;
    for (UIScene *s in UIApplication.sharedApplication.connectedScenes) {
        if (![s isKindOfClass:[UIWindowScene class]]) continue;
        UIWindowScene *scene = (UIWindowScene *)s;
        ++scenes;
        if (@available(iOS 16.0, *)) {
            UIWindowSceneGeometryPreferencesIOS *prefs =
                [[UIWindowSceneGeometryPreferencesIOS alloc]
                    initWithInterfaceOrientations:requested];
            [scene requestGeometryUpdateWithPreferences:prefs
                errorHandler:^(NSError *error) {
                    if (error) {
                        SPDLOG_WARN("playerForceOrientation: geometry update: {}",
                                    error.localizedDescription.UTF8String);
                    }
                }];
        }
    }
    if (scenes == 0) {
        SPDLOG_INFO("playerForceOrientation: no UIWindowScene yet — supported mask "
                    "armed; will re-apply when scenes exist");
    }
    geLogSurfaceMetrics("post-geometry");
    geCounterLetterboxIfNeeded(requested);
}

static void geTryFreezeIfMatched(UIInterfaceOrientationMask requested) {
    if (g_supportedOrientation == 0) return;
    if (g_freezeOrientation) return;
    if (!spyderInterfaceMatchesMask(requested)) {
        SPDLOG_INFO("playerForceOrientation: interface not yet in mask 0x{:x} — "
                    "keeping freeze off",
                    (unsigned)requested);
        return;
    }
    g_freezeOrientation = true;
    geRefreshViewControllers();
    SPDLOG_INFO("playerForceOrientation: freeze ON (interface matches mask 0x{:x})",
                (unsigned)requested);
}

void playerForceOrientation(uint8_t orientation) {
    if (orientation == 0) return;

    if (!g_deviceOrientationActive) {
        [[UIDevice currentDevice] beginGeneratingDeviceOrientationNotifications];
        g_deviceOrientationActive = true;
    }

    // Narrow support first; do NOT freeze yet — freezing portrait blocks rotate.
    g_supportedOrientation = orientation;
    g_freezeOrientation = false;
    geEnsureOrientationSwizzles();

    UIInterfaceOrientationMask requested = spyderLockMask(orientation);
    SPDLOG_INFO("playerForceOrientation: request {} (mask=0x{:x}) freeze=off",
                spyderLockName(orientation), (unsigned)requested);

    NSArray<NSString*>* plistOrientations =
        [[NSBundle mainBundle] objectForInfoDictionaryKey:@"UISupportedInterfaceOrientations"];
    if (plistOrientations) {
        UIInterfaceOrientationMask plistMask = 0;
        for (NSString* o in plistOrientations) {
            if ([o isEqualToString:@"UIInterfaceOrientationPortrait"])
                plistMask |= UIInterfaceOrientationMaskPortrait;
            else if ([o isEqualToString:@"UIInterfaceOrientationPortraitUpsideDown"])
                plistMask |= UIInterfaceOrientationMaskPortraitUpsideDown;
            else if ([o isEqualToString:@"UIInterfaceOrientationLandscapeLeft"])
                plistMask |= UIInterfaceOrientationMaskLandscapeLeft;
            else if ([o isEqualToString:@"UIInterfaceOrientationLandscapeRight"])
                plistMask |= UIInterfaceOrientationMaskLandscapeRight;
        }
        if ((plistMask & requested) == 0) {
            SPDLOG_WARN(
                "Info.plist UISupportedInterfaceOrientations (0x{:x}) does not include "
                "requested orientation {} (0x{:x}).",
                (unsigned)plistMask, spyderLockName(orientation), (unsigned)requested);
        }
    }

    void (^applyRotate)(void) = ^{
        geRequestGeometry(requested);
        geTryFreezeIfMatched(requested);
    };

    void (^armFreezeLater)(void) = ^{
        // Several kicks: geometry animation can take >1 frame on iPadOS 26.
        geRequestGeometry(requested);
        geTryFreezeIfMatched(requested);
        if (!g_freezeOrientation) {
            // Last resort after retries: freeze anyway so swivel cannot undo
            // a partial rotate — but only after we have re-requested geometry.
            g_freezeOrientation = true;
            geRefreshViewControllers();
            SPDLOG_INFO("playerForceOrientation: freeze ON (timeout settle)");
        }
    };

    if ([NSThread isMainThread]) {
        applyRotate();
        dispatch_async(dispatch_get_main_queue(), applyRotate);
        dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(0.05 * NSEC_PER_SEC)),
                       dispatch_get_main_queue(), applyRotate);
        dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(0.20 * NSEC_PER_SEC)),
                       dispatch_get_main_queue(), applyRotate);
        dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(0.50 * NSEC_PER_SEC)),
                       dispatch_get_main_queue(), armFreezeLater);
    } else {
        dispatch_async(dispatch_get_main_queue(), applyRotate);
        dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(0.20 * NSEC_PER_SEC)),
                       dispatch_get_main_queue(), applyRotate);
        dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(0.50 * NSEC_PER_SEC)),
                       dispatch_get_main_queue(), armFreezeLater);
    }
}
