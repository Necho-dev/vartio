//go:build darwin

package agent

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework CoreGraphics -framework Foundation -framework ImageIO
#include <dlfcn.h>
#include <pthread.h>
#include <stdlib.h>
#include <string.h>
#import <CoreGraphics/CoreGraphics.h>
#import <Foundation/Foundation.h>
#import <ImageIO/ImageIO.h>
#import <ScreenCaptureKit/ScreenCaptureKit.h>

// screenAccess reports the Screen Recording grant; older systems have no such check.
static int screenAccess(void) {
	if (@available(macOS 10.15, *)) return CGPreflightScreenCaptureAccess() ? 1 : 0;
	return 1;
}

static int screens(CGDirectDisplayID *ids, int max) {
	uint32_t n = 0;
	if (CGGetActiveDisplayList((uint32_t)max, ids, &n) != kCGErrorSuccess) return 0;
	return (int)n;
}

// ScreenCaptureKit is loaded at run time and its classes looked up by name, so
// the binary has no link-time dependency on it and still starts on old macOS.
static int sckAvailable(void) {
	if (@available(macOS 14.0, *)) {
		static int ok = -1;
		if (ok < 0) {
			ok = dlopen("/System/Library/Frameworks/ScreenCaptureKit.framework/ScreenCaptureKit", RTLD_LAZY) != NULL &&
				NSClassFromString(@"SCScreenshotManager") != nil;
		}
		return ok;
	}
	return 0;
}

static SCShareableContent *gContent API_AVAILABLE(macos(14.0));
static pthread_mutex_t gContentMu = PTHREAD_MUTEX_INITIALIZER;

static int sckRefresh(void) API_AVAILABLE(macos(14.0)) {
	dispatch_semaphore_t sem = dispatch_semaphore_create(0);
	__block SCShareableContent *got = nil;
	id content = NSClassFromString(@"SCShareableContent");
	[content getShareableContentExcludingDesktopWindows:NO onScreenWindowsOnly:YES
		completionHandler:^(SCShareableContent *c, NSError *e) { got = c; dispatch_semaphore_signal(sem); }];
	if (dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, 3 * NSEC_PER_SEC)) != 0) return -1;
	if (got == nil) return -2;
	gContent = got;
	return 0;
}

static SCDisplay *sckFind(CGDirectDisplayID did) API_AVAILABLE(macos(14.0)) {
	for (SCDisplay *d in gContent.displays) {
		if (d.displayID == did) return d;
	}
	return nil;
}

// sckCapture grabs one display scaled to fit maxSide and returns a malloc'd
// JPEG. Errors: -1 no permission/content, -2 display gone, -3 capture, -4 encode.
static void *sckCapture(CGDirectDisplayID did, int maxSide, double quality, int *outLen, int *rc) {
	*rc = 0; *outLen = 0;
	if (@available(macOS 14.0, *)) {
		if (!sckAvailable()) { *rc = -1; return NULL; }
		@autoreleasepool {
			pthread_mutex_lock(&gContentMu);
			if (gContent == nil || sckFind(did) == nil) {
				if (sckRefresh() != 0) {
					pthread_mutex_unlock(&gContentMu);
					*rc = -1;
					return NULL;
				}
			}
			SCDisplay *d = sckFind(did);
			if (d == nil) {
				pthread_mutex_unlock(&gContentMu);
				*rc = -2;
				return NULL;
			}
			SCContentFilter *f = [[NSClassFromString(@"SCContentFilter") alloc] initWithDisplay:d excludingWindows:@[]];
			pthread_mutex_unlock(&gContentMu);
			double scale = f.pointPixelScale > 0 ? f.pointPixelScale : 1;
			double w = f.contentRect.size.width * scale, h = f.contentRect.size.height * scale;
			double side = w > h ? w : h;
			if (maxSide > 0 && side > maxSide) { w = w * maxSide / side; h = h * maxSide / side; }
			SCStreamConfiguration *cfg = [[NSClassFromString(@"SCStreamConfiguration") alloc] init];
			cfg.width = (size_t)(w + 0.5);
			cfg.height = (size_t)(h + 0.5);
			cfg.showsCursor = YES;
			dispatch_semaphore_t sem = dispatch_semaphore_create(0);
			__block CGImageRef img = NULL;
			id mgr = NSClassFromString(@"SCScreenshotManager");
			[mgr captureImageWithFilter:f configuration:cfg completionHandler:^(CGImageRef i, NSError *e) {
				if (i != NULL) img = CGImageRetain(i);
				dispatch_semaphore_signal(sem);
			}];
			if (dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, 3 * NSEC_PER_SEC)) != 0 || img == NULL) {
				pthread_mutex_lock(&gContentMu);
				gContent = nil;
				pthread_mutex_unlock(&gContentMu);
				*rc = -3;
				return NULL;
			}
			NSMutableData *data = [NSMutableData data];
			CGImageDestinationRef dst = CGImageDestinationCreateWithData((__bridge CFMutableDataRef)data, CFSTR("public.jpeg"), 1, NULL);
			if (dst == NULL) { CGImageRelease(img); *rc = -4; return NULL; }
			NSDictionary *props = @{(__bridge NSString *)kCGImageDestinationLossyCompressionQuality: @(quality)};
			CGImageDestinationAddImage(dst, img, (__bridge CFDictionaryRef)props);
			bool ok = CGImageDestinationFinalize(dst);
			CFRelease(dst);
			CGImageRelease(img);
			if (!ok) { *rc = -4; return NULL; }
			void *out = malloc(data.length);
			memcpy(out, data.bytes, data.length);
			*outLen = (int)data.length;
			return out;
		}
	}
	*rc = -1;
	return NULL;
}
*/
import "C"

import (
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"os/exec"
	"strconv"
	"sync"

	"vartio/internal/entities"
)

func interactiveSession() error {
	return nil
}

var (
	dispMu  sync.Mutex
	dispIDs []C.CGDirectDisplayID
	// sckOff is set after ScreenCaptureKit fails, so later frames go straight
	// to screencapture instead of paying for a failed attempt each time.
	sckOff bool
)

// listScreens keeps CoreGraphics' order (main display first), which is also
// the numbering screencapture -D uses.
func listScreens() []entities.Screen {
	var ids [16]C.CGDirectDisplayID
	n := int(C.screens(&ids[0], C.int(len(ids))))
	out := make([]entities.Screen, 0, n)
	for i := range n {
		id := ids[i]
		b := C.CGDisplayBounds(id)
		name := "外接显示器"
		if C.CGDisplayIsBuiltin(id) != 0 {
			name = "内建显示器"
		}
		out = append(out, entities.Screen{Index: i, Name: name,
			X: int(b.origin.x), Y: int(b.origin.y), Width: int(b.size.width), Height: int(b.size.height),
			Primary: C.CGDisplayIsMain(id) != 0})
	}
	dispMu.Lock()
	dispIDs = append([]C.CGDirectDisplayID(nil), ids[:n]...)
	sckOff = C.sckAvailable() == 0
	dispMu.Unlock()
	return out
}

func captureFrame(sc entities.Screen, p displayProfile) ([]byte, error) {
	dispMu.Lock()
	off := sckOff
	id := C.CGMainDisplayID()
	if sc.Index >= 0 && sc.Index < len(dispIDs) {
		id = dispIDs[sc.Index]
	}
	dispMu.Unlock()
	if !off {
		b, err := sckFrame(id, p)
		if err == nil {
			return b, nil
		}
		dispMu.Lock()
		sckOff = true
		dispMu.Unlock()
	}
	return screencaptureFrame(sc, p)
}

func sckFrame(id C.CGDirectDisplayID, p displayProfile) ([]byte, error) {
	var n, rc C.int
	ptr := C.sckCapture(id, C.int(p.MaxSide), C.double(float64(p.Quality)/100), &n, &rc)
	if ptr == nil {
		return nil, fmt.Errorf("screencapturekit: code %d", int(rc))
	}
	defer C.free(ptr)
	return C.GoBytes(ptr, n), nil
}

func screencaptureFrame(sc entities.Screen, p displayProfile) ([]byte, error) {
	f, err := os.CreateTemp("", "probe-display-*.jpg")
	if err != nil {
		return nil, err
	}
	name := f.Name()
	_ = f.Close()
	defer os.Remove(name)

	cmd := exec.Command("screencapture", "-x", "-D", strconv.Itoa(sc.Index+1), "-t", "jpg", name)
	if out, err := cmd.CombinedOutput(); err != nil {
		if C.screenAccess() == 0 {
			return nil, &entities.FrameError{Code: "no_user_session", Message: "Vartio 未获得屏幕录制权限：在「系统设置 → 隐私与安全性 → 录屏与系统录音」中开启 probe-agent（隐私列表里的名称；如已开启，先用 − 移除旧条目再重新添加），然后重启"}
		}
		msg := string(out)
		if msg == "" {
			msg = err.Error()
		}
		return nil, &entities.FrameError{Code: "no_user_session", Message: "screen capture failed: " + msg}
	}
	rf, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer rf.Close()
	img, _, err := image.Decode(rf)
	if err != nil {
		return nil, err
	}
	return encodeJPEG(img, p)
}
