// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0
//
// iOS/tvOS impl of spyder::applyImmersive — hide the system status bar so
// direct and stream glass match pixel-for-pixel (no clock/battery chrome).
// Status-bar contents (time, network, battery) are non-deterministic and
// break visual regression / stream↔direct parity.
//
// Stack:
//   1. Swizzle prefersStatusBarHidden on UIViewController *and* on SDL's
//      UIKit view controller (it overrides the method, so a base-class
//      swizzle alone is a no-op for the real glass).
//   2. setNeedsStatusBarAppearanceUpdate after toggle (main queue + kick).
//
// Navigation / home indicator chrome is OS-managed on modern iPad; status
// bar is the main non-deterministic overlay for visual regression.

#include "Immersive.h"

#import <UIKit/UIKit.h>
#import <objc/runtime.h>

#include <spdlog/spdlog.h>

namespace {

bool g_immersive = false;

void spyderSwizzlePrefersStatusBarHidden(Class cls) {
    if (!cls) return;
    SEL sel = @selector(prefersStatusBarHidden);
    Method orig = class_getInstanceMethod(cls, sel);
    if (!orig) return;
    // Guard: skip if we already wrapped this class (load + apply may re-enter).
    static char kSwizzledKey;
    if (objc_getAssociatedObject((id)cls, &kSwizzledKey)) return;

    // class_replaceMethod installs on *this* class even when the method
    // only lived on a superclass. Subclasses that override keep their own
    // entry and must be swizzled separately (SDL_uikitviewcontroller).
    IMP originalImp = method_getImplementation(orig);
    IMP blockImp = imp_implementationWithBlock(^BOOL(id self) {
        if (g_immersive) return YES;
        using OrigFn = BOOL (*)(id, SEL);
        return ((OrigFn)originalImp)(self, sel);
    });
    class_replaceMethod(cls, sel, blockImp, method_getTypeEncoding(orig));
    objc_setAssociatedObject((id)cls, &kSwizzledKey, @YES,
                             OBJC_ASSOCIATION_RETAIN_NONATOMIC);
}

void spyderSwizzleStatusBarAnimation(Class cls) {
    if (!cls) return;
    SEL sel = @selector(preferredStatusBarUpdateAnimation);
    Method orig = class_getInstanceMethod(cls, sel);
    if (!orig) return;
    static char kSwizzledAnimKey;
    if (objc_getAssociatedObject((id)cls, &kSwizzledAnimKey)) return;
    IMP originalImp = method_getImplementation(orig);
    IMP blockImp = imp_implementationWithBlock(
        ^UIStatusBarAnimation(id self) {
            if (g_immersive) return UIStatusBarAnimationFade;
            using OrigFn = UIStatusBarAnimation (*)(id, SEL);
            return ((OrigFn)originalImp)(self, sel);
        });
    class_replaceMethod(cls, sel, blockImp, method_getTypeEncoding(orig));
    objc_setAssociatedObject((id)cls, &kSwizzledAnimKey, @YES,
                             OBJC_ASSOCIATION_RETAIN_NONATOMIC);
}

void spyderSwizzleClassTree(Class cls) {
    for (Class c = cls; c && c != [NSObject class]; c = class_getSuperclass(c)) {
        spyderSwizzlePrefersStatusBarHidden(c);
        spyderSwizzleStatusBarAnimation(c);
    }
}

void spyderUpdateStatusBarAppearance() {
    // SDL ships as SDL_uikitviewcontroller — override lives there, not on
    // UIViewController. Resolve by name so we don't hard-link vendor types.
    spyderSwizzlePrefersStatusBarHidden(NSClassFromString(@"SDL_uikitviewcontroller"));
    spyderSwizzleStatusBarAnimation(NSClassFromString(@"SDL_uikitviewcontroller"));
    spyderSwizzlePrefersStatusBarHidden(NSClassFromString(@"SDLUIKitDelegate"));

    for (UIScene *s in UIApplication.sharedApplication.connectedScenes) {
        if (![s isKindOfClass:[UIWindowScene class]]) continue;
        UIWindowScene *scene = (UIWindowScene *)s;
        for (UIWindow *w in scene.windows) {
            UIViewController *vc = w.rootViewController;
            if (!vc) continue;
            // Swizzle the concrete class actually asked for status-bar state.
            spyderSwizzleClassTree([vc class]);
            for (UIViewController *child in vc.childViewControllers) {
                spyderSwizzleClassTree([child class]);
                [child setNeedsStatusBarAppearanceUpdate];
            }
            if (vc.presentedViewController) {
                spyderSwizzleClassTree([vc.presentedViewController class]);
                [vc.presentedViewController setNeedsStatusBarAppearanceUpdate];
            }
            [vc setNeedsStatusBarAppearanceUpdate];
        }
    }
}

} // namespace

@interface UIViewController (SpyderImmersiveStatusBar)
@end

@implementation UIViewController (SpyderImmersiveStatusBar)

+ (void)load {
    spyderSwizzlePrefersStatusBarHidden([UIViewController class]);
    spyderSwizzleStatusBarAnimation([UIViewController class]);
}

@end

namespace spyder {

void applyImmersive(bool enabled) {
    g_immersive = enabled;
    SPDLOG_INFO("applyImmersive(iOS): statusBar hidden={}", enabled);

    void (^apply)(void) = ^{
        spyderUpdateStatusBarAppearance();
    };
    if ([NSThread isMainThread]) {
        apply();
        // Second kick after SDL attaches its root VC (same timing class as
        // playerForceOrientation post-glass).
        dispatch_async(dispatch_get_main_queue(), apply);
        dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(0.15 * NSEC_PER_SEC)),
                       dispatch_get_main_queue(), apply);
    } else {
        dispatch_async(dispatch_get_main_queue(), apply);
        dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(0.15 * NSEC_PER_SEC)),
                       dispatch_get_main_queue(), apply);
    }
}

} // namespace spyder
